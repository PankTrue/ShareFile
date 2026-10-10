import copy
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

LINUX = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(LINUX))
from sharefile_linux.bridge import BridgeCall, Cancelled, ClientError
from sharefile_linux.controller import Controller
from sharefile_linux.secrets import SecretStore
from sharefile_linux.state import CHECKPOINT, StateStore

IDENTITY = "a" * 43
OWNER, FOLDER, FILE, SHARE = (letter * 43 for letter in "ofns")


def job(status="queued", batch=IDENTITY, identity=IDENTITY):
    return {"id": identity, "source": "/tmp/Отчёт 日本語.txt", "name": "Отчёт 日本語.txt",
            "size": 5 * 1024 ** 3 + 7, "modified": 1728000000000000000,
            "fingerprint": "f" * 64, "folder": FOLDER, "upload": "", "file_id": "",
            "offset": 0, "status": status, "message": "", "batch": batch, "days": 7,
            "never": False, "share_id": "", "link": ""}


class MemoryCredentials:
    def __init__(self):
        self.values = {}

    def read(self, origin, kind="device"):
        return self.values.get((origin, kind), "")

    def write(self, origin, value, kind="device"):
        self.values[origin, kind] = value

    def delete(self, origin, kind="device"):
        self.values.pop((origin, kind), None)


