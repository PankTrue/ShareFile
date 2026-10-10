import UserNotifications

@MainActor final class UploadNotifications: NSObject, UNUserNotificationCenterDelegate {
    private let center = UNUserNotificationCenter.current()
    var onOpen: (() -> Void)?
    var onNotice: ((String) -> Void)?
    var isEnabled: () -> Bool = { true }

    override init() { super.init(); center.delegate = self }

    func requestPermission() async -> Bool {
        do {
            let allowed = try await center.requestAuthorization(options: [.alert])
            if !allowed { onNotice?("Загрузка и копирование работают. Для уведомлений разрешите ShareFile в System Settings → Notifications") }
            return allowed
        } catch {
            onNotice?("Не удалось включить уведомления: " + error.localizedDescription)
            return false
        }
    }

    func send(_ completion: UploadCompletion) async {
        await send(title: completion.title, body: completion.body)
    }
    func sendFailure(_ message: String) async {
        await send(title: "Не удалось загрузить изображение", body: message)
    }
    private func send(title: String, body: String) async {
        guard isEnabled(), await requestPermission(), isEnabled() else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        // Public links and device tokens never enter Notification Center's persisted payload.
        let request = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        do { try await center.add(request) }
        catch { onNotice?("Файлы загружены, но уведомление не доставлено: " + error.localizedDescription) }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list])
    }
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler completionHandler: @escaping () -> Void) {
        if response.actionIdentifier == UNNotificationDefaultActionIdentifier { Task { @MainActor in self.onOpen?() } }
        completionHandler()
    }
}
