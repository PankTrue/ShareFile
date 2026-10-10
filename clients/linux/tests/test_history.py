import json
from pathlib import Path
import stat
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sharefile_linux.bridge import ClientError
from sharefile_linux.history import HistoryStore, LIMIT
import test_client as fixtures
from test_client import MemoryCredentials, job, OWNER, SHARE, FakeBridge


class HistoryTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.path = Path(self.directory.name) / "state/history.json"
        self.credentials = MemoryCredentials()
        self.history = HistoryStore(self.path, self.credentials)

    def tearDown(self):
        self.directory.cleanup()

    def add(self, identity=SHARE, origin="https://example.test", owner=OWNER, **fields):
        value = job()
        value.update(fields)
        url = origin + "/s/" + identity
        self.history.add(origin, owner, value, {"url": url, "share": {"id": identity}})
        return url

    def test_restart_secret_free_metadata_scope_expiry_and_revocation(self):
        url = self.add()
        entry = self.history.entries[0]
        self.assertEqual(self.history.link(entry), url)
        raw = self.path.read_text()
        self.assertNotIn("/s/", raw)
        self.assertEqual(stat.S_IMODE(self.path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.path.parent.stat().st_mode), 0o700)
        restarted = HistoryStore(self.path, self.credentials)
        restarted.load()
        self.assertEqual(restarted.link(restarted.entries[0]), url)
        self.assertEqual(restarted.for_account("https://another.test", OWNER), [])
        self.assertEqual(restarted.for_account("https://example.test", "z" * 43), [])
        restarted.revoked(restarted.entries[0])
        self.assertFalse(self.credentials.values)
        again = HistoryStore(self.path, self.credentials)
        again.load()
        self.assertTrue(again.entries[0]["revoked"])
        with self.assertRaises(ClientError):
            again.link(again.entries[0])

    def test_new_links_are_preserved_independently_and_retention_removes_secrets(self):
        for number in range(LIMIT + 1):
            self.add(identity=str(number).zfill(43))
        self.assertEqual(len(self.history.entries), LIMIT)
        self.assertEqual(len(self.credentials.values), LIMIT)
        self.assertEqual(self.history.entries[0]["share_id"], str(LIMIT).zfill(43))
        self.assertNotIn(("https://example.test", "history:" + OWNER + ":" + "0" * 43), self.credentials.values)

    def test_expired_missing_and_unsafe_credentials_are_not_opened(self):
        self.add(days=-1)
        with self.assertRaises(ClientError):
            self.history.link(self.history.entries[0])
        self.add(identity="b" * 43, never=True)
        entry = self.history.entries[0]
        self.credentials.delete(entry["origin"], self.history.key(entry))
        with self.assertRaises(ClientError):
            self.history.link(entry)
        for value in ("file:///tmp/secret", "https://evil.test/s/" + SHARE + "?credential=bad", "javascript:alert(1)"):
            self.credentials.write(entry["origin"], value, self.history.key(entry))
            with self.assertRaises(ClientError):
                self.history.link(entry)

    def test_corrupt_index_is_preserved_and_never_overwritten(self):
        self.path.parent.mkdir()
        self.path.write_text("corrupted index")
        with self.assertRaises(ClientError):
            self.history.load()
        with self.assertRaises(ClientError):
            self.add()
        self.assertEqual(self.path.read_text(), "corrupted index")
        self.assertEqual(self.credentials.values, {})

    def test_same_share_id_on_another_account_does_not_replace_capability(self):
        self.add()
        self.add(owner="z" * 43)
        self.assertEqual(len(self.history.entries), 2)
        self.assertEqual(len(self.credentials.values), 2)

    def test_index_write_failure_does_not_leave_a_new_credential(self):
        self.path.mkdir(parents=True)  # A directory cannot be replaced by the index file.
        with self.assertRaises(OSError):
            self.add()
        self.assertEqual(self.history.entries, [])
        self.assertEqual(self.credentials.values, {})


class HistoryControllerTests(unittest.TestCase):
    setUp = fixtures.ControllerTests.setUp
    tearDown = fixtures.ControllerTests.tearDown
    wait_idle = fixtures.ControllerTests.wait_idle
    def test_history_survives_clear_and_restart_and_revoke_without_queue(self):
        self.controller.add_files(["/tmp/history.txt"]).result(5)
        self.wait_idle()
        entry, = self.controller.snapshot()["history"]
        self.controller.clear_finished().result(5)
        self.assertEqual(self.store.load()["jobs"], [])
        result = []
        self.controller.history_link(entry["share_id"], result.append).result(5)
        self.assertEqual(result, ["https://example.test/s/" + SHARE])
        self.controller.history.load()
        self.controller.revoke_history(entry["share_id"]).result(5)
        self.assertTrue(self.controller.snapshot()["history"][0]["revoked"])

    def test_failed_revoke_keeps_history_and_local_links(self):
        self.controller.add_files(["/tmp/history.txt"]).result(5)
        self.wait_idle()
        entry, = self.controller.snapshot()["history"]
        request = self.controller._request
        def failing(operation, **extra):
            if operation == "revoke_share":
                raise ClientError("Сервер недоступен")
            return request(operation, **extra)
        self.controller._request = failing
        with self.assertRaises(ClientError):
            self.controller.revoke_history(entry["share_id"]).result(5)
        self.assertFalse(self.controller.snapshot()["history"][0]["revoked"])
        self.assertTrue(self.controller.snapshot()["jobs"][0]["link"])

    def test_secret_store_failure_does_not_retry_share_or_mark_success_uncertain(self):
        with patch.object(self.credentials, "write", side_effect=ClientError("Keyring заблокирован")):
            self.controller.add_files(["/tmp/history.txt"]).result(5)
            self.wait_idle()
        item, = self.controller.snapshot()["jobs"]
        self.assertEqual(item["status"], "done")
        self.assertTrue(item["link"])
        self.assertIn("not saved", item["message"])
        self.assertEqual(len([r for r in FakeBridge.requests if r["operation"] == "share"]), 1)
        self.assertEqual(self.controller.snapshot()["history"], [])

    def test_locked_keyring_does_not_block_ui_snapshots(self):
        started, released = threading.Event(), threading.Event()
        original = self.credentials.write
        def blocked(origin, value, kind="device"):
            started.set()
            released.wait(5)
            original(origin, value, kind)
        snapshots = []
        with patch.object(self.credentials, "write", side_effect=blocked):
            self.controller.add_files(["/tmp/history.txt"]).result(5)
            self.assertTrue(started.wait(5))
            reader = threading.Thread(target=lambda: snapshots.append(self.controller.snapshot()))
            try:
                reader.start()
                reader.join(1)
                self.assertFalse(reader.is_alive(), "UI snapshot waited on a keyring unlock")
            finally:
                released.set()
                reader.join(5)
                self.wait_idle()
        self.assertEqual(snapshots[0]["jobs"][0]["status"], "sharing")