class FakeBridge:
    requests = []
    share_error = cancel_error = False
    block_upload = None
    started = None
    counter = 0

    def __init__(self, helper):
        self.stop = threading.Event()

    def cancel(self):
        self.stop.set()

    def run(self, request, progress=lambda job: None):
        self.requests.append(copy.deepcopy(request))
        if self.stop.is_set():
            raise Cancelled()
        operation = request["operation"]
        if operation == "session":
            return {"user": {"id": OWNER, "home_id": FOLDER, "username": "tester"}}
        if operation == "folders":
            return [{"id": FOLDER, "name": "Uploads", "kind": "folder", "effective_role": 3}]
        if operation in ("prepare", "prepare_directory"):
            type(self).counter += 1
            value = job(identity=str(self.counter).zfill(43))
            value["source"] = request["source"]
            value["name"] = Path(request["source"]).name
            value = {key: value[key] for key in CHECKPOINT}
            if operation == "prepare_directory":
                value.update(size=0, directory={"folders": [{"path": "empty", "key": "e" * 43, "id": ""}], "files": []})
            return value
        if operation == "upload_directory":
            value = copy.deepcopy(request["directory_job"])
            value["file_id"] = FILE
            value["directory"]["folders"][0]["id"] = "d" * 43
            progress(value)
            # Chunk events can omit the manifest. The adapter must keep its durable IDs.
            progress({key: value[key] for key in CHECKPOINT})
            return value
        if operation == "upload":
            value = copy.deepcopy(request["job"])
            value.update(upload="/api/v1/uploads/" + IDENTITY, file_id=FILE, offset=70000)
            progress(value)
            if self.started:
                self.started.set()
            while self.block_upload is not None and not self.block_upload.is_set():
                if self.stop.wait(0.01):
                    raise Cancelled()
            value["offset"] = value["size"]
            progress(value)
            return value
        if operation == "share":
            if self.share_error:
                raise ClientError("Ответ потерян")
            return {"url": "https://example.test/s/" + SHARE, "share": {"id": SHARE}}
        if operation == "cancel" and self.cancel_error:
            raise ClientError("Отмена не подтверждена")
        return None


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.store = StateStore(Path(self.directory.name) / "state/state.json")
        self.credentials = MemoryCredentials()
        self.credentials.write("https://example.test", "test-device-secret")
        self.completions = []
        FakeBridge.requests = []
        FakeBridge.share_error = FakeBridge.cancel_error = False
        FakeBridge.block_upload = FakeBridge.started = None
        FakeBridge.counter = 0
        self.controller = Controller("unused", self.store, self.credentials,
                                     completed=self.completions.append, bridge_factory=FakeBridge)
        self.controller._attach("https://example.test", "test-device-secret")

    def tearDown(self):
        self.controller.close().result(timeout=5)
        self.directory.cleanup()

    def wait_idle(self):
        for _ in range(500):
            with self.controller._lock:
                if not self.controller._working and not self.controller._pending:
                    return
            time.sleep(0.01)
        self.fail("Queue did not stop")

    def test_batch_completion_and_secret_free_state(self):
        self.controller.add_files(["/tmp/Отчёт 日本語.txt", "/tmp/space quote ' file.txt"]).result(timeout=5)
        self.wait_idle()
        self.assertEqual(len(self.completions), 1)
        self.assertEqual(len(self.completions[0]["links"]), 2)
        self.assertTrue(self.completions[0]["notify"])
        raw = self.store.path.read_text()
        self.assertNotIn("/s/", raw)
        self.assertNotIn("test-device-secret", raw)
        self.assertNotIn('"link"', raw)
        self.assertEqual(self.store.load()["jobs"][0]["size"], 5 * 1024 ** 3 + 7)
        self.assertEqual(stat.S_IMODE(self.store.path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.store.path.parent.stat().st_mode), 0o700)

    def test_disabled_notifications_preserve_completion_links(self):
        self.controller.configure(7, False, "", True, False).result(timeout=5)
        self.controller.add_files(["/tmp/a.txt"]).result(timeout=5)
        self.wait_idle()
        self.assertFalse(self.completions[0]["notify"])
        self.assertTrue(self.completions[0]["links"])

    def test_directory_manifest_survives_compact_progress_and_lost_share(self):
        source = Path(self.directory.name) / "Проект"
        source.mkdir()
        FakeBridge.share_error = True
        self.controller.add_files([str(source)]).result(timeout=5)
        self.wait_idle()
        queued, = self.store.load()["jobs"]
        self.assertEqual(queued["status"], "uncertain")
        self.assertEqual(queued["directory"]["folders"][0]["id"], "d" * 43)
        self.assertEqual(len([request for request in FakeBridge.requests if request["operation"] == "share"]), 1)
        self.controller.resume(queued["id"]).result(timeout=5)
        self.wait_idle()
        shares = [request for request in FakeBridge.requests if request["operation"] == "share"]
        self.assertEqual(len(shares), 1)
        self.assertNotIn("directory", shares[0]["job"])

    def test_pause_durable_resource_and_resume(self):
        FakeBridge.block_upload = threading.Event()
        FakeBridge.started = threading.Event()
        self.controller.add_files(["/tmp/a.txt"]).result(timeout=5)
        self.assertTrue(FakeBridge.started.wait(5))
        identity = self.controller.snapshot()["jobs"][0]["id"]
        self.controller.pause(identity)
        self.wait_idle()
        saved = self.store.load()["jobs"][0]
        self.assertEqual(saved["status"], "paused")
        self.assertEqual(saved["offset"], 70000)
        self.assertEqual(saved["file_id"], FILE)
        FakeBridge.block_upload.set()
        self.controller.resume(identity).result(timeout=5)
        self.wait_idle()
        uploads = [request for request in FakeBridge.requests if request["operation"] == "upload"]
        self.assertEqual(len(uploads), 2)
        self.assertEqual(uploads[1]["job"]["id"], uploads[0]["job"]["id"])
        self.assertEqual(uploads[1]["job"]["offset"], 70000)
        self.assertEqual(len(self.completions), 1)

    def test_lost_share_is_uncertain_and_never_retried_by_resume(self):
        FakeBridge.share_error = True
        self.controller.add_files(["/tmp/a.txt"]).result(timeout=5)
        self.wait_idle()
        item = self.controller.snapshot()["jobs"][0]
        self.assertEqual(item["status"], "uncertain")
        self.assertEqual(self.completions[0]["failed"], 1)
        self.controller.resume(item["id"]).result(timeout=5)
        self.wait_idle()
        self.assertEqual(sum(request["operation"] == "share" for request in FakeBridge.requests), 1)
        FakeBridge.share_error = False
        self.controller.new_link(item["id"]).result(timeout=5)
        self.assertEqual(self.controller.snapshot()["jobs"][0]["status"], "done")

    def test_cancel_failure_preserves_checkpoint_then_confirmed_cancel_removes(self):
        self.controller._commit(lambda state: state["jobs"].append(job("paused")))
        FakeBridge.cancel_error = True
        with self.assertRaises(ClientError):
            self.controller.remove(IDENTITY).result(timeout=5)
        self.assertEqual(len(self.store.load()["jobs"]), 1)
        self.assertEqual(self.store.load()["jobs"][0]["status"], "paused")
        FakeBridge.cancel_error = False
        self.controller.remove(IDENTITY).result(timeout=5)
        self.assertEqual(self.store.load()["jobs"], [])

    def test_account_isolation_and_restart_recovery(self):
        self.controller._commit(lambda state: state["jobs"].extend([job("sharing"), job("uploading", identity="b" * 43)]))
        recovered = self.store.load()
        self.assertEqual([item["status"] for item in recovered["jobs"]], ["uncertain", "paused"])
        with self.assertRaises(ClientError):
            self.controller._attach("https://other.test", "another-token")
        self.assertEqual(self.controller.snapshot()["settings"]["origin"], "https://example.test")

    def test_failed_checkpoint_stops_upload_before_share(self):
        original = self.store.save
        def fail_after_resource(state):
            if any(item["upload"] for item in state["jobs"]):
                raise OSError("disk full")
            original(state)
        self.store.save = fail_after_resource
        self.controller.add_files(["/tmp/a.txt"]).result(timeout=5)
        self.wait_idle()
        self.assertFalse(any(request["operation"] == "share" for request in FakeBridge.requests))
        self.assertEqual(self.store.load()["jobs"][0]["status"], "error")
        self.store.save = original

    def test_revoke_clears_memory_url_and_persisted_share_id(self):
        self.controller.add_files(["/tmp/a.txt"]).result(timeout=5)
        self.wait_idle()
        identity = self.controller.snapshot()["jobs"][0]["id"]
        self.controller.revoke(identity).result(timeout=5)
        self.assertFalse(self.controller.snapshot()["jobs"][0]["link"])
        self.assertFalse(self.store.load()["jobs"][0]["share_id"])

    def test_restored_done_jobs_do_not_copy_empty_links(self):
        item = job("done")
        item.update(file_id=FILE, share_id=SHARE)
        self.controller._commit(lambda state: state["jobs"].append(item))
        self.controller._finish_batch(IDENTITY)
        self.assertEqual(self.completions, [])

    def test_shutdown_still_terminates_when_disk_is_full(self):
        FakeBridge.block_upload = threading.Event()
        FakeBridge.started = threading.Event()
        self.controller.add_files(["/tmp/a.txt"]).result(timeout=5)
        self.assertTrue(FakeBridge.started.wait(5))
        original = self.store.save
        def fail(state):
            raise OSError("disk full")
        self.store.save = fail
        self.controller.close().result(timeout=5)
        self.assertFalse(self.controller.snapshot()["active_id"])
        self.store.save = original


