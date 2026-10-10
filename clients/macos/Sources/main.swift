import AppKit
import SwiftUI

@MainActor final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    var window: NSWindow!
    var statusItem: NSStatusItem!
    var model: Model!
    private var pendingFinderFiles: [URL] = []
    private var connectionRestored = false
    private var notifications: UploadNotifications!
    private var clipboardHotKey: ClipboardHotKey?

    func applicationWillFinishLaunching(_ notification: Notification) {
        prepareModel()
        notifications = UploadNotifications()
        notifications.onOpen = { [weak self] in self?.model.page = "files"; self?.showWindow() }
        notifications.onNotice = { [weak self] in self?.model.notice = $0 }
        notifications.isEnabled = { [weak self] in self?.model.settings.notifyUploads == true }
        model.onUploadCompletion = { [weak self] completion in
            Task { @MainActor [weak self] in await self?.notifications.send(completion) }
        }
        model.onClipboardFailure = { [weak self] message in
            Task { @MainActor [weak self] in await self?.notifications.sendFailure(message) }
        }
        NSApp.servicesProvider = self
    }
    private func prepareModel() {
        guard model == nil else { return }
        let directory: URL
        if let index = CommandLine.arguments.firstIndex(of: "--test-state-dir"), CommandLine.arguments.count > index + 1 {
            directory = URL(fileURLWithPath: CommandLine.arguments[index + 1], isDirectory: true)
        } else {
            directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("ShareFile", isDirectory: true)
        }
        model = Model(stateURL: directory.appendingPathComponent("state.json"))
    }
    func applicationDidFinishLaunching(_ notification: Notification) {
        prepareModel()
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 980, height: 700), styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = "ShareFile"
        window.titlebarAppearsTransparent = true
        window.contentView = NSHostingView(rootView: RootView(model: model))
        window.isReleasedWhenClosed = false
        window.center()
        let main = NSMenu()
        let appItem = NSMenuItem(); main.addItem(appItem)
        let appMenu = NSMenu(); appItem.submenu = appMenu
        appMenu.addItem(withTitle: "О ShareFile", action: #selector(about), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "История ссылок…", action: #selector(showHistory), keyEquivalent: "y")
        appMenu.addItem(withTitle: "Настройки…", action: #selector(settings), keyEquivalent: ",")
        let servicesItem = NSMenuItem(title: "Службы", action: nil, keyEquivalent: "")
        let services = NSMenu(); servicesItem.submenu = services; appMenu.addItem(servicesItem); NSApp.servicesMenu = services
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Завершить ShareFile", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        let editItem = NSMenuItem(); main.addItem(editItem)
        let edit = NSMenu(title: "Правка"); editItem.submenu = edit
        edit.addItem(withTitle: "Вырезать", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        edit.addItem(withTitle: "Копировать", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        edit.addItem(withTitle: "Вставить", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        edit.addItem(withTitle: "Выбрать всё", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        NSApp.mainMenu = main
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        statusItem.button?.image = NSImage(systemSymbolName: "arrow.up.doc", accessibilityDescription: "ShareFile")
        let menu = NSMenu()
        menu.addItem(withTitle: "Открыть ShareFile", action: #selector(showWindow), keyEquivalent: "")
        menu.addItem(withTitle: "История ссылок…", action: #selector(showHistory), keyEquivalent: "")
        let recent = NSMenuItem(title: "Недавние ссылки", action: nil, keyEquivalent: "")
        let recentMenu = NSMenu(); recentMenu.delegate = self; recent.submenu = recentMenu; menu.addItem(recent)
        menu.addItem(withTitle: "Настройки…", action: #selector(settings), keyEquivalent: "")
        menu.addItem(withTitle: "Загрузить и поделиться…", action: #selector(pickFiles), keyEquivalent: "")
        menu.addItem(withTitle: "Из буфера обмена (⌘⇧U)", action: #selector(uploadClipboard), keyEquivalent: "")
        menu.addItem(withTitle: "Приостановить загрузки", action: #selector(pauseAll), keyEquivalent: "")
        menu.addItem(.separator())
        menu.addItem(withTitle: "Завершить", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "")
        for item in menu.items { item.target = self }
        menu.items.last?.target = NSApp
        statusItem.menu = menu
        do { clipboardHotKey = try ClipboardHotKey { [weak self] in self?.uploadClipboard() } }
        catch { model.notice = error.localizedDescription }
        NSApp.servicesProvider = self
        NSUpdateDynamicServices()
        if (notification.userInfo?[NSApplication.launchIsDefaultUserInfoKey] as? Bool ?? true) && pendingFinderFiles.isEmpty { showWindow() }
        Task {
            await model.restoreConnection()
            finishRestoringConnection()
        }
    }
    private func finishRestoringConnection() {
        connectionRestored = true
        let files = pendingFinderFiles; pendingFinderFiles = []
        if !files.isEmpty { receiveFinderFiles(files) }
    }
    @objc func showWindow() {
        guard let window else { return }
        NSApp.setActivationPolicy(.regular)
        window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
    }
    @objc func showHistory() { model.page = "history"; showWindow() }
    func menuNeedsUpdate(_ menu: NSMenu) {
        menu.removeAllItems()
        for entry in model.sentLinks.prefix(8) {
            let item = NSMenuItem(title: entry.name, action: nil, keyEquivalent: "")
            let submenu = NSMenu(); submenu.autoenablesItems = false
            let status = NSMenuItem(title: entry.label, action: nil, keyEquivalent: ""); status.isEnabled = false; submenu.addItem(status)
            for (title, action) in [("Копировать ссылку", #selector(copyRecent(_:))), ("Открыть ссылку", #selector(openRecent(_:))), ("Отозвать ссылку", #selector(revokeRecent(_:)))] {
                let command = NSMenuItem(title: title, action: action, keyEquivalent: "")
                command.target = self; command.representedObject = entry
                command.isEnabled = action == #selector(revokeRecent(_:)) ? model.canRevokeHistory(entry) : entry.active
                submenu.addItem(command)
            }
            item.submenu = submenu; menu.addItem(item)
        }
        if menu.items.isEmpty { let empty = NSMenuItem(title: "История пока пуста", action: nil, keyEquivalent: ""); empty.isEnabled = false; menu.addItem(empty) }
        menu.addItem(.separator())
        let all = NSMenuItem(title: "Вся история…", action: #selector(showHistory), keyEquivalent: ""); all.target = self; menu.addItem(all)
    }
    @objc func copyRecent(_ sender: NSMenuItem) { if let entry = sender.representedObject as? SentLink { model.copyHistory(entry) } }
    @objc func openRecent(_ sender: NSMenuItem) { if let entry = sender.representedObject as? SentLink { model.openHistory(entry) } }
    @objc func revokeRecent(_ sender: NSMenuItem) { if let entry = sender.representedObject as? SentLink { model.revokeHistory(entry) } }
    @objc func settings() { model.page = "settings"; showWindow() }
    @objc func pickFiles() { showWindow(); model.chooseFiles() }
    @objc func uploadClipboard() {
        model.uploadClipboard()
        if !model.connected { showWindow() }
        if !model.notice.isEmpty && model.activeCount == 0 { NSSound.beep() }
    }
    @objc func pauseAll() { model.pauseAll() }
    @objc func about() {
        NSApp.orderFrontStandardAboutPanel(options: [.applicationName: "ShareFile", .applicationVersion: "0.1.0", .credits: NSAttributedString(string: "Личное хранилище на Keenetic\nmacOS 13+ · Apple Silicon / Intel")])
    }
    // The service returns as soon as files are queued; Finder never waits for network transfers.
    @objc func uploadAndShare(_ pasteboard: NSPasteboard, userData: String?, error: AutoreleasingUnsafeMutablePointer<NSString?>) {
        let files = Self.fileURLs(pasteboard)
        guard !files.isEmpty else { error.pointee = "Не удалось получить выбранные файлы"; return }
        receiveFinderFiles(files)
    }
    nonisolated static func fileURLs(_ pasteboard: NSPasteboard) -> [URL] {
        if let urls = pasteboard.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL], !urls.isEmpty { return urls }
        let legacy = NSPasteboard.PasteboardType("NSFilenamesPboardType")
        return (pasteboard.propertyList(forType: legacy) as? [String] ?? []).map { URL(fileURLWithPath: $0) }
    }
    func application(_ sender: NSApplication, openFiles filenames: [String]) {
        receiveFinderFiles(filenames.map { URL(fileURLWithPath: $0) })
        sender.reply(toOpenOrPrint: .success)
    }
    private func receiveFinderFiles(_ files: [URL]) {
        guard connectionRestored else { pendingFinderFiles.append(contentsOf: files); return }
        if !model.settings.silentFinderUploads || !model.connected || model.settings.folder.isEmpty {
            if !model.connected { model.page = "settings" }
            showWindow()
        }
        model.add(files)
    }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool { showWindow(); return true }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply { clipboardHotKey?.stop(); model?.quit(); return .terminateNow }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    static func verifySilentFinderLaunch(model: Model, pasteboard: NSPasteboard) throws {
        NSApplication.shared.setActivationPolicy(.accessory)
        let delegate = AppDelegate(); delegate.model = model
        delegate.applicationDidFinishLaunching(Notification(name: NSApplication.didFinishLaunchingNotification, userInfo: [NSApplication.launchIsDefaultUserInfoKey: false]))
        defer {
            delegate.clipboardHotKey?.stop()
            NSStatusBar.system.removeStatusItem(delegate.statusItem)
            delegate.window.close()
            NSApp.servicesProvider = nil
        }
        var error: NSString?
        delegate.uploadAndShare(pasteboard, userData: nil, error: &error)
        guard error == nil, !delegate.window.isVisible, !delegate.pendingFinderFiles.isEmpty else { throw AppFailure(message: "Finder cold launch opened a window or lost files during connection restore") }
        model.settings.owner = "self-test-owner"; model.settings.folder = "self-test-folder"; model.token = "disposable-test-token"
        delegate.finishRestoringConnection()
        delegate.uploadAndShare(pasteboard, userData: nil, error: &error)
        guard error == nil, !delegate.window.isVisible, NSApp.activationPolicy() == .accessory else { throw AppFailure(message: "Finder upload activated the existing background app") }
    }
}

if CommandLine.arguments.contains("--progress-test") {
    do { try verifyTransferProgress(); print("PASS: transfer rate, stalled/resumed estimates, byte-weighted batches, paused ETA and empty files"); exit(0) }
    catch { fputs("FAIL: \(error.localizedDescription)\n", stderr); exit(1) }
}
if CommandLine.arguments.contains("--self-test") {
    do { try verifyTransferProgress() }
    catch { fputs("FAIL: \(error.localizedDescription)\n", stderr); exit(1) }
    do {
        let pasteboard = NSPasteboard(name: NSPasteboard.Name("ShareFile-self-test-" + UUID().uuidString))
        let expected = [URL(fileURLWithPath: "/tmp/Отчёт с пробелами.pdf"), URL(fileURLWithPath: "/tmp/日本語.txt")]
        pasteboard.writeObjects(expected as [NSURL])
        guard AppDelegate.fileURLs(pasteboard) == expected else { throw AppFailure(message: "Finder pasteboard round trip failed") }
        let account = "self-test:" + UUID().uuidString
        defer { try? CredentialStore.delete(account); pasteboard.releaseGlobally() }
        try CredentialStore.write(account, value: "disposable-self-test-token")
        guard try CredentialStore.read(account) == "disposable-self-test-token" else { throw AppFailure(message: "Keychain round trip failed") }
        try CredentialStore.delete(account)
        guard try CredentialStore.read(account) == nil else { throw AppFailure(message: "Keychain cleanup failed") }
        let sample = Upload(id: "test", source: "/tmp/test", name: "test", size: 5, modified: 1, fingerprint: "hash", folder: "f", upload: "", file_id: "", offset: 0, link: "https://secret.example/s/private-token")
        let encoded = try JSONEncoder().encode(sample)
        guard !String(decoding: encoded, as: UTF8.self).contains("private-token") else { throw AppFailure(message: "Public link leaked into checkpoint") }
        try MainActor.assumeIsolated {
            try verifyClipboardImages()
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent("sharefile-native-test-" + UUID().uuidString)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            defer { try? FileManager.default.removeItem(at: directory) }
            let state = directory.appendingPathComponent("state.json")
            var interrupted = sample; interrupted.status = "uploading"; interrupted.size = 5_368_709_123
            var sharing = sample; sharing.id = "share-test"; sharing.status = "sharing"
            let preferences = Preferences(uploads: [interrupted, sharing])
            var legacy = try JSONSerialization.jsonObject(with: JSONEncoder().encode(preferences)) as! [String: Any]
            legacy["origin"] = "https://existing.example"; legacy["owner"] = "existing-owner"; legacy["folder"] = "existing-folder"
            legacy.removeValue(forKey: "silentFinderUploads"); legacy.removeValue(forKey: "notifyUploads")
            try JSONSerialization.data(withJSONObject: legacy).write(to: state)
            let model = Model(stateURL: state)
            guard model.settings.uploads[0].status == "paused", model.settings.uploads[1].status == "uncertain", model.settings.uploads[0].size == 5_368_709_123 else { throw AppFailure(message: "Unsafe queue recovery") }
            guard model.settings.origin == "https://existing.example", model.settings.owner == "existing-owner", model.settings.folder == "existing-folder", model.settings.silentFinderUploads, model.settings.notifyUploads else { throw AppFailure(message: "New Finder preferences broke legacy settings") }
            model.setSilentFinderUploads(false); model.setNotifyUploads(false)
            let reloaded = Model(stateURL: state)
            guard !reloaded.settings.silentFinderUploads, !reloaded.settings.notifyUploads, reloaded.settings.uploads.count == 2 else { throw AppFailure(message: "Finder preferences were not persisted") }
            let backgroundModel = Model(stateURL: directory.appendingPathComponent("background.json"))
            try AppDelegate.verifySilentFinderLaunch(model: backgroundModel, pasteboard: pasteboard)
            model.save()
        }
        pasteboard.releaseGlobally()
        Task { @MainActor in
            do { try await verifyLinkHistory() }
            catch { fputs("FAIL: \(error.localizedDescription)\n", stderr); exit(1) }
        print("PASS: secure link history restart, retention, scope, expiry, revocation and corruption; silent Finder cold/warm launch, legacy preferences/defaults/persistence, Unicode file URLs, Keychain, secret-free checkpoints, safe queue recovery, 64-bit sizes")
        exit(0)
        }
        dispatchMain()
    } catch { fputs("FAIL: \(error.localizedDescription)\n", stderr); exit(1) }
}
if CommandLine.arguments.contains("--integration-test") { integrationSmokeTest() }
MainActor.assumeIsolated {
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    let delegate = AppDelegate()
    app.delegate = delegate
    app.run()
}
