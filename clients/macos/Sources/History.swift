import Foundation
import CryptoKit

struct SentLink: Codable, Identifiable, Equatable {
    var origin: String
    var owner: String
    var shareID: String
    var jobID: String
    var name: String
    var created: Date
    var expires: Date?
    var revoked = false
    var id: String { shareID }
    var account: String {
        "history:" + SHA256.hash(data: Data((origin + "\n" + owner + "\n" + shareID).utf8)).map { String(format: "%02x", $0) }.joined()
    }
    var active: Bool { !revoked && (expires == nil || expires! > Date()) }
    var label: String {
        if revoked { return "Отозвана" }
        if let expires { return expires <= Date() ? "Срок истёк" : "До " + expires.formatted(date: .numeric, time: .shortened) }
        return "Бессрочно"
    }
}

// Only the capability URL is secret; the bounded index contains names and dates.
@MainActor final class LinkHistory: ObservableObject {
    @Published private(set) var entries: [SentLink] = []
    private let path: URL
    private let read: @Sendable (String) throws -> String?
    private let write: @Sendable (String, String) throws -> Void
    private let delete: @Sendable (String) throws -> Void
    private var available = true
    var loadError = ""
    init(path: URL, read: @escaping @Sendable (String) throws -> String? = { try CredentialStore.read($0) },
         write: @escaping @Sendable (String, String) throws -> Void = { try CredentialStore.write($0, value: $1) },
         delete: @escaping @Sendable (String) throws -> Void = { try CredentialStore.delete($0) }) {
        self.path = path; self.read = read; self.write = write; self.delete = delete
        do {
            if FileManager.default.fileExists(atPath: path.path) {
                let data = try Data(contentsOf: path)
                guard data.count <= 2 << 20 else { throw AppFailure(message: "История слишком большая") }
                let saved = try JSONDecoder().decode([SentLink].self, from: data)
                guard saved.count <= 200, Set(saved.map(\.account)).count == saved.count,
                      saved.allSatisfy({ Self.validID($0.shareID) && Self.validID($0.owner) && Self.validID($0.jobID)
                          && Self.validOrigin($0.origin) && $0.name.count <= 1024 && $0.created.timeIntervalSince1970 > 0 }) else {
                    throw AppFailure(message: "Некорректная история")
                }
                entries = saved
            }
        } catch { available = false; loadError = "Не удалось прочитать history.json. История сохранена для восстановления" }
    }
    static func validID(_ value: String) -> Bool { value.range(of: "\\A[A-Za-z0-9_-]{43}\\z", options: .regularExpression) != nil }
    static func validOrigin(_ value: String) -> Bool {
        guard let url = URL(string: value), let host = url.host, url.user == nil, url.password == nil,
              url.query == nil, url.fragment == nil, url.path.isEmpty else { return false }
        return url.scheme == "https" || (url.scheme == "http" && ["localhost", "127.0.0.1", "[::1]"].contains(host))
    }
    static func validateURL(_ value: String) throws -> URL {
        guard let url = URL(string: value), let host = url.host, url.user == nil, url.password == nil,
              url.query == nil, url.fragment == nil,
              url.path.range(of: "\\A/s/[A-Za-z0-9_-]{43}\\z", options: .regularExpression) != nil,
              url.scheme == "https" || (url.scheme == "http" && ["localhost", "127.0.0.1", "[::1]"].contains(host)) else {
            throw AppFailure(message: "Некорректная ссылка в защищённой истории")
        }
        return url
    }
    private func persist(_ next: [SentLink]) throws {
        let directory = path.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
        // Set the temporary file mode before writing metadata or atomically replacing the index.
        let temporary = directory.appendingPathComponent(".history-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: temporary) }
        FileManager.default.createFile(atPath: temporary.path, contents: nil, attributes: [.posixPermissions: 0o600])
        try JSONEncoder().encode(next).write(to: temporary)
        if FileManager.default.fileExists(atPath: path.path) { _ = try FileManager.default.replaceItemAt(path, withItemAt: temporary) }
        else { try FileManager.default.moveItem(at: temporary, to: path) }
    }
    func forAccount(_ origin: String, _ owner: String) -> [SentLink] { entries.filter { $0.origin == origin && $0.owner == owner } }
    func record(origin: String, owner: String, job: Upload, url: String, shareID: String, created: Date? = nil, expires: Date? = nil) async throws {
        guard available, Self.validID(owner), Self.validID(job.id), Self.validID(shareID), Self.validOrigin(origin) else { throw AppFailure(message: "История недоступна") }
        _ = try Self.validateURL(url)
        let entry = SentLink(origin: origin, owner: owner, shareID: shareID, jobID: job.id, name: job.name,
                             created: created ?? Date(), expires: expires ?? (job.never ? nil : Date().addingTimeInterval(Double(job.days) * 86400)))
        let write = self.write
        try await Task.detached { try write(entry.account, url) }.value
        let previous = entries.filter { $0.account != entry.account }
        let next = Array(([entry] + previous).prefix(200))
        let delete = self.delete
        do { try persist(next) }
        catch {
            if !entries.contains(where: { $0.account == entry.account }) { try? await Task.detached { try delete(entry.account) }.value }
            throw error
        }
        entries = next
        for removed in previous.dropFirst(199) { try await Task.detached { try delete(removed.account) }.value }
    }
    func link(_ entry: SentLink) async throws -> URL {
        guard let current = entries.first(where: { $0.account == entry.account }), current.active else { throw AppFailure(message: "Ссылка отозвана или срок действия истёк") }
        let read = self.read
        guard let value = try await Task.detached(operation: { try read(entry.account) }).value else { throw AppFailure(message: "Ссылка недоступна в Keychain") }
        // A revoke may have completed while Keychain was unlocked.
        guard entries.first(where: { $0.account == entry.account })?.active == true else { throw AppFailure(message: "Ссылка отозвана или срок действия истёк") }
        return try Self.validateURL(value)
    }
    func markRevoked(_ entry: SentLink) async throws {
        entries = entries.map { value in var item = value; if item.account == entry.account { item.revoked = true }; return item }
        let delete = self.delete
        do { try persist(entries) }
        catch { try? await Task.detached { try delete(entry.account) }.value; throw error }
        try await Task.detached { try delete(entry.account) }.value
    }
}
