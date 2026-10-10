import os
from pathlib import Path
import sys
import time
from datetime import datetime

import gi
gi.require_version("Gtk", "3.0")
gi.require_version("Gdk", "3.0")
from gi.repository import Gdk, Gio, GLib, Gtk

from .bridge import ClientError, safe_message
from .controller import Controller
from .secrets import SecretStore
from .state import StateStore
from .clipboard import MAX_BYTES, MAX_PIXELS
from .hotkeys import GlobalShortcut
from .progress import transfer_details

APP_ID = "io.github.panktrue.ShareFile"
LABELS = {"queued": "В очереди", "uploading": "Загружается", "paused": "На паузе",
          "uploaded": "Загрузка завершена", "sharing": "Создаётся ссылка", "done": "Готово",
          "uncertain": "Проверьте ссылки в Web UI", "error": "Нужна ваша помощь", "cancelling": "Отменяется"}


def idle(callback):
    def run():
        callback()
        return GLib.SOURCE_REMOVE
    GLib.idle_add(run)


def file_paths(arguments, command_line):
    paths = []
    for argument in arguments:
        if argument == "--":
            continue
        file = command_line.create_file_for_arg(argument)
        path = file.get_path()
        if path is None:
            raise ClientError("Выберите локальные файлы. Сетевые URI пока не поддерживаются")
        paths.append(path)
    return paths


