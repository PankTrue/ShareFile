import SwiftUI
import AppKit

private let ink = Color(red: 0.12, green: 0.19, blue: 0.23)
private let green = Color(red: 0.15, green: 0.39, blue: 0.32)
private let paper = Color(red: 0.965, green: 0.965, blue: 0.95)

private struct SidebarButtonStyle: ButtonStyle {
    let selected: Bool
    let focused: Bool

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 13, weight: .medium))
            .foregroundStyle(selected ? green : ink)
            .padding(12)
            .background(green.opacity(configuration.isPressed ? 0.16 : selected ? 0.10 : 0))
            .clipShape(RoundedRectangle(cornerRadius: 8))
            .overlay {
                RoundedRectangle(cornerRadius: 8)
                    .strokeBorder(focused ? green.opacity(0.55) : Color.clear, lineWidth: 2)
            }
            .contentShape(RoundedRectangle(cornerRadius: 8))
    }
}

struct RootView: View {
    @ObservedObject var model: Model
    @State private var hovering = false
    @FocusState private var focusedPage: String?
    var body: some View {
        HStack(spacing: 0) {
            sidebar
            VStack(alignment: .leading, spacing: 0) {
                HStack {
                    Text(model.page == "files" ? L("Загрузки") : model.page == "history" ? L("История ссылок") : L("Настройки")).font(.system(size: 27, weight: .semibold))
                    Spacer()
                    HStack(spacing: 6) {
                        Circle().fill(model.connected ? green : Color.secondary).frame(width: 7, height: 7)
                        Text(model.connected ? model.settings.username : L("Не подключено")).font(.system(size: 12))
                    }.padding(9).background(Color.white.opacity(0.7)).clipShape(Capsule())
                }.padding(.horizontal, 32).padding(.top, 30).padding(.bottom, 24)
                ScrollView {
                    VStack(alignment: .leading, spacing: 22) {
                        if model.page == "files" { uploads } else if model.page == "history" { LinkHistoryView(model: model, history: model.history) } else { settings }
                    }.padding(.horizontal, 32).padding(.bottom, 24)
                }
                if !model.notice.isEmpty {
                    HStack(alignment: .top, spacing: 8) {
                        Image(systemName: "info.circle")
                        Text(model.notice).font(.system(size: 12)).textSelection(.enabled)
                        Spacer()
                        Button { model.notice = "" } label: { Image(systemName: "xmark") }.buttonStyle(.plain).accessibilityLabel(L("Закрыть уведомление"))
                    }.padding(15).background(Color.white.opacity(0.85))
                }
            }.frame(maxWidth: .infinity, maxHeight: .infinity).background(paper)
        }.foregroundStyle(ink).tint(green).preferredColorScheme(.light).environment(\.locale, Localization.locale)
            .frame(minWidth: 850, minHeight: 600)
            .sheet(isPresented: $model.showFolders) { folderPicker }
            .onDrop(of: [.fileURL], isTargeted: $hovering) { providers in
                Task { @MainActor in
                    var files: [URL] = []
                    for provider in providers {
                        let url: URL? = await withCheckedContinuation { continuation in
                            _ = provider.loadObject(ofClass: URL.self) { url, _ in continuation.resume(returning: url) }
                        }
                        if let url, url.isFileURL { files.append(url) }
                    }
                    model.add(files)
                }
                return !providers.isEmpty
            }
            .overlay { if hovering { RoundedRectangle(cornerRadius: 8).stroke(green, lineWidth: 3).allowsHitTesting(false) } }
    }
    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 28) {
            HStack(spacing: 10) {
                Image(systemName: "arrow.up.doc.fill").font(.system(size: 24)).foregroundStyle(green)
                Text("ShareFile").font(.system(size: 19, weight: .bold))
            }.padding(.top, 34)
            VStack(spacing: 6) {
                nav(L("Загрузки"), icon: "arrow.up.circle", page: "files")
                nav(L("История"), icon: "clock.arrow.circlepath", page: "history")
                nav(L("Настройки"), icon: "slider.horizontal.3", page: "settings")
            }
            Spacer()
            VStack(alignment: .leading, spacing: 9) {
                Text(L("ВАШЕ ХРАНИЛИЩЕ")).font(.system(size: 9, weight: .semibold)).tracking(1.3).foregroundStyle(.secondary)
                Text(model.settings.origin.isEmpty ? L("Подключите Keenetic") : (URL(string: model.settings.origin)?.host ?? model.settings.origin)).font(.system(size: 12, weight: .medium)).lineLimit(2).textSelection(.enabled)
                Button { model.openWeb() } label: { Label(L("Открыть Web UI"), systemImage: "arrow.up.right") }.buttonStyle(.plain).font(.system(size: 12)).foregroundStyle(green).disabled(!model.connected)
            }.padding(.bottom, 25)
        }.padding(.horizontal, 20).frame(width: 185).frame(maxHeight: .infinity).background(Color.white)
    }
    @ViewBuilder private func nav(_ title: String, icon: String, page: String) -> some View {
        let button = Button { focusedPage = page; model.page = page } label: {
            HStack { Image(systemName: icon).frame(width: 18); Text(title).lineLimit(1); Spacer() }
        }.buttonStyle(SidebarButtonStyle(selected: model.page == page, focused: focusedPage == page))
            .focused($focusedPage, equals: page)
        if #available(macOS 14.0, *) {
            button.focusEffectDisabled()
        } else {
            button
        }
    }
    private var uploads: some View {
        VStack(alignment: .leading, spacing: 24) {
            VStack(alignment: .leading, spacing: 18) {
                HStack(alignment: .top) {
                    VStack(alignment: .leading, spacing: 8) {
                        Text(L("Файлы и папки. Ссылка для всех.")).font(.system(size: 20, weight: .semibold))
                        Text(L("Выберите файлы и папки или перетащите их сюда.\nВложенность сохранится, на каждую папку — одна ссылка.")).font(.system(size: 13)).foregroundStyle(.secondary).lineSpacing(4)
                    }
                    Spacer()
                    Image(systemName: "externaldrive.badge.wifi").font(.system(size: 45, weight: .light)).foregroundStyle(green.opacity(0.7)).padding(4)
                }
                HStack {
                    Button { model.chooseFiles() } label: { Label(L("Загрузить и поделиться"), systemImage: "plus") }.buttonStyle(.borderedProminent).controlSize(.large).keyboardShortcut("o")
                    Button { model.uploadClipboard() } label: { Label(L("Из буфера"), systemImage: "clipboard") }.help(L("Скриншот → ⌘⇧U → ссылка в буфере обмена"))
                    Spacer()
                    VStack(alignment: .trailing, spacing: 4) {
                        Text(L("В папку «{0}»", String(model.settings.folderName))).font(.system(size: 12, weight: .medium))
                        Text(model.settings.never ? L("Ссылка без срока") : L("Ссылка на {0} дн.", String(model.settings.days))).font(.system(size: 11)).foregroundStyle(.secondary)
                    }
                }
            }.padding(24).background(Color.white).clipShape(RoundedRectangle(cornerRadius: 12))
            if !model.fileErrors.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    HStack { Text(L("Не удалось добавить файлы или папки")).font(.system(size: 13, weight: .semibold)); Spacer(); Button(L("Убрать")) { model.fileErrors = [] }.buttonStyle(.plain) }
                    ForEach(Array(model.fileErrors.enumerated()), id: \.offset) { _, error in Text(error).font(.system(size: 12)).textSelection(.enabled) }
                }.padding(16).background(Color.orange.opacity(0.08)).clipShape(RoundedRectangle(cornerRadius: 10))
            }
            HStack {
                Text(L("Очередь")).font(.system(size: 15, weight: .semibold))
                Text("\(model.settings.uploads.count)").font(.system(size: 11)).padding(.horizontal, 7).padding(.vertical, 3).background(ink.opacity(0.06)).clipShape(Capsule())
                Spacer()
                if model.activeCount > 0 { Button(L("Приостановить всё")) { model.pauseAll() }.buttonStyle(.plain).font(.system(size: 12)).foregroundStyle(green) }
                if model.settings.uploads.contains(where: { $0.status == "done" }) { Button(L("Убрать завершённые")) { model.clearFinished() }.buttonStyle(.plain).font(.system(size: 12)).foregroundStyle(.secondary) }
            }
            if model.settings.uploads.isEmpty {
                VStack(spacing: 12) {
                    Image(systemName: "tray").font(.system(size: 31, weight: .light)).foregroundStyle(.secondary)
                    Text(L("Очередь пока пуста")).font(.system(size: 14, weight: .medium))
                    Text(L("В Finder: правый клик → Службы →\nShareFile: Загрузить и поделиться")).font(.system(size: 12)).foregroundStyle(.secondary).multilineTextAlignment(.center).lineSpacing(4)
                }.frame(maxWidth: .infinity).padding(.vertical, 35)
            } else {
                TimelineView(.periodic(from: .now, by: 1)) { _ in
                    VStack(alignment: .leading, spacing: 12) {
                        let summary = model.transferSummary
                        VStack(alignment: .leading, spacing: 7) {
                            Text(L("Общий прогресс · {0}% · Готово {1} из {2}", String(Int(summary.percent)), String(summary.completed), String(summary.count))).font(.system(size: 12, weight: .medium))
                            ProgressView(value: summary.percent, total: 100).tint(green).accessibilityLabel(L("Общий прогресс подборки"))
                            Text(L("{0} из {1} · {2}", String(progressBytes(summary.offset)), String(progressBytes(summary.size)), String(summary.detail))).font(.system(size: 11)).foregroundStyle(.secondary)
                        }.padding(16).background(Color.white).clipShape(RoundedRectangle(cornerRadius: 12))
                        VStack(spacing: 1) {
                            ForEach(model.settings.uploads) { job in uploadRow(job) }
                        }.background(Color.white).clipShape(RoundedRectangle(cornerRadius: 12))
                    }.monospacedDigit()
                }
            }
            HStack(spacing: 7) {
                Image(systemName: "lock.shield")
                Text(L("Файлы хранятся на вашем диске. Подключение хранится в Keychain.")).font(.system(size: 11))
            }.foregroundStyle(.secondary)
        }
    }
    private func uploadRow(_ job: Upload) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 12) {
                Image(systemName: job.status == "done" ? "checkmark.circle.fill" : job.directory == nil ? "doc" : "folder").font(.system(size: 22, weight: .light)).foregroundStyle(job.status == "done" ? green : Color.secondary)
                VStack(alignment: .leading, spacing: 4) {
                    Text(job.name).font(.system(size: 13, weight: .medium)).lineLimit(1).help(job.source)
                    Text("\(progressBytes(Double(job.offset))) / \(progressBytes(Double(job.size))) · \(job.label)").font(.system(size: 11)).foregroundStyle(.secondary)
                }
                Spacer()
                if job.status == "uploading" || job.status == "queued" { Button { model.pause(job.id) } label: { Image(systemName: "pause") }.help(L("Приостановить")) }
                if job.status == "paused" || job.status == "error" { Button { model.resume(job.id) } label: { Image(systemName: "play") }.disabled(!model.connected).help(L("Продолжить")) }
                if !job.link.isEmpty { Button(L("Ссылка")) { model.copy(job.link) } }
                else if let saved = model.sentLinks.first(where: { $0.shareID == job.shareID && $0.active }) {
                    Button(L("Ссылка")) { model.copyHistory(saved) }
                }
                if ["uploaded", "uncertain", "done"].contains(job.status) && job.link.isEmpty {
                    Button(L("Новая ссылка")) { model.retryLink(job.id) }.disabled(!model.connected || model.activeCount > 0)
                }
                if !["uploading", "sharing", "cancelling"].contains(job.status) { Button { model.remove(job.id) } label: { Image(systemName: "xmark") }.help(job.status == "done" ? L("Убрать из очереди") : L("Отменить и убрать")) }
            }.buttonStyle(.borderless)
            if job.status == "uploading" || job.status == "paused" {
                ProgressView(value: Double(job.offset), total: Double(max(1, job.size))).tint(green)
            }
            if job.status == "uploading" && job.offset < job.size {
                let rate = model.rate(job.id)
                Text(transferDetails(rate, seconds: rate > 0 ? Double(job.size - job.offset) / rate : nil)).font(.system(size: 11)).foregroundStyle(.secondary)
            }
            if !job.message.isEmpty { Text(L(job.message)).font(.system(size: 11)).foregroundStyle(Color(red: 0.65, green: 0.27, blue: 0.15)).textSelection(.enabled) }
        }.padding(16)
    }
    private var settings: some View {
        VStack(alignment: .leading, spacing: 22) {
            GroupBox {
                VStack(alignment: .leading, spacing: 12) {
                    Picker(L("Язык приложения"), selection: Binding(get: { model.settings.language }, set: { model.setLanguage($0) })) {
                        Text("English").tag("en")
                        Text("Русский").tag("ru")
                    }
                    Text(L("Язык сохраняется сразу")).font(.system(size: 11)).foregroundStyle(.secondary)
                }.padding(14).frame(maxWidth: .infinity, alignment: .leading)
            }
            GroupBox {
                VStack(alignment: .leading, spacing: 15) {
                    Text(L("Подключение к хранилищу")).font(.system(size: 16, weight: .semibold))
                    Text(L("Укажите HTTPS-адрес KeenDNS. Вход и подтверждение\nэтого Mac пройдут в браузере.")).font(.system(size: 12)).foregroundStyle(.secondary).lineSpacing(4)
                    TextField("https://my-storage.keenetic.pro", text: $model.serverInput).textFieldStyle(.roundedBorder).disabled(model.connected || model.busy).accessibilityLabel(L("Адрес хранилища"))
                    HStack {
                        if model.connected { Button(L("Отключить Mac")) { model.disconnect() } }
                        else if !model.pairingURL.isEmpty {
                            ProgressView().controlSize(.small)
                            Button(L("Открыть подтверждение")) { if let url = URL(string: model.pairingURL) { NSWorkspace.shared.open(url) } }
                            Button(L("Отмена")) { model.cancelPairing() }
                        } else { Button(L("Подключить через браузер")) { model.connect() }.buttonStyle(.borderedProminent).disabled(model.busy || model.serverInput.isEmpty) }
                        Spacer()
                    }
                    if !model.pairingURL.isEmpty { Text(L("Ожидаем подтверждение в браузере…")).font(.system(size: 12)).foregroundStyle(.secondary) }
                }.padding(14).frame(maxWidth: .infinity, alignment: .leading)
            }
            GroupBox {
                VStack(alignment: .leading, spacing: 17) {
                    Text(L("Загрузка и публичные ссылки")).font(.system(size: 16, weight: .semibold))
                    HStack {
                        VStack(alignment: .leading, spacing: 5) {
                            Text(L("Папка назначения")).font(.system(size: 12)).foregroundStyle(.secondary)
                            Text(model.settings.folderName).font(.system(size: 13, weight: .medium))
                        }
                        Spacer()
                        Button(L("Выбрать папку")) { model.folderTrail = []; model.browseFolders() }.disabled(!model.connected)
                    }
                    Picker(L("Срок ссылки"), selection: $model.settings.days) { Text(L("1 день")).tag(1); Text(L("7 дней")).tag(7); Text(L("30 дней")).tag(30); Text(L("90 дней")).tag(90) }.disabled(model.settings.never)
                    Toggle(L("Без срока действия"), isOn: $model.settings.never)
                    SecureField(L("Пароль ссылки (необязательно, минимум 12 байт)"), text: $model.password).textFieldStyle(.roundedBorder)
                    Text(L("Пароль для новых ссылок хранится в Keychain. Отозвать отправленную ссылку можно в истории; права на файлы настраиваются в Web UI.")).font(.system(size: 11)).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    Button(L("Сохранить настройки")) { model.saveOptions() }.disabled(!model.connected)
                }.padding(14).frame(maxWidth: .infinity, alignment: .leading)
            }
            GroupBox {
                VStack(alignment: .leading, spacing: 13) {
                    Text(L("Finder и уведомления")).font(.system(size: 16, weight: .semibold))
                    Toggle(L("Загружать из Finder без открытия окна"), isOn: Binding(get: { model.settings.silentFinderUploads }, set: { model.setSilentFinderUploads($0) }))
                    Text(L("Файлы загружаются в фоне, ссылки копируются в буфер обмена. Окно можно открыть из меню ShareFile в строке меню.")).font(.system(size: 11)).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    Toggle(L("Уведомлять о загрузке и копировании ссылки"), isOn: Binding(get: { model.settings.notifyUploads }, set: { model.setNotifyUploads($0) }))
                    Text(L("При первом уведомлении macOS запросит разрешение. Оба переключателя сохраняются сразу.")).font(.system(size: 11)).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }.padding(14).frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }
    private var folderPicker: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                if model.folderTrail.count > 1 { Button { model.folderBack() } label: { Image(systemName: "chevron.left") } }
                Text(model.folderTrail.last?.1 ?? L("Папки")).font(.headline)
                Spacer()
                Button(L("Готово")) { model.showFolders = false }
            }
            Text(L("Выберите папку с правами управляющего: загрузка и создание ссылки.")).font(.system(size: 12)).foregroundStyle(.secondary)
            List(model.folders) { folder in
                HStack {
                    Button { model.browseFolders(folder.id, name: folder.name) } label: { Label(folder.name, systemImage: "folder") }.buttonStyle(.plain)
                    Spacer()
                    Button(L("Выбрать")) { model.selectFolder(folder) }.disabled(folder.role < 3)
                }.padding(.vertical, 4)
            }
        }.padding(22).frame(width: 520, height: 400)
    }
}