class StateTests(unittest.TestCase):
    def test_corrupt_state_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            for value in ('{"version": 99}', '{"broken"', 'null'):
                path.write_text(value)
                with self.assertRaises(ClientError):
                    StateStore(path).load()
                self.assertEqual(path.read_text(), value)

    def test_invalid_upload_path_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            store = StateStore(Path(directory) / "state.json")
            state = store.load()
            item = job()
            item["upload"] = "https://attacker.test/api/v1/uploads/" + IDENTITY
            state["jobs"] = [item]
            store.save(state)
            with self.assertRaises(ClientError):
                store.load()


class BridgeTests(unittest.TestCase):
    def helper(self, directory, code):
        script = Path(directory) / "helper"
        script.write_text("#!" + sys.executable + "\n" + code)
        script.chmod(0o755)
        return script

    def test_progress_and_final_event_in_one_write(self):
        with tempfile.TemporaryDirectory() as directory:
            helper = self.helper(directory, 'import sys\nsys.stdin.readline()\nsys.stdout.write(\'{"type":"progress","job":{"offset":1}}\\n{"type":"result","result":{"ok":true}}\\n\')\n')
            events = []
            result = BridgeCall(helper).run({"token": "stdin-only"}, events.append)
            self.assertTrue(result["ok"])
            self.assertEqual(events, [{"offset": 1, "transferred": 0}])

    def test_cancel_terminates_blocked_process(self):
        with tempfile.TemporaryDirectory() as directory:
            helper = self.helper(directory, "import sys, time\nsys.stdin.readline()\ntime.sleep(60)\n")
            call = BridgeCall(helper)
            timer = threading.Timer(0.1, call.cancel)
            timer.start()
            try:
                with self.assertRaises(Cancelled):
                    call.run({})
            finally:
                timer.cancel()

    def test_secret_tool_receives_secrets_only_on_stdin(self):
        with patch("sharefile_linux.secrets.shutil.which", return_value="/usr/bin/secret-tool"), patch("sharefile_linux.secrets.subprocess.run") as run:
            run.return_value.returncode = 0
            run.return_value.stdout = ""
            SecretStore().write("https://example.test", "very-private-password")
            arguments, kwargs = run.call_args
            self.assertNotIn("very-private-password", repr(arguments))
            self.assertEqual(kwargs["input"], "very-private-password")

    def test_missing_secret_service_has_no_file_fallback(self):
        with patch("sharefile_linux.secrets.shutil.which", return_value=None):
            with self.assertRaises(ClientError):
                SecretStore().write("https://example.test", "private")


class InstallerTests(unittest.TestCase):
    def test_install_handoff_and_uninstall_preserve_state(self):
        specification = importlib.util.spec_from_file_location("linux_install", LINUX / "install.py")
        installer = importlib.util.module_from_spec(specification)
        specification.loader.exec_module(installer)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home = root / "Home 日本語 with ' quotes $ and spaces"
            source = root / "package"
            source.mkdir()
            for name in ("sharefile", "sharefile-client", "install.py", "uninstall.sh", "README.md"):
                (source / name).write_text("test")
            (source / "sharefile_linux").mkdir()
            (source / "resources").mkdir()
            (source / "resources/sharefile.svg").write_text("<svg/>")
            environment = {"XDG_DATA_HOME": str(home / ".local/share"), "XDG_CONFIG_HOME": str(home / ".config")}
            with patch.dict(os.environ, environment):
                paths = installer.install(source, home, autostart=True, check=False)
                self.assertTrue(paths["launcher"].exists())
                self.assertIn('"$@"', paths["nautilus"].read_text())
                self.assertNotIn("NAUTILUS_SCRIPT_SELECTED_FILE_PATHS", paths["nautilus"].read_text())
                self.assertIn("--background -- %F", paths["dolphin"].read_text())
                self.assertIn("inode/directory;", paths["dolphin"].read_text())
                self.assertIn("Extensions=any;", paths["nemo"].read_text())
                state = home / ".local/state/sharefile/state.json"
                state.parent.mkdir(parents=True)
                state.write_text("preserved")
                installer.uninstall(home, check=False)
                self.assertEqual(state.read_text(), "preserved")
                self.assertFalse(any(path.exists() for path in paths.values()))


if __name__ == "__main__":
    unittest.main()
