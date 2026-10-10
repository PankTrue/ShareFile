"""X11 Keybinder and Wayland GlobalShortcuts portal, released on exit."""
import os
import uuid

import gi
from gi.repository import Gio, GLib

BUS = "org.freedesktop.portal.Desktop"
PATH = "/org/freedesktop/portal/desktop"
INTERFACE = "org.freedesktop.portal.GlobalShortcuts"
SHORTCUT = "upload-clipboard"
FALLBACK = "Горячая клавиша недоступна. Назначьте Ctrl+Alt+U для команды sharefile --clipboard в настройках рабочего окружения"


class GlobalShortcut:
    def __init__(self, app_id, activated, notice):
        self.app_id, self.activated, self.notice = app_id, activated, notice
        self.keybinder = self.connection = None
        self.session = ""
        self.subscriptions = set()
        self.requests = set()
        self.closed = False

    def start(self):
        if not os.environ.get("WAYLAND_DISPLAY") and os.environ.get("XDG_SESSION_TYPE") != "wayland":
            try:
                gi.require_version("Keybinder", "3.0")
                from gi.repository import Keybinder
                Keybinder.init()
                if Keybinder.bind("<Control><Alt>u", lambda *_: self.activated(""), None):
                    self.keybinder = Keybinder
                    self.notice("Изображение из буфера: Ctrl+Alt+U")
                    return
            except (ImportError, ValueError, GLib.Error):
                pass
        try:
            # A separate connection registers the host identity before any other portal call.
            address = Gio.dbus_address_get_for_bus_sync(Gio.BusType.SESSION, None)
            self.connection = Gio.DBusConnection.new_for_address_sync(address,
                Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT | Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION, None, None)
            self._call("org.freedesktop.host.portal.Registry", "Register",
                GLib.Variant("(sa{sv})", (self.app_id, {})), self._registered)
        except GLib.Error:
            self.notice(FALLBACK)

    def _call(self, interface, method, parameters, callback):
        def done(connection, result):
            if self.closed:
                return
            try:
                callback(connection.call_finish(result), None)
            except GLib.Error as error:
                callback(None, error)
        self.connection.call(BUS, PATH, interface, method, parameters, None,
                             Gio.DBusCallFlags.NONE, 10000, None, done)

    def _registered(self, result, error):
        # Registry was introduced after GlobalShortcuts; older portals lack it.
        if error and Gio.DBusError.get_remote_error(error) not in (
                "org.freedesktop.DBus.Error.UnknownMethod", "org.freedesktop.DBus.Error.UnknownInterface"):
            self.notice(FALLBACK)
            return
        self._request("CreateSession", "(a{sv})", (),
                      {"session_handle_token": GLib.Variant("s", "sf" + uuid.uuid4().hex)}, self._created)

    def _request(self, method, signature, arguments, options, callback):
        token = "sf" + uuid.uuid4().hex
        options = {**options, "handle_token": GLib.Variant("s", token)}
        sender = self.connection.get_unique_name()[1:].replace(".", "_")
        path = "/org/freedesktop/portal/desktop/request/" + sender + "/" + token
        self.requests.add(path)
        subscription = None
        def response(connection, sender, object_path, interface, signal, parameters):
            connection.signal_unsubscribe(subscription)
            self.subscriptions.discard(subscription)
            self.requests.discard(path)
            code, results = parameters.unpack()
            if code != 0:
                self.notice(FALLBACK)
            else:
                callback(results)
        subscription = self.connection.signal_subscribe(BUS, "org.freedesktop.portal.Request", "Response",
            path, None, Gio.DBusSignalFlags.NONE, response)
        self.subscriptions.add(subscription)
        def requested(result, error):
            if error:
                self.connection.signal_unsubscribe(subscription)
                self.subscriptions.discard(subscription)
                self.requests.discard(path)
                self.notice(FALLBACK)
        self._call(INTERFACE, method, GLib.Variant(signature, (*arguments, options)), requested)

    def _created(self, results):
        self.session = results.get("session_handle", "")
        if not self.session.startswith("/org/freedesktop/portal/desktop/session/"):
            self.notice(FALLBACK)
            return
        subscription = self.connection.signal_subscribe(BUS, INTERFACE, "Activated", PATH,
            None, Gio.DBusSignalFlags.NONE, self._activated)
        self.subscriptions.add(subscription)
        shortcuts = [(SHORTCUT, {"description": GLib.Variant("s", "Загрузить изображение из буфера в ShareFile"),
                                "preferred_trigger": GLib.Variant("s", "CTRL+ALT+u")})]
        self._request("BindShortcuts", "(oa(sa{sv})sa{sv})", (self.session, shortcuts, ""), {}, self._bound)

    def _bound(self, results):
        bound = next((options for identity, options in results.get("shortcuts", []) if identity == SHORTCUT), None)
        if bound is None:
            self.notice(FALLBACK)
        else:
            self.notice("Изображение из буфера: " + bound.get("trigger_description", "Ctrl+Alt+U"))

    def _activated(self, connection, sender, path, interface, signal, parameters):
        session, identity, timestamp, options = parameters.unpack()
        if not self.closed and session == self.session and identity == SHORTCUT:
            self.activated(options.get("activation_token", ""))

    def stop(self):
        self.closed = True
        if self.keybinder:
            self.keybinder.unbind("<Control><Alt>u")
        if self.connection:
            for subscription in self.subscriptions:
                self.connection.signal_unsubscribe(subscription)
            for path, interface in [(path, "org.freedesktop.portal.Request") for path in self.requests] + (
                    [(self.session, "org.freedesktop.portal.Session")] if self.session else []):
                self.connection.call(BUS, path, interface, "Close", None, None, Gio.DBusCallFlags.NONE, 1000, None, None)
            self.connection.close(None, None)
        self.subscriptions.clear()
        self.requests.clear()
