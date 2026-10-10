import Foundation
import AppKit

@MainActor func verifyClipboardImages() throws {
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("sharefile-clipboard-test-" + UUID().uuidString)
    let pasteboard = NSPasteboard(name: NSPasteboard.Name("ShareFile-image-test-" + UUID().uuidString))
    defer { pasteboard.releaseGlobally(); try? FileManager.default.removeItem(at: directory) }
    let spool = ClipboardImages(directory: directory.appendingPathComponent("clipboard"))
    guard let image = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 2, pixelsHigh: 2, bitsPerSample: 8,
        samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 8, bitsPerPixel: 32),
        let png = image.representation(using: .png, properties: [:]) else { throw AppFailure(message: "Clipboard fixture failed") }
    pasteboard.setData(png, forType: .png)
    let first = try spool.capture(pasteboard), second = try spool.capture(pasteboard)
    guard first != second, let decoded = NSBitmapImageRep(data: try Data(contentsOf: first)), decoded.pixelsWide == 2,
          (try FileManager.default.attributesOfItem(atPath: first.path)[.posixPermissions] as? NSNumber)?.intValue == 0o600 else {
        throw AppFailure(message: "Clipboard PNG, permissions or unique names failed")
    }
    var job = Upload(id: "clipboard-test", source: first.path, name: first.lastPathComponent, size: Int64(png.count),
        modified: 0, fingerprint: "", folder: "", upload: "", file_id: "", offset: 0)
    job.status = "paused"
    spool.reconcile([job])
    guard FileManager.default.fileExists(atPath: first.path), !FileManager.default.fileExists(atPath: second.path) else {
        throw AppFailure(message: "Paused clipboard source was lost")
    }
    let userFile = directory.appendingPathComponent("Screenshot-personal.png"); try png.write(to: userFile)
    spool.remove(userFile.path)
    job.status = "done"; spool.reconcile([job])
    guard !FileManager.default.fileExists(atPath: first.path), FileManager.default.fileExists(atPath: userFile.path) else {
        throw AppFailure(message: "Clipboard cleanup touched user files")
    }
    pasteboard.clearContents(); pasteboard.setString("a link", forType: .string)
    do { _ = try spool.capture(pasteboard); throw AppFailure(message: "Text clipboard accepted as image") }
    catch let error as AppFailure { guard error.message != "Text clipboard accepted as image" else { throw error } }
    pasteboard.clearContents(); pasteboard.setData(png, forType: .png)
    let retained = try spool.capture(pasteboard)
    let state = directory.appendingPathComponent("state.json")
    try Data("broken checkpoint".utf8).write(to: state)
    let corrupt = Model(stateURL: state)
    corrupt.save()
    guard FileManager.default.fileExists(atPath: retained.path), try String(contentsOf: state, encoding: .utf8) == "broken checkpoint" else {
        throw AppFailure(message: "Corrupt state destroyed a pending clipboard source")
    }
}

// Disposable in-memory credentials keep backend smoke tests out of the user's Keychain.
final class TestHistorySecrets: @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String: String] = [:]
    func read(_ account: String) -> String? { lock.lock(); defer { lock.unlock() }; return values[account] }
    func write(_ account: String, _ value: String) { lock.lock(); defer { lock.unlock() }; values[account] = value }
    func delete(_ account: String) { lock.lock(); defer { lock.unlock() }; values.removeValue(forKey: account) }
    var count: Int { lock.lock(); defer { lock.unlock() }; return values.count }
}

