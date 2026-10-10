import ast
import json
from pathlib import Path
import re
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sharefile_linux.localization import ENGLISH, normalize, set_language, tr
from sharefile_linux.state import StateStore
from sharefile_linux.controller import Controller
from test_client import MemoryCredentials, FakeBridge, job


class LocalizationTests(unittest.TestCase):
    def tearDown(self):
        set_language("en")

    def test_catalog_is_complete_and_preserves_format_arguments(self):
        slots = re.compile(r"\{\d+(?::[^}]+)?\}")
        for source, english in ENGLISH.items():
            self.assertTrue(english.strip(), source)
            self.assertFalse(re.search("[А-Яа-яЁё]", english), source)
            self.assertEqual(sorted(slots.findall(source)), sorted(slots.findall(english)), source)
        self.assertEqual(normalize("de"), "en")
        self.assertEqual(normalize(None), "en")

    def test_every_native_ui_translation_key_exists_in_the_shared_catalog(self):
        clients = Path(__file__).resolve().parents[2]
        for path in (clients / "linux/sharefile_linux").glob("*.py"):
            for node in ast.walk(ast.parse(path.read_text())):
                if (isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "tr"
                        and node.args and isinstance(node.args[0], ast.Constant)):
                    self.assertIn(node.args[0].value, ENGLISH, str(path))
        pattern = re.compile(r'(?:L|Localization.Text)\("((?:[^"\\]|\\.)*)"')
        for folder, extension in (("macos/Sources", "*.swift"), ("windows/src", "*.cs")):
            for path in (clients / folder).rglob(extension):
                if path.name in ("Localization.swift", "Localization.cs"):
                    continue
                for key in pattern.findall(path.read_text()):
                    self.assertTrue(json.loads('"' + key + '"') in ENGLISH, str(path) + ": " + key)
        keys = json.loads((clients / "windows/src/ShareFile.Windows/LocalizationKeys.json").read_text())
        for key in keys.values():
            self.assertIn(key, ENGLISH)

    def test_language_switch_translates_status_notifications_and_dynamic_errors(self):
        set_language("en")
        self.assertEqual(tr("Настройки"), "Settings")
        self.assertEqual(tr("Загружено объектов: {0}", 2), "Items uploaded: 2")
        self.assertEqual(tr("Каталог содержит специальный файл: /tmp/Файл.txt"),
                         "The folder contains a special file: /tmp/Файл.txt")
        set_language("ru")
        self.assertEqual(tr("Settings"), "Настройки")
        self.assertEqual(tr("Items uploaded: 2"), "Загружено объектов: 2")
        self.assertEqual(tr("Загрузка завершена"), "Загрузка завершена")
        # Substitutions are data, including placeholder-looking filenames.
        set_language("en")
        self.assertEqual(tr("{0} из {1} · {2}", "{1}", "файл", "{0}"), "{1} of файл · {0}")

    def test_legacy_default_restart_and_switch_preserve_queue_and_link_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            store = StateStore(Path(directory) / "state.json")
            state = store.load()
            state["settings"].update(days=30, never=True, notify_uploads=False)
            state["jobs"] = [job(status="paused")]
            store.save(state)
            legacy = json.loads(store.path.read_text())
            del legacy["settings"]["language"]
            store.path.write_text(json.dumps(legacy))
            controller = Controller("unused", store, MemoryCredentials(), bridge_factory=FakeBridge)
            try:
                self.assertEqual(controller.snapshot()["settings"]["language"], "en")
                before = controller.snapshot()
                controller.set_language("ru")
                after = controller.snapshot()
                self.assertEqual(after["settings"]["language"], "ru")
                self.assertEqual(after["jobs"], before["jobs"])
                self.assertEqual(after["settings"]["days"], 30)
                self.assertFalse(after["settings"]["notify_uploads"])
                self.assertEqual(store.load()["settings"]["language"], "ru")
                self.assertEqual(tr("Настройки"), "Настройки")
                controller.set_language("unsupported")
                self.assertEqual(store.load()["settings"]["language"], "en")
            finally:
                controller.close().result(timeout=5)