class Application(Gtk.Application):
    def __init__(self, helper=None, controller_factory=Controller, credentials=None, state_store=None,
                 application_id=APP_ID):
        super().__init__(application_id=application_id, flags=Gio.ApplicationFlags.HANDLES_COMMAND_LINE)
        for option, description in (("background", "Загрузить выбранные файлы в фоне"),
                                    ("clipboard", "Загрузить изображение из буфера обмена"),
                                    ("share", "Загрузить и поделиться"), ("quit", "Остановить очередь и выйти")):
            self.add_main_option(option, 0, GLib.OptionFlags.NONE, GLib.OptionArg.NONE, description, None)
        self.helper = Path(helper or Path(__file__).resolve().parents[1] / "sharefile-client")
        self.controller_factory = controller_factory
        self.credentials = credentials or SecretStore()
        self.state_store = state_store or StateStore()
        self.controller = None
        self.window = None
        self._snapshot = {}
        self._settings_identity = None
        self._quitting = False
        self._capturing_clipboard = False
        self.shortcut = None

    def do_startup(self):
        Gtk.Application.do_startup(self)
        self.hold()  # Keep GTK clipboard ownership and accept later file-manager invocations.
        action = Gio.SimpleAction.new("show", None)
        action.connect("activate", lambda *_: self.show())
        self.add_action(action)
        try:
            if self.get_dbus_connection() is None:
                raise ClientError("Нужна пользовательская D-Bus-сессия. Запустите ShareFile из графического окружения")
            if not self.helper.is_file() or not os.access(self.helper, os.X_OK):
                raise ClientError("Не найдено ядро передачи. Соберите или переустановите пакет ShareFile")
            self.controller = self.controller_factory(self.helper, self.state_store, self.credentials,
                dispatch=idle, changed=self.refresh, completed=self.complete,
                open_browser=self.open_url)
            self.build_window()
            self.build_tray()
            self.refresh(self.controller.snapshot())
            GLib.timeout_add_seconds(1, self.tick_progress)
            self.shortcut = GlobalShortcut(self.get_application_id(), self.upload_clipboard, self.shortcut_label.set_text)
            self.shortcut.start()
            self.controller.restore()
        except Exception as error:
            dialog = Gtk.MessageDialog(message_type=Gtk.MessageType.ERROR, buttons=Gtk.ButtonsType.CLOSE,
                                       text="ShareFile не удалось запустить")
            dialog.format_secondary_text(safe_message(error))
            dialog.run()
            dialog.destroy()
            idle(self.quit)

    def do_activate(self):
        self.show()

    def do_command_line(self, command_line):
        if self.controller is None:
            return 1
        options = command_line.get_options_dict()
        if options.contains("quit"):
            self.stop(command_line)
            return 0
        try:
            if options.contains("clipboard"):
                self.upload_clipboard()
                return 0
            paths = file_paths(command_line.get_arguments()[1:], command_line)
            background = options.contains("background") or options.contains("share")
            snapshot = self.controller.snapshot()
            if paths:
                self.controller.add_files(paths)
            if not background or not paths or not snapshot["settings"]["silent_uploads"] or not snapshot["settings"]["origin"]:
                # An autostart invocation has no files and must stay silent.
                if not (background and not paths):
                    self.show(1 if paths and not snapshot["settings"]["origin"] else None)
            return 0
        except Exception as error:
            self.message(safe_message(error))
            self.show()
            return 1

    def show(self, page=None):
        if self.window:
            self.window.show_all()
            # Notebook pages must be visible before selecting the initial tab.
            if page is not None:
                self.notebook.set_current_page(page)
            self.window.present()

    def open_url(self, url):
        try:
            Gio.AppInfo.launch_default_for_uri(url, None)
        except GLib.Error:
            self.message("Не удалось открыть браузер. Откройте адрес сервера вручную")
            self.show()

    def button(self, label, callback, box):
        button = Gtk.Button(label=label)
        button.connect("clicked", lambda *_: self.invoke(callback))
        box.pack_start(button, False, False, 0)
        return button

    def invoke(self, callback):
        try:
            return callback()
        except Exception as error:
            self.message(safe_message(error))

    def message(self, text):
        if self.window:
            self.notice.set_text(text)

    def build_window(self):
        self.window = Gtk.ApplicationWindow(application=self, title="ShareFile")
        self.window.set_default_size(860, 560)
        self.window.connect("delete-event", self.hide_window)
        outer = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=10, margin=16)
        self.window.add(outer)
        header = Gtk.Box(spacing=8)
        self.connection = Gtk.Label(xalign=0)
        header.pack_start(self.connection, True, True, 0)
        self.button("Web UI", lambda: self.open_url(self._snapshot["settings"]["origin"]), header)
        self.button("Выйти", self.stop, header)
        outer.pack_start(header, False, False, 0)
        self.notebook = Gtk.Notebook()
        outer.pack_start(self.notebook, True, True, 0)
        self.build_queue()
        self.build_settings()
        self.build_history()
        self.notice = Gtk.Label(xalign=0, wrap=True, selectable=True)
        outer.pack_start(self.notice, False, False, 0)
        self.window.drag_dest_set(Gtk.DestDefaults.ALL, [Gtk.TargetEntry.new("text/uri-list", 0, 0)], Gdk.DragAction.COPY)
        self.window.connect("drag-data-received", self.drop_files)

    def hide_window(self, *_):
        self.window.hide()
        return True

    def tick_progress(self):
        if self._quitting:
            return GLib.SOURCE_REMOVE
        if any(job["status"] == "uploading" for job in self._snapshot.get("jobs", [])):
            self.refresh(self.controller.snapshot())
        return GLib.SOURCE_CONTINUE

    def build_queue(self):
        page = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=10, margin=12)
        tools = Gtk.Box(spacing=8)
        self.add_button = self.button("Загрузить и поделиться…", self.choose_files, tools)
        self.upload_folder_button = self.button("Загрузить папку…", self.choose_directory, tools)
        self.button("Из буфера", self.upload_clipboard, tools).set_tooltip_text("Скриншот → Ctrl+Alt+U → ссылка")
        self.button("Пауза всех", self.controller.pause_all, tools)
        self.button("Удалить завершённые", self.controller.clear_finished, tools)
        page.pack_start(tools, False, False, 0)
        self.shortcut_label = Gtk.Label(label="Изображение из буфера: Ctrl+Alt+U", xalign=0, wrap=True)
        page.pack_start(self.shortcut_label, False, False, 0)
        self.total_progress = Gtk.ProgressBar(show_text=True)
        self.total_progress.set_tooltip_text("Общий прогресс подборки")
        page.pack_start(self.total_progress, False, False, 0)
        self.total_detail = Gtk.Label(xalign=0, wrap=True)
        page.pack_start(self.total_detail, False, False, 0)
        self.rows = Gtk.ListStore(str, str, str, int, str, str, str)
        self.tree = Gtk.TreeView(model=self.rows)
        for title, column in (("Файл или папка", 1), ("Размер", 2), ("Состояние", 4)):
            renderer = Gtk.CellRendererText()
            renderer.set_property("ellipsize", 3)
            item = Gtk.TreeViewColumn(title, renderer, text=column)
            item.set_resizable(True)
            item.set_expand(column == 1)
            self.tree.append_column(item)
        self.tree.insert_column(Gtk.TreeViewColumn("Прогресс", Gtk.CellRendererProgress(), value=3), 2)
        self.tree.append_column(Gtk.TreeViewColumn("Скорость / осталось", Gtk.CellRendererText(), text=6))
        self.tree.set_tooltip_column(5)
        self.tree.get_selection().connect("changed", lambda *_: self.refresh_actions())
        scroll = Gtk.ScrolledWindow()
        scroll.add(self.tree)
        page.pack_start(scroll, True, True, 0)
        actions = Gtk.Box(spacing=6)
        self.job_buttons = {}
        for key, title, method in (("pause", "Пауза", self.controller.pause), ("resume", "Продолжить", self.controller.resume),
                                   ("remove", "Удалить / отменить", self.controller.remove), ("copy", "Копировать", self.copy_selected),
                                   ("new", "Новая ссылка", self.new_link), ("revoke", "Отозвать", self.controller.revoke)):
            self.job_buttons[key] = self.button(title, lambda method=method: method(self.selected()), actions)
        page.pack_start(actions, False, False, 0)
        self.detail = Gtk.Label(xalign=0, wrap=True, selectable=True)
        page.pack_start(self.detail, False, False, 0)
        self.notebook.append_page(page, Gtk.Label(label="Загрузки"))

    def build_settings(self):
        page = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12, margin=16)
        server_row = Gtk.Box(spacing=8)
        self.origin = Gtk.Entry(placeholder_text="https://sharefile.example.org", hexpand=True)
        server_row.pack_start(self.origin, True, True, 0)
        self.connect_button = self.button("Подключить", lambda: self.controller.connect(self.origin.get_text()), server_row)
        self.button("Отменить", self.controller.cancel_pairing, server_row)
        self.disconnect_button = self.button("Отключить", self.controller.disconnect, server_row)
        page.pack_start(Gtk.Label(label="Адрес хранилища. Подтверждение откроется в браузере", xalign=0), False, False, 0)
        page.pack_start(server_row, False, False, 0)
        self.pair_button = Gtk.Button(label="Открыть подтверждение в браузере")
        self.pair_button.connect("clicked", lambda *_: self.open_url(self._snapshot.get("pairing_url", "")))
        page.pack_start(self.pair_button, False, False, 0)
        folder_row = Gtk.Box(spacing=8)
        self.folder_label = Gtk.Label(xalign=0, hexpand=True)
        folder_row.pack_start(self.folder_label, True, True, 0)
        self.folder_button = self.button("Выбрать папку…", self.browse_folders, folder_row)
        page.pack_start(folder_row, False, False, 0)
        expiry = Gtk.Box(spacing=8)
        expiry.pack_start(Gtk.Label(label="Срок ссылки, дней"), False, False, 0)
        self.days = Gtk.SpinButton.new_with_range(1, 365, 1)
        expiry.pack_start(self.days, False, False, 0)
        self.never = Gtk.CheckButton(label="Бессрочно")
        expiry.pack_start(self.never, False, False, 0)
        page.pack_start(expiry, False, False, 0)
        self.password_entry = Gtk.Entry(placeholder_text="Пароль ссылки: от 12 до 256 байт; пусто — без пароля", visibility=False)
        page.pack_start(self.password_entry, False, False, 0)
        self.silent = Gtk.CheckButton(label="Загружать из файлового менеджера без открытия окна")
        self.notify = Gtk.CheckButton(label="Уведомлять о завершении загрузки")
        page.pack_start(self.silent, False, False, 0)
        page.pack_start(self.notify, False, False, 0)
        controls = Gtk.Box()
        self.save_button = self.button("Сохранить настройки", lambda: self.controller.configure(
            self.days.get_value_as_int(), self.never.get_active(), self.password_entry.get_text(),
            self.silent.get_active(), self.notify.get_active()), controls)
        page.pack_start(controls, False, False, 0)
        explanation = Gtk.Label(xalign=0, wrap=True, label="Токен и пароль хранятся в GNOME Keyring / KWallet (Secret Service). "
            "Закрытие окна оставляет ShareFile в фоне. Повторный запуск открывает очередь. Для выхода используйте кнопку «Выйти».")
        page.pack_start(explanation, False, False, 0)
        self.notebook.append_page(page, Gtk.Label(label="Настройки"))

    def build_tray(self):
        # Supported by Plasma/Xfce; GNOME can use the launcher or a tray extension.
        self.tray = Gtk.StatusIcon.new_from_icon_name("folder-publicshare")
        self.tray.set_title("ShareFile")
        self.tray.set_tooltip_text("ShareFile — загрузить и поделиться")
        self.tray.connect("activate", lambda *_: self.show())
        self.tray_menu = Gtk.Menu()
        for title, callback in (("Открыть ShareFile", self.show), ("Из буфера (Ctrl+Alt+U)", self.upload_clipboard), ("История ссылок", self.show_history), ("Пауза всех", self.controller.pause_all), ("Выйти", self.stop)):
            item = Gtk.MenuItem(label=title)
            item.connect("activate", lambda item, callback=callback: self.invoke(callback))
            self.tray_menu.append(item)
        self.recent_menu = Gtk.Menu()
        recent = Gtk.MenuItem(label="Недавние ссылки")
        recent.set_submenu(self.recent_menu)
        self.tray_menu.prepend(recent)
        self.tray_menu.show_all()
        self.tray.connect("popup-menu", lambda icon, button, timestamp:
                          self.tray_menu.popup(None, None, Gtk.StatusIcon.position_menu, icon, button, timestamp))

    def show_history(self):
        self.show(2)

    @staticmethod
    def history_status(entry):
        if entry["revoked"]:
            return "Отозвана"
        if entry["expires"] and entry["expires"] <= time.time():
            return "Срок истёк"
        return "Бессрочно" if not entry["expires"] else "До " + datetime.fromtimestamp(entry["expires"]).strftime("%d.%m.%Y %H:%M")

    def build_history(self):
        page = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=10, margin=12)
        self.history_search = Gtk.SearchEntry(placeholder_text="Найти файл или папку в истории")
        self.history_search.connect("search-changed", lambda *_: self.refresh_history())
        page.pack_start(self.history_search, False, False, 0)
        self.history_rows = Gtk.ListStore(str, str, str, str)
        self.history_tree = Gtk.TreeView(model=self.history_rows)
        for title, column in (("Файл или папка", 1), ("Создана", 2), ("Состояние", 3)):
            item = Gtk.TreeViewColumn(title, Gtk.CellRendererText(), text=column)
            item.set_expand(column == 1)
            item.set_resizable(True)
            self.history_tree.append_column(item)
        self.history_tree.get_selection().connect("changed", lambda *_: self.refresh_history_actions())
        scroll = Gtk.ScrolledWindow()
        scroll.add(self.history_tree)
        page.pack_start(scroll, True, True, 0)
        controls = Gtk.Box(spacing=8)
        self.history_buttons = {}
        for key, label, callback in (("copy", "Копировать ссылку", self.copy_history),
                                     ("open", "Открыть ссылку", self.open_history),
                                     ("revoke", "Отозвать ссылку", self.controller.revoke_history)):
            self.history_buttons[key] = self.button(label, lambda callback=callback: callback(self.selected_history()), controls)
        page.pack_start(controls, False, False, 0)
        page.pack_start(Gtk.Label(xalign=0, wrap=True, label="История хранит до 200 ссылок. Показаны ссылки текущего аккаунта. Ссылки сохраняются в Secret Service. Очистка очереди сохраняет историю."), False, False, 0)
        self.notebook.append_page(page, Gtk.Label(label="История ссылок"))

    def selected_history(self):
        model, iterator = self.history_tree.get_selection().get_selected()
        return model[iterator][0] if iterator else ""

    def refresh_history(self):
        selected = self.selected_history()
        self.history_rows.clear()
        query = self.history_search.get_text().casefold().strip()
        for entry in self._snapshot.get("history", []):
            if query not in entry["name"].casefold():
                continue
            row = self.history_rows.append([entry["share_id"], entry["name"],
                datetime.fromtimestamp(entry["created"]).strftime("%d.%m.%Y %H:%M"), self.history_status(entry)])
            if entry["share_id"] == selected:
                self.history_tree.get_selection().select_iter(row)
        self.refresh_history_actions()

    def refresh_history_actions(self):
        entry = next((entry for entry in self._snapshot.get("history", []) if entry["share_id"] == self.selected_history()), None)
        active = entry and not entry["revoked"] and (not entry["expires"] or entry["expires"] > time.time())
        for key, button in self.history_buttons.items():
            button.set_sensitive(bool(entry and (not entry["revoked"] if key == "revoke" else active)
                                      and not self._snapshot.get("busy", False)
                                      and (key != "revoke" or self._snapshot.get("connected", False))))

    def copy_history(self, identity):
        self.controller.history_link(identity, lambda url: self.controller.set_notice(
            "Ссылка скопирована" if self.clipboard([url]) else "Не удалось скопировать ссылку"))

    def open_history(self, identity):
        self.controller.history_link(identity, self.open_url)

    def refresh_recent(self):
        if not hasattr(self, "recent_menu"):
            return
        for child in self.recent_menu.get_children():
            self.recent_menu.remove(child)
        for entry in self._snapshot.get("history", [])[:8]:
            item = Gtk.MenuItem(label=entry["name"])
            submenu = Gtk.Menu()
            item.set_submenu(submenu)
            active = not entry["revoked"] and (not entry["expires"] or entry["expires"] > time.time())
            for title, callback in (("Копировать", self.copy_history), ("Открыть", self.open_history), ("Отозвать", self.controller.revoke_history)):
                action = Gtk.MenuItem(label=title)
                action.set_sensitive((not entry["revoked"] and self._snapshot.get("connected", False)) if title == "Отозвать" else active)
                action.connect("activate", lambda _, callback=callback, identity=entry["share_id"]: self.invoke(lambda: callback(identity)))
                submenu.append(action)
            self.recent_menu.append(item)
        if not self._snapshot.get("history"):
            empty = Gtk.MenuItem(label="История пока пуста")
            empty.set_sensitive(False)
            self.recent_menu.append(empty)
        self.recent_menu.show_all()

    def selected(self):
        model, iterator = self.tree.get_selection().get_selected()
        return model[iterator][0] if iterator else ""

    def refresh(self, snapshot):
        if self._quitting:
            return
        self._snapshot = snapshot
        total = snapshot.get("progress")
        if total:
            self.total_progress.set_fraction(total["percent"] / 100)
            self.total_progress.set_text(f"Общий прогресс · {total['percent']}% · Готово {total['completed']} из {total['count']}")
            self.total_detail.set_text(f"{self.size_label(total['offset'])} из {self.size_label(total['size'])} · {total['detail']}")
        selected = self.selected()
        self.rows.clear()
        for job in snapshot["jobs"]:
            percent = int(job["offset"] * 100 // job["size"]) if job["size"] else (100 if job["status"] == "done" else 0)
            rate = job.get("rate", 0)
            detail = transfer_details(rate, (job["size"] - job["offset"]) / rate if rate > 0 else None) if job["status"] == "uploading" and job["offset"] < job["size"] else "—"
            iterator = self.rows.append([job["id"], job["name"], self.size_label(job["size"]), percent,
                                        LABELS.get(job["status"], job["status"]), job["message"], detail])
            if job["id"] == selected:
                self.tree.get_selection().select_iter(iterator)
        settings = snapshot["settings"]
        identity = (settings["origin"], settings["owner"], snapshot["connected"])
        if identity != self._settings_identity:
            self._settings_identity = identity
            self.origin.set_text(settings["origin"])
            self.days.set_value(settings["days"])
            self.never.set_active(settings["never"])
            self.silent.set_active(settings["silent_uploads"])
            self.notify.set_active(settings["notify_uploads"])
            self.password_entry.set_text(self.controller.password)
        self.connection.set_text(("Подключено: " + settings["username"]) if snapshot["connected"] else "Хранилище не подключено")
        self.folder_label.set_text("Папка назначения: " + settings["folder_name"])
        self.connect_button.set_sensitive(not snapshot["connected"] and not snapshot["busy"])
        self.disconnect_button.set_sensitive(snapshot["connected"] and not snapshot["busy"])
        self.folder_button.set_sensitive(snapshot["connected"] and not snapshot["busy"])
        self.save_button.set_sensitive(not snapshot["busy"])
        self.pair_button.set_sensitive(bool(snapshot["pairing_url"]))
        self.message(snapshot["notice"])
        self.refresh_actions()
        self.refresh_history()
        self.refresh_recent()
        if not snapshot["connected"] and snapshot["pending_files"]:
            self.show(1)

    @staticmethod
    def size_label(size):
        for unit in ("Б", "КиБ", "МиБ", "ГиБ", "ТиБ"):
            if size < 1024 or unit == "ТиБ":
                return f"{size:.1f} {unit}" if unit != "Б" else f"{size} {unit}"
            size /= 1024

    def refresh_actions(self):
        job = next((job for job in self._snapshot.get("jobs", []) if job["id"] == self.selected()), None)
        connected = self._snapshot.get("connected", False)
        busy = self._snapshot.get("busy", False)
        for key, button in self.job_buttons.items():
            enabled = False
            if job:
                status = job["status"]
                enabled = {"pause": status in ("queued", "uploading"),
                           "resume": connected and status in ("paused", "error"),
                           "remove": status not in ("uploading", "sharing", "cancelling") and (connected or status in ("done", "uploaded", "uncertain")),
                           "copy": bool(job["link"]) or any(entry["share_id"] == job["share_id"] and not entry["revoked"]
                               and (not entry["expires"] or entry["expires"] > time.time()) for entry in self._snapshot.get("history", [])), "new": connected and status in ("done", "uploaded", "uncertain"),
                           "revoke": connected and bool(job["share_id"])}[key]
            button.set_sensitive(enabled and (not busy or key == "pause"))
        self.detail.set_text(job["message"] if job else "Перетащите файлы или папку сюда. Для папки создаётся одна ссылка")

    def choose_files(self):
        dialog = Gtk.FileChooserDialog(title="Загрузить и поделиться", transient_for=self.window,
                                       action=Gtk.FileChooserAction.OPEN)
        dialog.add_buttons("Отмена", Gtk.ResponseType.CANCEL, "Загрузить", Gtk.ResponseType.OK)
        dialog.set_select_multiple(True)
        dialog.set_local_only(True)
        if dialog.run() == Gtk.ResponseType.OK:
            self.controller.add_files(dialog.get_filenames())
        dialog.destroy()

    def drop_files(self, widget, context, x, y, data, info, timestamp):
        try:
            paths = [Gio.File.new_for_uri(uri).get_path() for uri in data.get_uris()]
            if not paths or any(path is None for path in paths):
                raise ClientError("Выберите локальные файлы")
            self.controller.add_files(paths)
            Gtk.drag_finish(context, True, False, timestamp)
        except Exception as error:
            self.message(safe_message(error))
            Gtk.drag_finish(context, False, False, timestamp)

    def choose_directory(self):
        dialog = Gtk.FileChooserDialog(title="Загрузить папку целиком", transient_for=self.window,
                                       action=Gtk.FileChooserAction.SELECT_FOLDER)
        dialog.add_buttons("Отмена", Gtk.ResponseType.CANCEL, "Загрузить", Gtk.ResponseType.OK)
        dialog.set_select_multiple(True)
        dialog.set_local_only(True)
        if dialog.run() == Gtk.ResponseType.OK:
            self.controller.add_files(dialog.get_filenames())
        dialog.destroy()

    def upload_clipboard(self, activation_token=""):
        if self._capturing_clipboard or self._quitting:
            return
        if not self.controller.snapshot()["connected"]:
            self.controller.set_notice("Сначала подключите хранилище")
            self.show(1)
            return
        self._capturing_clipboard = True
        display = Gdk.Display.get_default()
        wayland = "Wayland" in display.__gtype__.name
        if wayland:
            # Native Wayland gives clipboard contents only to focused clients.
            if activation_token and hasattr(display, "set_startup_notification_id"):
                display.set_startup_notification_id(activation_token)
            self.show()
        def received(clipboard, image, data):
            self._capturing_clipboard = False
            try:
                if image is None:
                    raise ClientError("В буфере обмена нет изображения. Скопируйте скриншот и повторите")
                if image.get_width() * image.get_height() > MAX_PIXELS:
                    raise ClientError("Изображение слишком большое (максимум 25 Мп)")
                ok, png = image.save_to_bufferv("png", [], [])
                if not ok or len(png) > MAX_BYTES:
                    raise ClientError("Изображение слишком большое (максимум 32 МиБ)")
                future = self.controller.add_clipboard(bytes(png))
                if future:
                    def finished(result):
                        try:
                            result.result()
                        except Exception as error:
                            message = safe_message(error)
                            idle(lambda: self.clipboard_failure(message))
                    future.add_done_callback(finished)
            except Exception as error:
                self.clipboard_failure(safe_message(error))
        def request():
            Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD).request_image(received, None)
        if wayland and not self.window.is_active():
            self._wait_clipboard_focus(request)
        else:
            request()

    def _wait_clipboard_focus(self, callback):
        deadline = time.monotonic() + 3
        def check():
            if self._quitting:
                self._capturing_clipboard = False
                return GLib.SOURCE_REMOVE
            if self.window.is_active():
                callback()
                return GLib.SOURCE_REMOVE
            if time.monotonic() >= deadline:
                self._capturing_clipboard = False
                self.clipboard_failure("Нажмите «Из буфера» в окне ShareFile: рабочее окружение не разрешило доступ к буферу в фоне")
                return GLib.SOURCE_REMOVE
            return GLib.SOURCE_CONTINUE
        GLib.timeout_add(50, check)

    def clipboard_failure(self, message):
        self.controller.set_notice(message)
        if self.controller.snapshot()["settings"]["notify_uploads"]:
            self.notification("Не удалось загрузить изображение", message)

    def clipboard(self, links):
        try:
            if "Wayland" in Gdk.Display.get_default().__gtype__.name and not self.window.is_active():
                return False
            clipboard = Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD)
            text = "\n".join(links)
            clipboard.set_text(text, -1)
            clipboard.store()
            return clipboard.wait_for_text() == text
        except (GLib.Error, RuntimeError):
            return False

    def complete(self, batch):
        if batch["failed"]:
            text = f"Не удалось завершить загрузку файлов: {batch['failed']}. Откройте ShareFile"
            self.controller.set_notice(text)
            if batch["notify"]:
                self.notification("Загрузка требует внимания", text)
            return
        copied = self.clipboard(batch["links"])
        text = "Ссылки скопированы" if copied else "Не удалось скопировать ссылки. Откройте ShareFile и нажмите «Копировать»"
        self.controller.set_notice(text)
        if batch["notify"]:
            # Notification contains names and copy status, never URLs or credentials.
            files = batch["names"][0] if len(batch["names"]) == 1 else f"Загружено объектов: {len(batch['names'])}"
            self.notification("Загрузка завершена", files + "\n" + text)

    def notification(self, title, body):
        notification = Gio.Notification.new(title)
        notification.set_body(body)
        notification.set_default_action("app.show")
        self.send_notification(None, notification)

    def copy_selected(self, identity):
        job = next(job for job in self._snapshot["jobs"] if job["id"] == identity)
        if not job["link"]:
            self.copy_history(job["share_id"])
            return
        self.controller.set_notice("Ссылка скопирована" if self.clipboard([job["link"]]) else "Не удалось скопировать ссылку")

    def new_link(self, identity):
        dialog = Gtk.MessageDialog(transient_for=self.window, modal=True, message_type=Gtk.MessageType.QUESTION,
                                   buttons=Gtk.ButtonsType.OK_CANCEL, text="Создать новую публичную ссылку?")
        dialog.format_secondary_text("Проверьте существующие ссылки в Web UI. Новая ссылка не отзывает предыдущую.")
        response = dialog.run()
        dialog.destroy()
        if response == Gtk.ResponseType.OK:
            self.controller.new_link(identity)

    def browse_folders(self):
        dialog = Gtk.Dialog(title="Папка назначения", transient_for=self.window, modal=True)
        dialog.set_default_size(520, 420)
        dialog.add_buttons("Закрыть", Gtk.ResponseType.CLOSE)
        box = dialog.get_content_area()
        trail = [("", "Доступные папки")]
        title = Gtk.Label(label=trail[-1][1])
        listing = Gtk.ListBox()
        scroll = Gtk.ScrolledWindow(hexpand=True, vexpand=True)
        scroll.add(listing)
        box.pack_start(title, False, False, 8)
        box.pack_start(scroll, True, True, 8)
        controls = Gtk.Box(spacing=8)
        box.pack_start(controls, False, False, 8)
        alive = [True]
        def fill(future):
            def render():
                if not alive[0]:
                    return
                try:
                    folders = future.result()
                    for row in listing.get_children():
                        listing.remove(row)
                    title.set_text(trail[-1][1])
                    for folder in folders:
                        row = Gtk.ListBoxRow()
                        row.folder = folder
                        row.add(Gtk.Label(label=folder["name"] + (" (только чтение)" if folder["effective_role"] < 3 else ""), xalign=0, margin=8))
                        listing.add(row)
                    listing.show_all()
                except Exception as error:
                    title.set_text(safe_message(error))
            idle(render)
        def load():
            future = self.controller.folders(trail[-1][0])
            if future:
                future.add_done_callback(fill)
        def descend():
            row = listing.get_selected_row()
            if row:
                trail.append((row.folder["id"], row.folder["name"]))
                load()
        def back():
            if len(trail) > 1:
                trail.pop()
                load()
        def choose():
            row = listing.get_selected_row()
            identity = row.folder["id"] if row else trail[-1][0]
            if identity:
                self.controller.select_folder(identity)
                dialog.response(Gtk.ResponseType.CLOSE)
        self.button("Назад", back, controls)
        self.button("Открыть папку", descend, controls)
        self.button("Выбрать", choose, controls)
        listing.connect("row-activated", lambda *_: descend())
        load()
        dialog.show_all()
        dialog.run()
        alive[0] = False
        dialog.destroy()

    def stop(self, command_line=None):
        if self._quitting:
            return
        self._quitting = True
        if self.shortcut:
            self.shortcut.stop()
        self.message("Останавливается передача…")
        def finish():
            # Retain the remote command line until the helper has stopped, so an
            # installer waiting on --quit can safely replace its executable.
            if command_line is not None:
                command_line.set_exit_status(0)
                if hasattr(command_line, "done"):
                    command_line.done()
            self.quit()
        if self.controller:
            self.controller.close().add_done_callback(lambda future: idle(finish))
        else:
            finish()


def main():
    return Application().run(sys.argv)
