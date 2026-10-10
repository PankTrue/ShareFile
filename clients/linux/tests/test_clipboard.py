import base64
import copy
from pathlib import Path
import struct
import sys
import tempfile
import threading
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sharefile_linux.clipboard import ClipboardImages
from sharefile_linux.bridge import ClientError
from sharefile_linux.controller import Controller
from sharefile_linux.state import StateStore
from test_client import FakeBridge, MemoryCredentials, OWNER, FOLDER

PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")


class ClipboardTests(unittest.TestCase):
    def test_private_unique_sources_and_cleanup_never_deletes_user_files(self):
        with tempfile.TemporaryDirectory() as root:
            spool = ClipboardImages(Path(root) / "clipboard")
            first, second = spool.write(PNG), spool.write(PNG)
            self.assertNotEqual(first, second)
            self.assertEqual(Path(first).read_bytes(), PNG)
            self.assertEqual(Path(first).stat().st_mode & 0o777, 0o600)
            self.assertEqual(spool.directory.stat().st_mode & 0o777, 0o700)
            spool.reconcile([])  # A concurrent queue checkpoint must retain sources still being prepared.
            self.assertTrue(Path(first).exists())
            self.assertTrue(Path(second).exists())
            spool.release(first)
            spool.release(second)
            user = Path(root) / "Screenshot-personal.png"
            user.write_bytes(PNG)
            spool.remove(str(user))
            spool.reconcile([{"source": first, "status": "paused"}])
            self.assertTrue(Path(first).exists())
            self.assertFalse(Path(second).exists())
            spool.reconcile([{"source": first, "status": "done"}])
            self.assertFalse(Path(first).exists())
            self.assertTrue(user.exists())

    def test_non_image_and_oversized_dimensions_are_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            spool = ClipboardImages(Path(root) / "clipboard")
            for value in (b"https://example.test", PNG[:10], PNG[:16] + struct.pack(">II", 100000, 100000) + PNG[24:]):
                with self.assertRaises(ClientError):
                    spool.write(value)
            self.assertFalse(spool.directory.exists())

    def test_paused_clipboard_source_survives_restart_and_is_cleaned_after_resume(self):
        with tempfile.TemporaryDirectory() as root:
            store = StateStore(Path(root) / "state.json")
            state = store.load()
            state["settings"].update(origin="https://example.test", owner=OWNER, folder=FOLDER)
            store.save(state)
            credentials = MemoryCredentials()
            credentials.write("https://example.test", "disposable-test-token")
            FakeBridge.counter = 0
            FakeBridge.requests = []
            FakeBridge.share_error = False
            FakeBridge.block_upload = threading.Event()
            FakeBridge.started = threading.Event()
            completions = []
            controller = Controller("unused", store, credentials, bridge_factory=FakeBridge)
            restarted = None
            try:
                controller.restore().result(timeout=5)
                controller.add_clipboard(PNG).result(timeout=5)
                self.assertTrue(FakeBridge.started.wait(5))
                selected = controller.snapshot()["jobs"][0]
                source = Path(selected["source"])
                controller.close().result(timeout=5)
                self.assertTrue(source.exists())
                FakeBridge.block_upload = None
                restarted = Controller("unused", store, credentials, completed=completions.append, bridge_factory=FakeBridge)
                self.assertEqual(restarted.snapshot()["jobs"][0]["status"], "paused")
                restarted.restore().result(timeout=5)
                restarted.resume(selected["id"]).result(timeout=5)
                import time
                for _ in range(500):
                    if completions:
                        break
                    time.sleep(0.01)
                self.assertEqual(len(completions), 1)
                self.assertEqual(len(completions[0]["links"]), 1)
                self.assertFalse(source.exists())
                self.assertEqual(len([r for r in FakeBridge.requests if r["operation"] == "share"]), 1)
            finally:
                FakeBridge.block_upload = FakeBridge.started = None
                controller.close().result(timeout=5)
                if restarted:
                    restarted.close().result(timeout=5)