struct LinkHistoryView: View {
    @ObservedObject var model: Model
    @ObservedObject var history: LinkHistory
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            TextField(L("Найти файл или папку в истории"), text: $model.historySearch).textFieldStyle(.roundedBorder)
            Text(L("История хранит до 200 ссылок. Показаны ссылки текущего аккаунта. Очистка очереди сохраняет историю. Ссылки защищены Keychain."))
                .font(.system(size: 12)).foregroundStyle(.secondary)
            let entries = model.sentLinks.filter { model.historySearch.isEmpty || $0.name.localizedCaseInsensitiveContains(model.historySearch) }
            if entries.isEmpty { Text(model.historySearch.isEmpty ? L("История пока пуста") : L("Ничего не найдено")).foregroundStyle(.secondary).padding(.vertical, 28) }
            ForEach(entries) { entry in
                HStack(spacing: 14) {
                    Image(systemName: "link").foregroundStyle(green)
                    VStack(alignment: .leading, spacing: 5) {
                        Text(entry.name).font(.system(size: 13, weight: .medium)).lineLimit(2)
                        Text(entry.created.formatted(date: .numeric, time: .shortened) + " · " + entry.label).font(.system(size: 11)).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button(L("Копировать")) { model.copyHistory(entry) }.disabled(!entry.active)
                    Button(L("Открыть")) { model.openHistory(entry) }.disabled(!entry.active)
                    Button(L("Отозвать")) { model.revokeHistory(entry) }.disabled(!model.canRevokeHistory(entry))
                }.buttonStyle(.borderless).padding(16).background(Color.white).clipShape(RoundedRectangle(cornerRadius: 10))
            }
        }
    }
}
