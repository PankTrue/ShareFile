import AppKit
import Carbon

// A private, durable source lets the existing upload queue resume after a restart.
struct ClipboardImages {
    let directory: URL
    static let maxBytes = 32 * 1024 * 1024
    static let maxPixels = 25_000_000

    func capture(_ pasteboard: NSPasteboard = .general) throws -> URL {
        guard let data = pasteboard.data(forType: .png) ?? pasteboard.data(forType: .tiff) else {
            throw AppFailure(message: "В буфере обмена нет изображения. Скопируйте скриншот и повторите")
        }
        guard data.count <= Self.maxBytes, let image = NSBitmapImageRep(data: data),
              image.pixelsWide > 0, image.pixelsHigh > 0,
              image.pixelsWide <= Self.maxPixels / image.pixelsHigh,
              let png = image.representation(using: .png, properties: [:]), png.count <= Self.maxBytes else {
            throw AppFailure(message: "Изображение повреждено или слишком большое (максимум 25 Мп и 32 МиБ)")
        }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let values = try directory.resourceValues(forKeys: [.isSymbolicLinkKey])
        guard values.isSymbolicLink != true else { throw AppFailure(message: "Не удалось открыть каталог изображений") }
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
        let formatter = DateFormatter(); formatter.dateFormat = "yyyyMMdd-HHmmss"
        let file = directory.appendingPathComponent("Screenshot-" + formatter.string(from: Date()) + "-" + UUID().uuidString + ".png")
        try png.write(to: file, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
        return file
    }

    func remove(_ path: String) {
        let file = URL(fileURLWithPath: path)
        guard (try? directory.resourceValues(forKeys: [.isSymbolicLinkKey]).isSymbolicLink) == false,
              file.deletingLastPathComponent().standardizedFileURL.path == directory.standardizedFileURL.path,
              file.lastPathComponent.hasPrefix("Screenshot-"), file.pathExtension == "png" else { return }
        try? FileManager.default.removeItem(at: file)
    }
    func reconcile(_ uploads: [Upload], pending: Set<String> = []) {
        guard (try? directory.resourceValues(forKeys: [.isSymbolicLinkKey]).isSymbolicLink) == false else { return }
        let sources = uploads.filter { !["done", "uploaded", "uncertain"].contains($0.status) }.map(\.source) + Array(pending)
        let keep = Set(sources.map { URL(fileURLWithPath: $0).standardizedFileURL.path })
        guard let files = try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil) else { return }
        for file in files where !keep.contains(file.standardizedFileURL.path) { remove(file.path) }
    }
}

@MainActor final class ClipboardHotKey {
    private var hotKey: EventHotKeyRef?
    private var handler: EventHandlerRef?
    private let action: () -> Void
    init(action: @escaping () -> Void) throws {
        self.action = action
        var event = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let context = Unmanaged.passUnretained(self).toOpaque()
        let installed = InstallEventHandler(GetApplicationEventTarget(), { _, _, context in
            guard let context else { return OSStatus(eventNotHandledErr) }
            MainActor.assumeIsolated { Unmanaged<ClipboardHotKey>.fromOpaque(context).takeUnretainedValue().action() }
            return noErr
        }, 1, &event, context, &handler)
        guard installed == noErr else { throw AppFailure(message: "Не удалось включить горячую клавишу") }
        let status = RegisterEventHotKey(UInt32(kVK_ANSI_U), UInt32(cmdKey | shiftKey), EventHotKeyID(signature: 0x5346434C, id: 1), GetApplicationEventTarget(), 0, &hotKey)
        guard status == noErr else {
            stop()
            throw AppFailure(message: "⌘⇧U занято другим приложением. Используйте «Из буфера обмена» в меню ShareFile")
        }
    }
    func stop() {
        if let hotKey { UnregisterEventHotKey(hotKey) }; hotKey = nil
        if let handler { RemoveEventHandler(handler) }; handler = nil
    }
}
