import AppKit
import SwiftUI

@MainActor final class AppDelegate: NSObject, NSApplicationDelegate {
    var window: NSWindow!
    var statusItem: NSStatusItem!
    var model: Model!
    private var initialFiles: [URL] = []

    func applicationDidFinishLaunching(_ notification: Notification) {
        let directory: URL
        if let index = CommandLine.arguments.firstIndex(of: "--test-state-dir"), CommandLine.arguments.count > index + 1 {
            directory = URL(fileURLWithPath: CommandLine.arguments[index + 1], isDirectory: true)
        } else {
            directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("ShareFile", isDirectory: true)
        }
        model = Model(stateURL: directory.appendingPathComponent("state.json"))
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
        menu.addItem(withTitle: "Загрузить и поделиться…", action: #selector(pickFiles), keyEquivalent: "")
        menu.addItem(withTitle: "Приостановить загрузки", action: #selector(pauseAll), keyEquivalent: "")
        menu.addItem(.separator())
        menu.addItem(withTitle: "Завершить", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "")
        for item in menu.items { item.target = self }
        menu.items.last?.target = NSApp
        statusItem.menu = menu
        NSApp.servicesProvider = self
        NSUpdateDynamicServices()
        showWindow()
        if !initialFiles.isEmpty { model.add(initialFiles); initialFiles = [] }
        Task { await model.restoreConnection() }
    }
    @objc func showWindow() { window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true) }
    @objc func settings() { model.page = "settings"; showWindow() }
    @objc func pickFiles() { showWindow(); model.chooseFiles() }
    @objc func pauseAll() { model.pauseAll() }
    @objc func about() {
        NSApp.orderFrontStandardAboutPanel(options: [.applicationName: "ShareFile", .applicationVersion: "0.1.0", .credits: NSAttributedString(string: "Личное хранилище на Keenetic\nmacOS 13+ · Apple Silicon / Intel")])
    }
    // The service returns as soon as files are queued; Finder never waits for network transfers.
    @objc func uploadAndShare(_ pasteboard: NSPasteboard, userData: String?, error: AutoreleasingUnsafeMutablePointer<NSString?>) {
        let files = Self.fileURLs(pasteboard)
        guard !files.isEmpty else { error.pointee = "Не удалось получить выбранные файлы"; return }
        showWindow(); model.add(files)
    }
    nonisolated static func fileURLs(_ pasteboard: NSPasteboard) -> [URL] {
        if let urls = pasteboard.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL], !urls.isEmpty { return urls }
        let legacy = NSPasteboard.PasteboardType("NSFilenamesPboardType")
        return (pasteboard.propertyList(forType: legacy) as? [String] ?? []).map { URL(fileURLWithPath: $0) }
    }
    func application(_ sender: NSApplication, openFiles filenames: [String]) {
        let urls = filenames.map { URL(fileURLWithPath: $0) }
        if model == nil { initialFiles.append(contentsOf: urls) }
        else { showWindow(); model.add(urls) }
        sender.reply(toOpenOrPrint: .success)
    }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool { showWindow(); return true }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply { model.quit(); return .terminateNow }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
}

if CommandLine.arguments.contains("--self-test") {
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
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent("sharefile-native-test-" + UUID().uuidString)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            defer { try? FileManager.default.removeItem(at: directory) }
            let state = directory.appendingPathComponent("state.json")
            var interrupted = sample; interrupted.status = "uploading"; interrupted.size = 5_368_709_123
            var sharing = sample; sharing.id = "share-test"; sharing.status = "sharing"
            let preferences = Preferences(uploads: [interrupted, sharing])
            try JSONEncoder().encode(preferences).write(to: state)
            let model = Model(stateURL: state)
            guard model.settings.uploads[0].status == "paused", model.settings.uploads[1].status == "uncertain", model.settings.uploads[0].size == 5_368_709_123 else { throw AppFailure(message: "Unsafe queue recovery") }
            model.save()
        }
        pasteboard.releaseGlobally()
        print("PASS: Finder Unicode/multiple file URLs, Keychain read/write/delete, secret-free checkpoints, safe queue recovery, 64-bit sizes")
        exit(0)
    } catch { fputs("FAIL: \(error.localizedDescription)\n", stderr); exit(1) }
}
if CommandLine.arguments.contains("--integration-test") { integrationSmokeTest() }
MainActor.assumeIsolated {
    let app = NSApplication.shared
    app.setActivationPolicy(.regular)
    let delegate = AppDelegate()
    app.delegate = delegate
    app.run()
}
