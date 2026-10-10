"""Run in Linux with dbus-run-session -- xvfb-run -a python3 -m unittest ..."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
GTK_AVAILABLE = False
if sys.platform.startswith("linux") and (os.environ.get("SHAREFILE_REQUIRE_GTK") or os.environ.get("SHAREFILE_TEST_GTK")):
    try:
        import gi
        gi.require_version("Gtk", "3.0")
        gi.require_version("Gdk", "3.0")
        from gi.repository import Gdk, Gio, GLib, Gtk
        GTK_AVAILABLE = Gtk.init_check()[0]
    except (ImportError, ValueError):
        pass
if os.environ.get("SHAREFILE_REQUIRE_GTK") and not GTK_AVAILABLE:
    raise RuntimeError("GTK/Xvfb session required for this test run")


@unittest.skipUnless(GTK_AVAILABLE, "requires a Linux GTK/D-Bus display session")
class GtkTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        from sharefile_linux.app import Application
        from sharefile_linux.controller import Controller
        from sharefile_linux.state import StateStore
        from test_client import FakeBridge, MemoryCredentials, OWNER, FOLDER
        cls.directory = tempfile.TemporaryDirectory(prefix="sharefile-gtk-")
        cls.root = Path(cls.directory.name)
        helper = cls.root / "helper"
        helper.write_text("#!/bin/sh\nexit 0\n")
        helper.chmod(0o755)
        store = StateStore(cls.root / "state/state.json")
        state = store.load()
        state["settings"].update(origin="https://example.test", owner=OWNER, folder=FOLDER, username="tester")
        store.save(state)
        credentials = MemoryCredentials()
        credentials.write("https://example.test", "gtk-test-only-token")
        FakeBridge.requests = []
        FakeBridge.counter = 0
        FakeBridge.block_upload = FakeBridge.started = None
        FakeBridge.share_error = FakeBridge.cancel_error = False
        def factory(*args, **kwargs):
            return Controller(*args, **kwargs, bridge_factory=FakeBridge)
        cls.app = Application(helper, factory, credentials, store, application_id="io.github.panktrue.ShareFile.Test" + uuid.uuid4().hex)
        cls.app.register(None)
        cls.wait_idle()

    @classmethod
    def spin(cls):
        context = GLib.MainContext.default()
        while context.pending():
            context.iteration(False)

    @classmethod
    def wait_idle(cls):
        for _ in range(500):
            cls.spin()
            snapshot = cls.app.controller.snapshot()
            if not snapshot["active_id"] and not snapshot["busy"]:
                cls.spin()
                return
            time.sleep(0.01)
        raise AssertionError("GTK queue timed out")

    @classmethod
    def tearDownClass(cls):
        cls.app.shortcut.stop()
        cls.app.controller.close().result(timeout=5)
        cls.app.tray.set_visible(False)
        cls.app.window.destroy()
        cls.app.release()
        cls.spin()
        cls.directory.cleanup()

    def test_native_window_initially_hidden_manual_open_and_hide(self):
        self.app.window.hide()
        self.assertFalse(self.app.window.get_visible())
        self.app.show()
        self.assertTrue(self.app.window.get_visible())
        self.assertTrue(self.app.hide_window())
        self.assertFalse(self.app.window.get_visible())

    def test_global_shortcut_captures_image_while_main_window_is_hidden(self):
        from gi.repository import GdkPixbuf
        self.assertIsNotNone(self.app.shortcut.keybinder)
        notifications = []
        original = self.app.notification
        self.app.notification = lambda title, body: notifications.append((title, body))
        try:
            clipboard = Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD)
            image = GdkPixbuf.Pixbuf.new(GdkPixbuf.Colorspace.RGB, True, 8, 2, 2)
            image.fill(0xff000080)
            clipboard.set_image(image)
            self.app.window.hide()
            subprocess.run(["xdotool", "key", "ctrl+alt+u"], check=True, timeout=5)
            for _ in range(500):
                self.spin()
                if notifications:
                    break
                time.sleep(0.01)
            self.wait_idle()
            self.assertEqual(len(notifications), 1)
            self.assertIn("Ссылки скопированы", notifications[0][1])
            self.assertIn("/s/", clipboard.wait_for_text())
            self.assertFalse(self.app.window.get_visible())
            self.assertEqual(list(self.root.glob("state/clipboard/Screenshot-*.png")), [])
            before = len(self.app.controller.snapshot()["jobs"])
            self.app.upload_clipboard()  # The clipboard now contains text, not an image.
            for _ in range(100):
                self.spin()
                if not self.app._capturing_clipboard:
                    break
                time.sleep(0.01)
            self.assertEqual(len(self.app.controller.snapshot()["jobs"]), before)
            self.assertIn("нет изображения", self.app.controller.snapshot()["notice"])
        finally:
            self.app.notification = original

    def test_wayland_portal_protocol_registration_binding_and_activation(self):
        from sharefile_linux import hotkeys
        bus = "io.github.panktrue.ShareFile.TestPortal"
        connection = Gio.bus_get_sync(Gio.BusType.SESSION, None)
        result = connection.call_sync("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus",
            "RequestName", GLib.Variant("(su)", (bus, 0)), None, Gio.DBusCallFlags.NONE, 1000, None)
        self.assertEqual(result.unpack()[0], 1)
        xml = '''<node>
        <interface name="org.freedesktop.host.portal.Registry"><method name="Register">
          <arg type="s" direction="in"/><arg type="a{sv}" direction="in"/></method></interface>
        <interface name="org.freedesktop.portal.GlobalShortcuts"><method name="CreateSession">
          <arg type="a{sv}" direction="in"/><arg type="o" direction="out"/></method>
          <method name="BindShortcuts"><arg type="o" direction="in"/><arg type="a(sa{sv})" direction="in"/>
          <arg type="s" direction="in"/><arg type="a{sv}" direction="in"/><arg type="o" direction="out"/></method></interface>
        </node>'''
        info = Gio.DBusNodeInfo.new_for_xml(xml)
        calls, activations, notices = [], [], []
        session = "/org/freedesktop/portal/desktop/session/sharefile/test"
        def called(conn, sender, path, interface, method, parameters, invocation):
            values = parameters.unpack()
            calls.append((method, values))
            if method == "Register":
                invocation.return_value(GLib.Variant("()", ()))
                return
            options = values[-1]
            request = "/org/freedesktop/portal/desktop/request/" + sender[1:].replace(".", "_") + "/" + options["handle_token"]
            invocation.return_value(GLib.Variant("(o)", (request,)))
            output = {"session_handle": GLib.Variant("s", session)} if method == "CreateSession" else {
                "shortcuts": GLib.Variant("a(sa{sv})", [(hotkeys.SHORTCUT, {"trigger_description": GLib.Variant("s", "Ctrl+Alt+U")})])}
            GLib.idle_add(lambda: (conn.emit_signal(sender, request, "org.freedesktop.portal.Request", "Response",
                GLib.Variant("(ua{sv})", (0, output))), GLib.SOURCE_REMOVE)[1])
        registrations = [connection.register_object(hotkeys.PATH, interface, called, None, None) for interface in info.interfaces]
        shortcut = hotkeys.GlobalShortcut("io.github.panktrue.ShareFile", activations.append, notices.append)
        try:
            with patch.object(hotkeys, "BUS", bus), patch.dict(os.environ, {"WAYLAND_DISPLAY": "test"}):
                shortcut.start()
                for _ in range(500):
                    self.spin()
                    if any("Ctrl+Alt+U" in notice for notice in notices):
                        break
                    time.sleep(0.01)
                self.assertEqual([method for method, _ in calls], ["Register", "CreateSession", "BindShortcuts"])
                self.assertEqual(calls[0][1][0], "io.github.panktrue.ShareFile")
                self.assertEqual(calls[2][1][1][0][1]["preferred_trigger"], "CTRL+ALT+u")
                for identity in ("different-shortcut", hotkeys.SHORTCUT):
                    connection.emit_signal(None, hotkeys.PATH, hotkeys.INTERFACE, "Activated",
                        GLib.Variant("(osta{sv})", (session, identity, 123, {"activation_token": GLib.Variant("s", "test-token")})))
                for _ in range(200):
                    self.spin()
                    if activations:
                        break
                    time.sleep(0.01)
                self.assertEqual(activations, ["test-token"])
                shortcut.stop()
        finally:
            if not shortcut.closed:
                shortcut.stop()
            for registration in registrations:
                connection.unregister_object(registration)
            connection.call_sync("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "ReleaseName",
                GLib.Variant("(s)", (bus,)), None, Gio.DBusCallFlags.NONE, 1000, None)

    def test_batch_clipboard_before_one_capability_free_notification(self):
        notifications = []
        old_notification = self.app.notification
        self.app.notification = lambda title, body: notifications.append((title, body,
            Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD).wait_for_text()))
        try:
            self.app.controller.add_files([str(self.root / "Отчёт 日本語.txt"), str(self.root / "file with spaces.txt")]).result(timeout=5)
            self.wait_idle()
            self.assertEqual(len(notifications), 1)
            title, body, clipboard = notifications[0]
            self.assertEqual(len(clipboard.splitlines()), 2)
            self.assertIn("Ссылки скопированы", body)
            self.assertNotIn("/s/", body)
            self.assertNotIn("gtk-test-only-token", body)
            self.assertFalse(self.app.window.get_visible())
        finally:
            self.app.notification = old_notification

    def test_clipboard_failure_is_reported_and_notification_toggle_keeps_copy(self):
        old_clipboard, old_notification = self.app.clipboard, self.app.notification
        notices, copies = [], []
        try:
            self.app.clipboard = lambda links: False
            self.app.notification = lambda title, body: notices.append(body)
            batch = {"links": ["https://example.test/s/private"], "names": ["name.txt"], "notify": True, "failed": 0}
            self.app.complete(batch)
            self.assertIn("Не удалось скопировать", notices[-1])
            self.app.clipboard = lambda links: copies.extend(links) or True
            self.app.complete({**batch, "notify": False})
            self.assertEqual(len(notices), 1)
            self.assertEqual(copies, batch["links"])
        finally:
            self.app.clipboard, self.app.notification = old_clipboard, old_notification

    def test_remote_relative_unicode_path_resolution_and_uri_rejection(self):
        from sharefile_linux.app import file_paths
        from sharefile_linux.bridge import ClientError
        class Command:
            def create_file_for_arg(inner, argument):
                if argument.startswith("smb://"):
                    return Gio.File.new_for_uri(argument)
                return Gio.File.new_for_path(str(self.root / argument))
        self.assertEqual(file_paths(["Отчёт 日本語.txt"], Command()), [str(self.root / "Отчёт 日本語.txt")])
        with self.assertRaises(ClientError):
            file_paths(["smb://server/file"], Command())

    def test_real_secret_service_roundtrip(self):
        from sharefile_linux.secrets import SecretStore
        origin = "https://gtk-test-" + uuid.uuid4().hex + ".invalid"
        store = SecretStore()
        try:
            store.write(origin, "ephemeral-gtk-test-secret")
            self.assertEqual(store.read(origin), "ephemeral-gtk-test-secret")
        finally:
            store.delete(origin)
        self.assertEqual(store.read(origin), "")

    def test_history_search_copy_open_and_revoke_after_queue_clear(self):
        self.app.controller.add_files([str(self.root / "История 日本語.txt")]).result(timeout=5)
        self.wait_idle()
        self.app.controller.clear_finished().result(timeout=5)
        self.wait_idle()
        self.app.show_history()
        self.assertEqual(self.app.notebook.get_current_page(), 2)
        self.app.history_search.set_text("日本語")
        self.app.refresh_history()
        self.assertEqual(len(self.app.history_rows), 1)
        self.app.history_tree.get_selection().select_path(0)
        identity = self.app.selected_history()
        copies, opened = [], []
        old_clipboard, old_open = self.app.clipboard, self.app.open_url
        try:
            self.app.clipboard = lambda links: copies.extend(links) or True
            self.app.open_url = opened.append
            self.app.copy_history(identity)
            self.wait_idle()
            self.app.open_history(identity)
            self.wait_idle()
            self.assertEqual(copies, opened)
            self.assertEqual(len(copies), 1)
            self.app.controller.revoke_history(identity).result(timeout=5)
            self.wait_idle()
            self.assertEqual(self.app.history_rows[0][3], "Отозвана")
            self.assertFalse(self.app.history_buttons["copy"].get_sensitive())
        finally:
            self.app.clipboard, self.app.open_url = old_clipboard, old_open
            self.app.history_search.set_text("")
            self.app.window.hide()

    def test_desktop_entry_validation_and_real_nautilus_argv(self):
        import importlib.util
        from unittest.mock import patch
        linux = Path(__file__).resolve().parents[1]
        specification = importlib.util.spec_from_file_location("gtk_install", linux / "install.py")
        installer = importlib.util.module_from_spec(specification)
        specification.loader.exec_module(installer)
        source = self.root / "package"
        source.mkdir(exist_ok=True)
        for name in ("sharefile", "sharefile-client", "install.py", "uninstall.sh", "README.md"):
            (source / name).write_text("test")
        (source / "sharefile_linux").mkdir(exist_ok=True)
        (source / "resources").mkdir(exist_ok=True)
        (source / "resources/sharefile.svg").write_text("<svg/>")
        home = self.root / "home with 日本語 quotes ' $ and spaces"
        environment = {"XDG_DATA_HOME": str(home / ".local/share"), "XDG_CONFIG_HOME": str(home / ".config")}
        with patch.dict(os.environ, environment):
            paths = installer.install(source, home, autostart=True, check=False)
            for key in ("desktop", "autostart"):
                subprocess.run(["desktop-file-validate", str(paths[key])], check=True)
            record = self.root / "argv.json"
            runner = paths["app"] / "sharefile"
            runner.write_text("#!/usr/bin/python3\nimport json, sys\nfrom pathlib import Path\nPath(" + repr(str(record)) + ").write_text(json.dumps(sys.argv[1:]))\n")
            runner.chmod(0o755)
            selected = [str(self.root / "space Unicode 日本語.txt"), str(self.root / "newline\nquote'file.txt")]
            subprocess.run([str(paths["nautilus"]), *selected], check=True)
            self.assertEqual(json.loads(record.read_text()), ["--background", "--", *selected])
            record.unlink()
            desktop = Gio.DesktopAppInfo.new_from_filename(str(paths["desktop"]))
            self.assertIsNotNone(desktop)
            desktop.launch([Gio.File.new_for_path(path) for path in selected], None)
            for _ in range(200):
                if record.exists():
                    break
                self.spin()
                time.sleep(0.01)
            self.assertEqual(json.loads(record.read_text()), ["--", *selected])
            installer.uninstall(home, check=False)

    def test_real_dbus_single_instance_background_handoff_and_graceful_quit(self):
        linux = Path(__file__).resolve().parents[1]
        probe = self.root / "probe.py"
        report = self.root / "probe-events.jsonl"
        app_id = "io.github.panktrue.ShareFile.Probe" + uuid.uuid4().hex
        probe.write_text(
            "import json, sys\n"
            "from pathlib import Path\n"
            "sys.path[:0] = " + repr([str(linux), str(linux / "tests")]) + "\n"
            "from sharefile_linux.app import Application\n"
            "from sharefile_linux.controller import Controller\n"
            "from sharefile_linux.state import StateStore\n"
            "from test_client import FakeBridge, MemoryCredentials, OWNER, FOLDER\n"
            "root = Path(" + repr(str(self.root / "probe-state")) + ")\n"
            "root.mkdir(exist_ok=True)\n"
            "helper = root / 'helper'\n"
            "helper.write_text('#!/bin/sh\\nexit 0\\n'); helper.chmod(0o755)\n"
            "store = StateStore(root / 'state.json')\n"
            "if not store.path.exists():\n"
            "    state = store.load()\n"
            "    state['settings'].update(origin='https://example.test', owner=OWNER, folder=FOLDER)\n"
            "    store.save(state)\n"
            "credentials = MemoryCredentials()\n"
            "credentials.write('https://example.test', 'probe-test-only-token')\n"
            "def factory(*args, **kwargs):\n"
            "    return Controller(*args, **kwargs, bridge_factory=FakeBridge)\n"
            "class Probe(Application):\n"
            "    def refresh(self, snapshot):\n"
            "        super().refresh(snapshot)\n"
            "        with open(" + repr(str(report)) + ", 'a') as file:\n"
            "            file.write(json.dumps({'visible': self.window.get_visible(), 'statuses': [j['status'] for j in snapshot['jobs']]}) + '\\n')\n"
            "    def notification(self, title, body):\n"
            "        pass\n"
            "app = Probe(helper, factory, credentials, store, application_id=" + repr(app_id) + ")\n"
            "sys.exit(app.run(sys.argv))\n", encoding="utf-8")
        process = subprocess.Popen([sys.executable, str(probe), "--background"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            for _ in range(200):
                if report.exists():
                    break
                if process.poll() is not None:
                    self.fail("GTK probe failed: " + process.stderr.read().decode())
                time.sleep(0.02)
            self.assertTrue(report.exists())
            selected = [str(self.root / "Два слова 日本語.txt"), str(self.root / "second.txt")]
            subprocess.run([sys.executable, str(probe), "--background", "--", *selected],
                           check=True, capture_output=True, timeout=10)
            for _ in range(200):
                lines = [json.loads(line) for line in report.read_text().splitlines()]
                if any(event["statuses"] == ["done", "done"] for event in lines):
                    break
                time.sleep(0.02)
            self.assertTrue(any(event["statuses"] == ["done", "done"] for event in lines))
            self.assertFalse(any(event["visible"] for event in lines))
            self.assertIsNone(process.poll())
            subprocess.run([sys.executable, str(probe), "--quit"], check=True, capture_output=True, timeout=10)
            self.assertEqual(process.wait(timeout=10), 0)
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
            process.stderr.close()


if __name__ == "__main__":
    unittest.main()
