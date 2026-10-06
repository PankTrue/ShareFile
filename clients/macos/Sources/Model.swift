import AppKit
import SwiftUI

struct Upload: Codable, Identifiable {
    var id: String
    var source: String
    var name: String
    var size: Int64
    var modified: Int64
    var fingerprint: String
    var folder: String
    var upload: String
    var file_id: String
    var offset: Int64
    var status = "queued"
    var message = ""
    var batch = ""
    var days = 7
    var never = false
    var shareID = ""
    // Public capabilities remain in memory and are deliberately excluded from persisted state.
    var link: String = ""
    enum CodingKeys: String, CodingKey {
        case id, source, name, size, modified, fingerprint, folder, upload, file_id, offset
        case status, message, batch, days, never, shareID
    }
    var checkpoint: [String: Any] {
        ["id": id, "source": source, "name": name, "size": size, "modified": modified,
         "fingerprint": fingerprint, "folder": folder, "upload": upload, "file_id": file_id, "offset": offset]
    }
    var label: String {
        switch status {
        case "queued": return "В очереди"
        case "uploading": return "Загружается"
        case "paused": return "На паузе"
        case "uploaded": return "Файл загружен"
        case "sharing": return "Создаётся ссылка"
        case "uncertain": return "Проверьте ссылки в Web UI"
        case "done": return "Готово"
        case "cancelling": return "Отменяется"
        default: return "Нужна ваша помощь"
        }
    }
}
struct Folder: Identifiable {
    let id: String
    let name: String
    let role: Int
    init?(_ raw: [String: Any]) {
        guard let id = raw["id"] as? String, let name = raw["name"] as? String else { return nil }
        self.id = id; self.name = name; role = raw["effective_role"] as? Int ?? 0
    }
}
struct Preferences: Codable {
    var origin = ""
    var owner = ""
    var username = ""
    var folder = ""
    var folderName = "Uploads"
    var days = 7
    var never = false
    var silentFinderUploads = true
    var notifyUploads = true
    var uploads: [Upload] = []

    enum CodingKeys: String, CodingKey {
        case origin, owner, username, folder, folderName, days, never, uploads
        case silentFinderUploads, notifyUploads
    }
    init(uploads: [Upload] = []) { self.uploads = uploads }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        origin = try values.decodeIfPresent(String.self, forKey: .origin) ?? ""
        owner = try values.decodeIfPresent(String.self, forKey: .owner) ?? ""
        username = try values.decodeIfPresent(String.self, forKey: .username) ?? ""
        folder = try values.decodeIfPresent(String.self, forKey: .folder) ?? ""
        folderName = try values.decodeIfPresent(String.self, forKey: .folderName) ?? "Uploads"
        days = try values.decodeIfPresent(Int.self, forKey: .days) ?? 7
        never = try values.decodeIfPresent(Bool.self, forKey: .never) ?? false
        uploads = try values.decodeIfPresent([Upload].self, forKey: .uploads) ?? []
        silentFinderUploads = try values.decodeIfPresent(Bool.self, forKey: .silentFinderUploads) ?? true
        notifyUploads = try values.decodeIfPresent(Bool.self, forKey: .notifyUploads) ?? true
    }
}

struct UploadCompletion: Equatable {
    let names: [String]
    let copied: Bool
    var title: String { names.count == 1 ? "Файл загружен" : "Файлы загружены" }
    var body: String {
        let files = names.count == 1 ? names[0] : "Загружено файлов: \(names.count)"
        let result = copied ? (names.count == 1 ? "Ссылка скопирована" : "Ссылки скопированы") : "Не удалось скопировать ссылку. Откройте ShareFile"
        return files + "\n" + result
    }
}