@MainActor func verifyLinkHistory() async throws {
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("sharefile-history-test-" + UUID().uuidString)
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let path = directory.appendingPathComponent("history.json")
    let secrets = TestHistorySecrets()
    func history() -> LinkHistory { LinkHistory(path: path, read: { secrets.read($0) }, write: { secrets.write($0, $1) }, delete: { secrets.delete($0) }) }
    let store = history()
    let origin = "https://history-test.invalid", owner = String(repeating: "o", count: 43), identity = String(repeating: "s", count: 43)
    let url = origin + "/s/" + identity
    let job = Upload(id: String(repeating: "j", count: 43), source: "/tmp/Отчёт 日本語.txt", name: "Отчёт 日本語.txt", size: 0, modified: 0, fingerprint: "", folder: "", upload: "", file_id: "", offset: 0)
    try await store.record(origin: origin, owner: owner, job: job, url: url, shareID: identity)
    let restarted = history()
    guard restarted.entries.count == 1, try await restarted.link(restarted.entries[0]).absoluteString == url,
          restarted.forAccount(origin, String(repeating: "x", count: 43)).isEmpty,
          restarted.forAccount("https://other.invalid", owner).isEmpty,
          !(try String(contentsOf: path, encoding: .utf8)).contains("/s/") else { throw AppFailure(message: "History restart, secret-free index or account isolation failed") }
    try await restarted.markRevoked(restarted.entries[0])
    guard history().entries[0].revoked, secrets.count == 0 else { throw AppFailure(message: "History revocation was not persisted") }
    do { _ = try await restarted.link(store.entries[0]); throw AppFailure(message: "Revoked link was returned") }
    catch let error as AppFailure { guard error.message != "Revoked link was returned" else { throw error } }
    let bounded = history()
    for index in 0...200 {
        let shareID = String(repeating: "0", count: 40) + String(format: "%03d", index)
        try await bounded.record(origin: origin, owner: owner, job: job, url: origin + "/s/" + shareID, shareID: shareID)
    }
    guard bounded.entries.count == 200, secrets.count == 200 else { throw AppFailure(message: "History retention failed") }
    var expired = job; expired.days = -1
    try await bounded.record(origin: origin, owner: owner, job: expired, url: url, shareID: identity)
    guard !bounded.entries[0].active else { throw AppFailure(message: "Expired history link is active") }
    try "broken index".write(to: path, atomically: true, encoding: .utf8)
    let corrupt = history()
    guard !corrupt.loadError.isEmpty else { throw AppFailure(message: "Corrupt history was ignored") }
    do { try await corrupt.record(origin: origin, owner: owner, job: job, url: url, shareID: identity); throw AppFailure(message: "Corrupt history overwritten") }
    catch let error as AppFailure { guard error.message != "Corrupt history overwritten" else { throw error } }
    guard try String(contentsOf: path, encoding: .utf8) == "broken index" else { throw AppFailure(message: "Corrupt history was changed") }
    // Exercise the real native store using a unique, disposable scope.
    let native = LinkHistory(path: directory.appendingPathComponent("native.json"))
    let nativeOrigin = "https://" + UUID().uuidString.lowercased() + ".invalid"
    try await native.record(origin: nativeOrigin, owner: owner, job: job, url: url, shareID: identity)
    defer { if let entry = native.entries.first { try? CredentialStore.delete(entry.account) } }
    let nativeRestarted = LinkHistory(path: directory.appendingPathComponent("native.json"))
    guard try await nativeRestarted.link(nativeRestarted.entries[0]).absoluteString == url else { throw AppFailure(message: "Native history Keychain round trip failed") }
    let menuModel = Model(stateURL: directory.appendingPathComponent("menu-state.json"), history: nativeRestarted, writeClipboard: { _ in true })
    menuModel.settings.origin = nativeOrigin; menuModel.settings.owner = owner
    let delegate = AppDelegate(); delegate.model = menuModel
    let menu = NSMenu()
    delegate.menuNeedsUpdate(menu)
    guard let submenu = menu.items.first?.submenu, submenu.items.map(\.title).suffix(3) == ["Copy link", "Open link", "Revoke link"],
          submenu.items[1].isEnabled, submenu.items[2].isEnabled, !submenu.items[3].isEnabled else { throw AppFailure(message: "Recent-link menu did not expose offline copy/open or disabled revoke") }
    menuModel.token = "disposable-menu-test-token"
    delegate.menuNeedsUpdate(menu)
    guard menu.items[0].submenu?.items[3].isEnabled == true else { throw AppFailure(message: "Connected recent-link revoke is disabled") }
    try await nativeRestarted.markRevoked(nativeRestarted.entries[0])
    delegate.menuNeedsUpdate(menu)
    guard menu.items[0].submenu?.items.dropFirst().allSatisfy({ !$0.isEnabled }) == true else { throw AppFailure(message: "Revoked recent-link actions remain enabled") }
}

