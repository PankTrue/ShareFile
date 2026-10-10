import Foundation
import Security

struct AppFailure: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

// Secrets travel only through private pipes; never through argv, preferences or logs.
final class BridgeCall: @unchecked Sendable {
    private let process = Process()
    private let lock = NSLock()
    private var cancelled = false

    func interrupt() {
        lock.lock(); cancelled = true
        if process.isRunning { process.interrupt() }
        lock.unlock()
    }

    func run(_ input: [String: Any], progress: @escaping @Sendable ([String: Any]) -> Void = { _ in }) async throws -> Any {
        let data = try JSONSerialization.data(withJSONObject: input)
        guard let directory = Bundle.main.executableURL?.deletingLastPathComponent() else {
            throw AppFailure(message: "Не найдено ядро приложения")
        }
        return try await withTaskCancellationHandler(operation: {
          try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                let stdin = Pipe(), stdout = Pipe()
                self.process.executableURL = directory.appendingPathComponent("sharefile-client")
                self.process.standardInput = stdin
                self.process.standardOutput = stdout
                self.process.standardError = FileHandle.nullDevice
                do {
                    self.lock.lock()
                    defer { self.lock.unlock() }
                    if self.cancelled { throw CancellationError() }
                    try self.process.run()
                } catch {
                    continuation.resume(throwing: AppFailure(message: "Не удалось запустить ядро приложения")); return
                }
                do {
                    try stdin.fileHandleForWriting.write(contentsOf: data)
                    try stdin.fileHandleForWriting.close()
                } catch { self.interrupt() }
                var buffer = Data()
                var final: Any?
                var failure: Error?
                while true {
                    let bytes = stdout.fileHandleForReading.availableData
                    if bytes.isEmpty { break }
                    buffer.append(bytes)
                    if buffer.count > 16_000_000 { self.interrupt(); failure = AppFailure(message: "Слишком большой ответ ядра"); break }
                    while let newline = buffer.firstIndex(of: 10) {
                        let line = buffer[..<newline]
                        buffer.removeSubrange(...newline)
                        guard let event = (try? JSONSerialization.jsonObject(with: Data(line))) as? [String: Any] else { continue }
                        switch event["type"] as? String {
                        case "progress": if var job = event["job"] as? [String: Any] {
                            job["transferred"] = event["transferred"] ?? 0
                            progress(job)
                        }
                        case "result": final = event["result"] ?? NSNull()
                        case "error": failure = AppFailure(message: event["error"] as? String ?? "Ошибка ядра")
                        default: break
                        }
                    }
                }
                self.process.waitUntilExit()
                if let failure { continuation.resume(throwing: failure) }
                else if let final { continuation.resume(returning: final) }
                else { continuation.resume(throwing: AppFailure(message: "Передача прервана. Её можно продолжить")) }
            }
          }
        }, onCancel: { self.interrupt() })
    }
}

enum CredentialStore {
    private static let service = "app.sharefile.desktop.credentials.v1"
    private static func query(_ account: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account]
    }
    // The local login keychain is used by this non-sandboxed, locally signed desktop app.
    // Calls run off the main thread because Keychain may wait for user interaction.
    static func read(_ account: String) throws -> String? {
        var q = query(account)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data, let value = String(data: data, encoding: .utf8) else {
            throw AppFailure(message: "Keychain не предоставил доступ к подключению (\(status))")
        }
        return value
    }
    static func write(_ account: String, value: String) throws {
        let q = query(account)
        let attrs: [String: Any] = [kSecValueData as String: Data(value.utf8)]
        var status = SecItemUpdate(q as CFDictionary, attrs as CFDictionary)
        if status == errSecItemNotFound { status = SecItemAdd(q.merging(attrs) { _, new in new } as CFDictionary, nil) }
        guard status == errSecSuccess else { throw AppFailure(message: "Не удалось сохранить подключение в Keychain (\(status))") }
    }
    static func delete(_ account: String) throws {
        let status = SecItemDelete(query(account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw AppFailure(message: "Не удалось удалить подключение из Keychain (\(status))") }
    }
}