@MainActor final class Model: ObservableObject {
    @Published var settings = Preferences()
    @Published var serverInput = ""
    @Published var token = ""
    @Published var password = ""
    @Published var page = "files"
    @Published var busy = false
    @Published var pairingURL = ""
    @Published var notice = ""
    @Published var fileErrors: [String] = []
    @Published var folders: [Folder] = []
    @Published var folderTrail: [(String, String)] = []
    @Published var showFolders = false
    private var pairTask: Task<Void, Never>?
    private var activeCall: BridgeCall?
    private var activeID: String?
    private var pumping = false
    private var suspended = false
    private var pendingFiles: [URL] = []
    private var copiedBatches = Set<String>()
    private var preparingBatches = Set<String>()
    private let stateURL: URL
    private let writeClipboard: (String) -> Bool
    var onUploadCompletion: ((UploadCompletion) -> Void)?
    var connected: Bool { !token.isEmpty && !settings.owner.isEmpty }
    var canQuitSafely: Bool { activeID == nil }
    var activeCount: Int { settings.uploads.filter { ["queued", "uploading", "sharing"].contains($0.status) }.count }
    var keyAccount: String { "device:" + settings.origin }

    init(stateURL: URL, writeClipboard: @escaping (String) -> Bool = {
        NSPasteboard.general.clearContents()
        return NSPasteboard.general.setString($0, forType: .string)
    }) {
        self.stateURL = stateURL
        self.writeClipboard = writeClipboard
        if let data = try? Data(contentsOf: stateURL), let saved = try? JSONDecoder().decode(Preferences.self, from: data) {
            settings = saved
            for index in settings.uploads.indices {
                switch settings.uploads[index].status {
                case "queued", "uploading", "cancelling": settings.uploads[index].status = "paused"
                case "sharing": settings.uploads[index].status = "uncertain"; settings.uploads[index].message = "Сервер мог создать ссылку. Проверьте её в Web UI перед созданием новой."
                default: break
                }
            }
        }
        serverInput = settings.origin
    }
    func save() {
        do {
            try FileManager.default.createDirectory(at: stateURL.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            try JSONEncoder().encode(settings).write(to: stateURL, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: stateURL.path)
        } catch { notice = "Не удалось сохранить очередь. Проверьте свободное место на Mac" }
    }
    func request(_ operation: String, _ extra: [String: Any] = [:], call: BridgeCall = BridgeCall(), progress: @escaping @Sendable ([String: Any]) -> Void = { _ in }) async throws -> Any {
        try await call.run(["operation": operation, "origin": settings.origin, "token": token].merging(extra) { _, new in new }, progress: progress)
    }
    func restoreConnection() async {
        guard !settings.origin.isEmpty else { return }
        busy = true
        defer {
            busy = false
            if connected {
                if !pendingFiles.isEmpty { let files = pendingFiles; pendingFiles = []; add(files) }
                pump()
            }
        }
        do {
            let account = keyAccount
            let saved = try await Task.detached { try CredentialStore.read(account) }.value
            guard let saved else { return }
            token = saved
            try await validateSession()
            password = try await Task.detached { try CredentialStore.read("password:" + account) ?? "" }.value
        } catch { token = ""; notice = error.localizedDescription }
    }
    private func validateSession() async throws {
        guard let raw = try await request("session") as? [String: Any], let user = raw["user"] as? [String: Any], let owner = user["id"] as? String else { throw AppFailure(message: "Некорректный ответ сервера") }
        if !settings.owner.isEmpty && settings.owner != owner && !settings.uploads.isEmpty {
            token = ""; throw AppFailure(message: "Очередь принадлежит другому аккаунту. Удалите завершённые записи и отмените незавершённые загрузки перед сменой аккаунта")
        }
        settings.owner = owner; settings.username = user["username"] as? String ?? ""
        if settings.folder.isEmpty, let home = user["home_id"] as? String {
            let nodes = try await request("folders", ["parent": home]) as? [[String: Any]] ?? []
            if let uploads = nodes.compactMap(Folder.init).first(where: { $0.name == "Uploads" && $0.role >= 3 }) {
                settings.folder = uploads.id; settings.folderName = uploads.name
            } else { settings.folder = home; settings.folderName = "Личная папка" }
        }
        save()
    }
    func connect() {
        guard !busy else { return }
        if connected { notice = "Сначала отключите текущее подключение"; return }
        let old = settings.origin
        let new = serverInput.trimmingCharacters(in: .whitespacesAndNewlines).trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if old != new && !settings.uploads.isEmpty { notice = "Очередь связана с текущим сервером. Завершите или отмените её перед сменой адреса"; return }
        settings.origin = new
        busy = true; notice = ""; pairingURL = ""
        pairTask = Task {
            defer { busy = false; pairingURL = ""; pairTask = nil }
            do {
                let name = "Mac · " + (Host.current().localizedName ?? "ShareFile")
                guard let result = try await request("pair_start", ["name": String(name.prefix(80))]) as? [String: Any], let verification = result["verification_url"] as? String, let raw = result["request"] as? [String: Any], let id = raw["id"] as? String, let secret = raw["secret"] as? String else { throw AppFailure(message: "Не удалось начать подключение") }
                pairingURL = verification
                if let url = URL(string: verification) { NSWorkspace.shared.open(url) }
                let interval = max(result["interval"] as? Int ?? 12, 12)
                let expiry = raw["expires_at"] as? Double ?? Date().timeIntervalSince1970 + 600
                while Date().timeIntervalSince1970 < expiry {
                    try await Task.sleep(nanoseconds: UInt64(interval) * 1_000_000_000)
                    try Task.checkCancellation()
                    let poll = try await request("pair_poll", ["request_id": id, "secret": secret]) as? [String: Any] ?? [:]
                    if let value = poll["token"] as? String, !value.isEmpty {
                        token = value
                        try await validateSession()
                        let account = keyAccount
                        try await Task.detached { try CredentialStore.write(account, value: value) }.value
                        notice = "Mac подключён. Можно загружать файлы"
                        page = "files"
                        if !pendingFiles.isEmpty { let files = pendingFiles; pendingFiles = []; add(files) }
                        return
                    }
                }
                throw AppFailure(message: "Время подтверждения истекло. Подключите Mac заново")
            } catch is CancellationError { notice = "Подключение отменено" }
            catch { token = ""; settings.origin = old; notice = Task.isCancelled ? "Подключение отменено" : error.localizedDescription }
        }
    }
    func cancelPairing() { pairTask?.cancel() }
    func disconnect() {
        guard activeID == nil && !busy else { notice = "Приостановите загрузку перед отключением"; return }
        busy = true
        Task {
            defer { busy = false }
            do { let account = keyAccount; try await Task.detached { try CredentialStore.delete(account) }.value; token = ""; notice = "Подключение удалено с Mac. Отозвать устройство можно в Web UI" }
            catch { notice = error.localizedDescription }
        }
    }
    func saveOptions() {
        if !password.isEmpty && !(12...256).contains(password.utf8.count) { notice = "Пароль ссылки: от 12 до 256 байт UTF-8"; return }
        let value = password, account = "password:" + keyAccount
        Task {
            do { try await Task.detached { if value.isEmpty { try CredentialStore.delete(account) } else { try CredentialStore.write(account, value: value) } }.value; save(); notice = "Настройки сохранены" }
            catch { notice = error.localizedDescription }
        }
    }
    func setSilentFinderUploads(_ enabled: Bool) { settings.silentFinderUploads = enabled; save() }
    func setNotifyUploads(_ enabled: Bool) { settings.notifyUploads = enabled; save() }
    func chooseFiles() {
        let panel = NSOpenPanel(); panel.canChooseDirectories = false; panel.canChooseFiles = true; panel.allowsMultipleSelection = true
        panel.prompt = "Загрузить и поделиться"
        panel.begin { response in if response == .OK { Task { @MainActor in self.add(panel.urls) } } }
    }
    func add(_ urls: [URL]) {
        guard connected else { pendingFiles.append(contentsOf: urls); page = "settings"; notice = "Подключите хранилище. Выбранные файлы добавятся после подтверждения"; return }
        guard !settings.folder.isEmpty else { notice = "Выберите папку назначения"; return }
        guard password.isEmpty || (12...256).contains(password.utf8.count) else { notice = "Исправьте пароль ссылки в настройках"; return }
        let folder = settings.folder, batch = UUID().uuidString, days = settings.days, never = settings.never
        preparingBatches.insert(batch)
        Task {
            defer { preparingBatches.remove(batch); finishBatch(batch) }
            for url in urls {
                if settings.uploads.count >= 100 { notice = "В очереди уже 100 файлов. Удалите завершённые записи"; break }
                do {
                    let raw = try await request("prepare", ["source": url.path, "parent": folder])
                    let data = try JSONSerialization.data(withJSONObject: raw)
                    var job = try JSONDecoder().decode(Upload.self, from: dataWithJobDefaults(data))
                    job.batch = batch; job.days = days; job.never = never
                    settings.uploads.append(job); save()
                } catch { fileErrors.append(url.lastPathComponent + ": " + error.localizedDescription) }
            }
            page = "files"; suspended = false; pump()
        }
    }
    private func dataWithJobDefaults(_ data: Data) throws -> Data {
        let raw = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        return try JSONSerialization.data(withJSONObject: ["status": "queued", "message": "", "batch": "", "days": 7, "never": false, "shareID": ""].merging(raw) { _, new in new })
    }
    func update(_ id: String, persist: Bool = true, _ change: (inout Upload) -> Void) {
        if let index = settings.uploads.firstIndex(where: { $0.id == id }) { change(&settings.uploads[index]); if persist { save() } }
    }
    func resume(_ id: String) { update(id) { $0.status = "queued"; $0.message = "" }; suspended = false; pump() }
    func pause(_ id: String) {
        update(id) { $0.status = "paused" }
        if activeID == id { activeCall?.interrupt() }
    }
    func pauseAll() { suspended = true; for job in settings.uploads where ["queued", "uploading"].contains(job.status) { pause(job.id) } }
    func quit() { suspended = true; activeCall?.interrupt(); save() }
    private func pump() {
        guard !pumping && !suspended && !busy && connected else { return }
        pumping = true
        Task {
            defer { pumping = false; activeID = nil; activeCall = nil }
            while !suspended && connected, let job = settings.uploads.first(where: { $0.status == "queued" }) {
                activeID = job.id; let call = BridgeCall(); activeCall = call
                update(job.id) { $0.status = "uploading" }
                do {
                    let result = try await request("upload", ["job": job.checkpoint], call: call) { raw in
                        Task { @MainActor in
                            let previous = self.settings.uploads.first(where: { $0.id == job.id })
                            let resourceChanged = previous?.upload != raw["upload"] as? String || previous?.file_id != raw["file_id"] as? String
                            // HEAD recovers durable offsets; only resource identity must be checkpointed immediately.
                            self.update(job.id, persist: resourceChanged) { item in
                                item.upload = raw["upload"] as? String ?? item.upload
                                item.file_id = raw["file_id"] as? String ?? item.file_id
                                item.offset = (raw["offset"] as? NSNumber)?.int64Value ?? item.offset
                            }
                        }
                    } as? [String: Any] ?? [:]
                    update(job.id) { $0.upload = result["upload"] as? String ?? $0.upload; $0.file_id = result["file_id"] as? String ?? $0.file_id; $0.offset = job.size }
                    guard settings.uploads.first(where: { $0.id == job.id })?.status == "uploading" else { continue }
                    update(job.id) { $0.status = "uploaded" }
                    await makeLink(job.id)
                } catch {
                    if settings.uploads.first(where: { $0.id == job.id })?.status == "uploading" { update(job.id) { $0.status = "error"; $0.message = error.localizedDescription } }
                }
                activeID = nil; activeCall = nil
            }
        }
    }
    func makeLink(_ id: String) async {
        guard let job = settings.uploads.first(where: { $0.id == id }), connected else { return }
        update(id) { $0.status = "sharing"; $0.message = "" }
        let call = BridgeCall(); activeCall = call
        do {
            let result = try await request("share", ["job": job.checkpoint, "options": ["days": job.days, "never": job.never, "password": password]], call: call) as? [String: Any] ?? [:]
            guard let link = result["url"] as? String else { throw AppFailure(message: "Сервер не вернул ссылку") }
            let shareID = (result["share"] as? [String: Any])?["id"] as? String ?? ""
            update(id) { $0.status = "done"; $0.link = link; $0.shareID = shareID }
            finishBatch(job.batch)
        } catch { update(id) { $0.status = "uncertain"; $0.message = error.localizedDescription + ". Сервер мог создать ссылку; проверьте Web UI перед повтором." } }
    }
    private func finishBatch(_ id: String) {
        guard !preparingBatches.contains(id), !copiedBatches.contains(id) else { return }
        let batch = settings.uploads.filter { $0.batch == id }
        guard !batch.isEmpty, batch.allSatisfy({ $0.status == "done" && !$0.link.isEmpty }) else { return }
        let copied = copy(batch.map(\.link).joined(separator: "\n"))
        copiedBatches.insert(id)
        if settings.notifyUploads { onUploadCompletion?(UploadCompletion(names: batch.map(\.name), copied: copied)) }
    }
    func remove(_ id: String) {
        guard let job = settings.uploads.first(where: { $0.id == id }), activeID != id else { notice = "Сначала приостановите загрузку"; return }
        if ["done", "uploaded", "uncertain"].contains(job.status) { settings.uploads.removeAll { $0.id == id }; save(); return }
        guard connected else { notice = "Для отмены загрузки подключите тот же аккаунт"; return }
        update(id) { $0.status = "cancelling" }
        Task {
            do { _ = try await request("cancel", ["job": job.checkpoint]); settings.uploads.removeAll { $0.id == id }; save() }
            catch { update(id) { $0.status = "paused"; $0.message = "Отмена не подтверждена: " + error.localizedDescription } }
        }
    }
    func clearFinished() { settings.uploads.removeAll { $0.status == "done" }; save() }
    func retryLink(_ id: String) {
        guard !pumping && !busy else { notice = "Дождитесь завершения текущей операции"; return }
        busy = true
        Task { defer { busy = false; activeCall = nil; pump() }; await makeLink(id) }
    }
    @discardableResult func copy(_ text: String) -> Bool {
        guard !text.isEmpty else { return false }
        let copied = writeClipboard(text)
        notice = copied ? "Ссылки скопированы в буфер обмена" : "Не удалось скопировать ссылки. Попробуйте ещё раз"
        return copied
    }
    func openWeb() { if let url = URL(string: settings.origin) { NSWorkspace.shared.open(url) } }
    func browseFolders(_ parent: String = "", name: String = "Доступные папки", back: Bool = false) {
        Task {
            do {
                let result = try await request("folders", ["parent": parent]) as? [[String: Any]] ?? []
                folders = result.compactMap(Folder.init)
                if !back { folderTrail.append((parent, name)) }
                showFolders = true
            } catch { notice = error.localizedDescription }
        }
    }
    func folderBack() { if folderTrail.count > 1 { folderTrail.removeLast(); let last = folderTrail.last!; browseFolders(last.0, name: last.1, back: true) } }
    func selectFolder(_ folder: Folder) {
        guard folder.role >= 3 else { notice = "Для загрузки с публичной ссылкой нужны права управляющего"; return }
        settings.folder = folder.id; settings.folderName = folder.name; save(); showFolders = false
    }
}