// Disposable fixture driver exercises the same native model / queue / process bridge as the UI.
// Device tokens arrive through stdin; clipboard and persistent user preferences are untouched.
func integrationSmokeTest() -> Never {
    let data = FileHandle.standardInput.readDataToEndOfFile()
    Task { @MainActor in
        do {
            guard let input = try JSONSerialization.jsonObject(with: data) as? [String: Any],
                  let origin = input["origin"] as? String, let url = URL(string: origin),
                  url.scheme == "http", url.host == "127.0.0.1", url.user == nil,
                  let token = input["token"] as? String,
                  let folder = input["folder"] as? String,
                  let files = input["files"] as? [String], !files.isEmpty else {
                throw AppFailure(message: "Invalid disposable integration fixture")
            }
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent("sharefile-native-smoke-" + UUID().uuidString)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            defer { try? FileManager.default.removeItem(at: directory) }
            let state = directory.appendingPathComponent("state.json")
            let historySecrets = TestHistorySecrets()
            @MainActor func fixtureHistory() -> LinkHistory {
                LinkHistory(path: directory.appendingPathComponent("history.json"), read: { historySecrets.read($0) },
                            write: { historySecrets.write($0, $1) }, delete: { historySecrets.delete($0) })
            }
            var clipboardWrites: [String] = []
            var completions: [UploadCompletion] = []
            let model = Model(stateURL: state, history: fixtureHistory(), writeClipboard: { clipboardWrites.append($0); return true })
            model.onUploadCompletion = { completions.append($0) }
            model.settings.origin = origin; model.token = token
            guard let session = try await model.request("session") as? [String: Any], let user = session["user"] as? [String: Any], let owner = user["id"] as? String else { throw AppFailure(message: "Session validation failed") }
            model.settings.owner = owner; model.settings.folder = folder
            model.settings.username = user["username"] as? String ?? ""
            model.add(files.map { URL(fileURLWithPath: $0) })
            let deadline = Date().addingTimeInterval(60)
            while Date() < deadline {
                if model.settings.uploads.count == files.count && model.settings.uploads.allSatisfy({ $0.status == "done" && !$0.link.isEmpty }) {
                    let saved = try String(contentsOf: state, encoding: .utf8)
                    guard !saved.contains(token), model.settings.uploads.allSatisfy({ !saved.contains($0.link) }) else { throw AppFailure(message: "Checkpoint leaked credentials") }
                    let initialLinks = model.settings.uploads.map(\.link)
                    let initialFiles = model.settings.uploads.map(\.file_id)
                    guard clipboardWrites == [initialLinks.joined(separator: "\n")], completions == [UploadCompletion(names: model.settings.uploads.map(\.name), copied: true)] else { throw AppFailure(message: "Batch must copy all links once before its completion notification") }
                    guard !completions[0].body.contains(token), !initialLinks.contains(where: { completions[0].body.contains($0) }) else { throw AppFailure(message: "Notification leaked a capability") }
                    model.setSilentFinderUploads(false); model.setNotifyUploads(false)
                    let reloaded = Model(stateURL: state, history: fixtureHistory(), writeClipboard: { _ in false })
                    guard reloaded.sentLinks.count == files.count else { throw AppFailure(message: "Native history did not survive restart") }
                    for entry in reloaded.sentLinks {
                        guard initialLinks.contains(try await reloaded.history.link(entry).absoluteString) else { throw AppFailure(message: "Saved history URL differs from created capability") }
                    }
                    guard !reloaded.settings.silentFinderUploads, !reloaded.settings.notifyUploads, reloaded.settings.owner == owner, reloaded.settings.uploads.count == files.count else { throw AppFailure(message: "Saved Finder options changed connection or queue") }
                    model.add([URL(fileURLWithPath: files.last!)])
                    let silentDeadline = Date().addingTimeInterval(15)
                    while (model.settings.uploads.count != files.count + 1 || model.settings.uploads.last?.status != "done") && Date() < silentDeadline { try await Task.sleep(nanoseconds: 100_000_000) }
                    guard model.settings.uploads.last?.status == "done", clipboardWrites.count == 2, completions.count == 1 else { throw AppFailure(message: "Disabling notifications must preserve upload and clipboard behavior") }
                    let failedClipboard = Model(stateURL: directory.appendingPathComponent("failed-clipboard.json"), history: fixtureHistory(), writeClipboard: { _ in false })
                    failedClipboard.settings = model.settings; failedClipboard.settings.uploads = []; failedClipboard.settings.notifyUploads = true; failedClipboard.token = token
                    var failedCompletion: UploadCompletion?
                    failedClipboard.onUploadCompletion = { failedCompletion = $0 }
                    failedClipboard.add([URL(fileURLWithPath: files.last!)])
                    let clipboardDeadline = Date().addingTimeInterval(15)
                    while failedCompletion == nil && Date() < clipboardDeadline { try await Task.sleep(nanoseconds: 100_000_000) }
                    guard failedCompletion?.copied == false, failedClipboard.settings.uploads.last?.status == "done", failedCompletion?.body.contains("Could not copy") == true else { throw AppFailure(message: "Clipboard failure must not claim that the link was copied") }
                    model.add([directory.appendingPathComponent("missing-test-source.txt")])
                    let rejectionDeadline = Date().addingTimeInterval(5)
                    while model.fileErrors.isEmpty && Date() < rejectionDeadline { try await Task.sleep(nanoseconds: 100_000_000) }
                    guard !model.fileErrors.isEmpty else { throw AppFailure(message: "Missing file was not reported") }
                    let imagePasteboard = NSPasteboard(name: NSPasteboard.Name("ShareFile-smoke-image-" + UUID().uuidString))
                    defer { imagePasteboard.releaseGlobally() }
                    let png = Data(base64Encoded: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")!
                    imagePasteboard.setData(png, forType: .png)
                    model.setNotifyUploads(true)
                    let count = model.settings.uploads.count
                    model.uploadClipboard(imagePasteboard)
                    let imageDeadline = Date().addingTimeInterval(15)
                    while (model.settings.uploads.count == count || model.settings.uploads.last?.status != "done") && Date() < imageDeadline {
                        try await Task.sleep(nanoseconds: 100_000_000)
                    }
                    guard let screenshot = model.settings.uploads.last, screenshot.status == "done", screenshot.name.hasSuffix(".png"),
                          !FileManager.default.fileExists(atPath: screenshot.source), clipboardWrites.last == screenshot.link,
                          completions.last?.copied == true, completions.last?.names == [screenshot.name] else {
                        throw AppFailure(message: "Clipboard upload, copy, notification or cleanup failed")
                    }
                    let (downloaded, response) = try await URLSession.shared.data(from: URL(string: screenshot.link)!)
                    guard (response as? HTTPURLResponse)?.statusCode == 200, NSBitmapImageRep(data: downloaded)?.pixelsWide == 1 else {
                        throw AppFailure(message: "Clipboard screenshot download differs from image")
                    }
                    let result: [String: Any] = ["links": initialLinks, "files": initialFiles]
                    let encoded = try JSONSerialization.data(withJSONObject: result)
                    try FileManager.default.removeItem(at: directory)
                    FileHandle.standardOutput.write(encoded)
                    exit(0)
                }
                if model.settings.uploads.contains(where: { ["error", "uncertain"].contains($0.status) }) { throw AppFailure(message: "Native queue failed: " + (model.settings.uploads.first(where: { !$0.message.isEmpty })?.message ?? "unknown")) }
                try await Task.sleep(nanoseconds: 100_000_000)
            }
            throw AppFailure(message: "Native queue timed out: " + model.notice)
        } catch { fputs("FAIL: \(error.localizedDescription)\n", stderr); exit(1) }
    }
    dispatchMain()
}
