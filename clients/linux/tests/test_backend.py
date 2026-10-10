"""Real backend and JSON bridge, isolated data and no user's credentials."""
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import signal
import socket
import sys
import tempfile
import time
import unittest
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sharefile_linux.bridge import BridgeCall, ClientError
from sharefile_linux.controller import Controller
from sharefile_linux.state import CHECKPOINT, StateStore
from test_client import MemoryCredentials
import subprocess


def free_port():
    with socket.socket() as connection:
        connection.bind(("127.0.0.1", 0))
        return connection.getsockname()[1]


@unittest.skipUnless(os.environ.get("SHAREFILE_BACKEND") and os.environ.get("SHAREFILE_HELPER"), "run make linux-smoke")
class BackendTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="sharefile-linux-smoke-")
        self.root = Path(self.directory.name)
        self.process = self.controller = None
        self.port, admin = free_port(), free_port()
        while admin == self.port:
            admin = free_port()
        self.origin = f"http://127.0.0.1:{self.port}"
        self.backend = os.environ["SHAREFILE_BACKEND"]
        self.helper = os.environ["SHAREFILE_HELPER"]
        self.config = self.root / "config.json"
        self.config.write_text(json.dumps({"data_dir": str(self.root / "storage"), "volume_id": "linux-test",
            "public_addr": f"127.0.0.1:{self.port}", "admin_addr": f"127.0.0.1:{admin}",
            "public_url": self.origin, "admin_url": f"http://127.0.0.1:{admin}",
            "insecure_local": True, "reserve_bytes": 0, "chunk_bytes": 1048576}))
        self.cli("init-storage")
        self.cli("admin-create", ["-username", "linux-test", "-password-stdin"], "linux-test-only-password\n")
        self.start_backend()
        code, headers, body = self.http("POST", "/api/v1/auth/login", {"username": "linux-test", "password": "linux-test-only-password"})
        self.assertEqual(code, 200)
        self.auth = {"Cookie": headers["set-cookie"].split(";")[0], "X-CSRF-Token": json.loads(body)["csrf_token"]}
        self.token = ""
        pairing = self.call("pair_start", name="Disposable Linux test")
        self.assertEqual(self.http("POST", "/api/v1/device-requests/" + pairing["request"]["id"] + "/approve", headers=self.auth)[0], 204)
        self.token = self.call("pair_poll", request_id=pairing["request"]["id"], secret=pairing["request"]["secret"])["token"]
        session = self.call("session")["user"]
        self.folder = next(folder for folder in self.call("folders", parent=session["home_id"]) if folder["name"] == "Uploads")["id"]
        self.credentials = MemoryCredentials()
        self.credentials.write(self.origin, self.token)
        self.completions = []
        self.store = StateStore(self.root / "client/state.json")

    def tearDown(self):
        if self.controller:
            self.controller.close().result(timeout=10)
        if self.process:
            self.stop_backend()
        self.directory.cleanup()

    def cli(self, command, extra=(), input_text=None):
        subprocess.run([self.backend, command, "-config", str(self.config), *extra], input=input_text,
                       text=True, check=True, capture_output=True, timeout=30)

    def http(self, method, path, body=None, headers=None):
        if isinstance(body, dict):
            body = json.dumps(body).encode()
            headers = {**(headers or {}), "Content-Type": "application/json"}
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=30)
        try:
            connection.request(method, path, body=body, headers=headers or {})
            response = connection.getresponse()
            return response.status, {key.lower(): value for key, value in response.getheaders()}, response.read()
        finally:
            connection.close()

    def start_backend(self):
        self.process = subprocess.Popen([self.backend, "serve", "-config", str(self.config)],
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(200):
            try:
                if self.http("GET", "/readyz")[0] == 200:
                    return
            except OSError:
                pass
            if self.process.poll() is not None:
                self.fail("Disposable backend failed to start")
            time.sleep(0.05)
        self.fail("Backend startup timed out")

    def stop_backend(self):
        self.process.send_signal(signal.SIGTERM)
        self.process.wait(timeout=35)
        self.assertEqual(self.process.returncode, 0)
        self.process = None

    def call(self, operation, **extra):
        return BridgeCall(self.helper).run({"operation": operation, "origin": self.origin, "token": self.token, **extra})

    def new_controller(self):
        self.controller = Controller(self.helper, self.store, self.credentials, completed=self.completions.append)
        if self.controller.snapshot()["settings"]["origin"]:
            self.controller.restore().result(timeout=10)
        else:
            self.controller._attach(self.origin, self.token)
        return self.controller

    def wait_idle(self):
        for _ in range(1200):
            snapshot = self.controller.snapshot()
            if not snapshot["active_id"] and not snapshot["busy"]:
                return
            time.sleep(0.05)
        self.fail("Desktop queue timed out")

    def test_clipboard_png_upload_download_link_and_cleanup(self):
        from test_clipboard import PNG
        controller = self.new_controller()
        controller.add_clipboard(PNG).result(timeout=20)
        self.wait_idle()
        job, = controller.snapshot()["jobs"]
        self.assertEqual(job["status"], "done")
        self.assertTrue(job["name"].startswith("Screenshot-"))
        self.assertTrue(job["name"].endswith(".png"))
        code, headers, downloaded = self.http("GET", urlsplit(job["link"]).path)
        self.assertEqual(code, 200)
        self.assertEqual(downloaded, PNG)
        self.assertFalse(Path(job["source"]).exists())
        self.assertEqual(len(self.completions), 1)
        self.assertEqual(self.completions[0]["links"], [job["link"]])

    def test_upload_batch_hash_empty_password_revoke_and_device_revocation(self):
        controller = self.new_controller()
        source = self.root / "Отчёт с пробелами 日本語.bin"
        payload = os.urandom(2 * 1048576 + 137)
        source.write_bytes(payload)
        empty = self.root / "пустой файл.txt"
        empty.touch()
        controller.add_files([str(source), str(empty)]).result(timeout=20)
        self.wait_idle()
        snapshot = controller.snapshot()
        self.assertEqual([job["status"] for job in snapshot["jobs"]], ["done", "done"])
        self.assertEqual(len(self.completions), 1)
        self.assertEqual(len(self.completions[0]["links"]), 2)
        for job, expected in zip(snapshot["jobs"], (payload, b"")):
            code, _, downloaded = self.http("GET", urlsplit(job["link"]).path)
            self.assertEqual(code, 200)
            self.assertEqual(hashlib.sha256(downloaded).digest(), hashlib.sha256(expected).digest())
        raw = self.store.path.read_text()
        self.assertNotIn(self.token, raw)
        self.assertNotIn("/s/", raw)
        first = snapshot["jobs"][0]
        controller.configure(7, False, "linux-link-only-password", True, False).result(timeout=5)
        controller.new_link(first["id"]).result(timeout=10)
        protected = controller.snapshot()["jobs"][0]["link"]
        self.assertEqual(self.http("GET", urlsplit(protected).path)[0], 401)
        self.assertNotIn("linux-link-only-password", self.store.path.read_text())
        controller.revoke(first["id"]).result(timeout=10)
        self.assertEqual(self.http("GET", urlsplit(protected).path)[0], 404)
        # Original capabilities survive new-link creation, clearing the queue and restart.
        self.assertEqual(len(controller.snapshot()["history"]), 3)
        self.assertTrue(controller.snapshot()["history"][0]["revoked"])
        controller.clear_finished().result(timeout=5)
        controller.close().result(timeout=5)
        controller = self.new_controller()
        self.assertEqual(controller.snapshot()["jobs"], [])
        restored = []
        controller.history_link(first["share_id"], restored.append).result(timeout=5)
        self.assertEqual(restored, [first["link"]])
        self.assertEqual(self.http("GET", urlsplit(restored[0]).path)[0], 200)
        controller.revoke_history(first["share_id"]).result(timeout=5)
        self.assertEqual(self.http("GET", urlsplit(restored[0]).path)[0], 404)
        self.assertNotIn("/s/", controller.history.path.read_text())
        devices = json.loads(self.http("GET", "/api/v1/devices", headers=self.auth)[2])
        self.assertEqual(self.http("DELETE", "/api/v1/devices/" + devices[0]["id"], headers=self.auth)[0], 204)
        with self.assertRaises(ClientError) as error:
            self.call("session")
        self.assertNotIn(self.token, str(error.exception))

    def test_interrupted_socket_restart_and_idempotent_queue_resume(self):
        source = self.root / "resume 日本語.bin"
        payload = os.urandom(2 * 1048576 + 137)
        source.write_bytes(payload)
        prepared = self.call("prepare", source=str(source), parent=self.folder)
        tus = {"Authorization": "Bearer " + self.token, "Tus-Resumable": "1.0.0",
               "Upload-Length": str(len(payload)), "Idempotency-Key": prepared["id"],
               "Upload-Metadata": "filename " + base64.b64encode(source.name.encode()).decode()
               + ",folder_id " + base64.b64encode(self.folder.encode()).decode()}
        code, headers, _ = self.http("POST", "/api/v1/uploads", headers=tus)
        self.assertEqual(code, 201)
        file_id = headers["sharefile-file-id"]
        path = urlsplit(headers["location"]).path
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        connection.connect()
        connection.putrequest("POST", path)
        for key, value in {"Authorization": "Bearer " + self.token, "Tus-Resumable": "1.0.0",
                           "Upload-Offset": "0", "X-HTTP-Method-Override": "PATCH",
                           "Content-Type": "application/offset+octet-stream", "Content-Length": "1000000"}.items():
            connection.putheader(key, value)
        connection.endheaders()
        connection.send(payload[:70000])
        connection.close()
        for _ in range(100):
            durable = self.http("HEAD", path, headers={"Authorization": "Bearer " + self.token, "Tus-Resumable": "1.0.0"})
            if durable[1].get("upload-offset") == "70000":
                break
            time.sleep(0.02)
        self.assertEqual(durable[1]["upload-offset"], "70000")
        controller = self.new_controller()
        # Simulate a lost upload-create response: the persisted job has no resource path.
        prepared.update(status="uploading", message="", batch=prepared["id"], days=7, never=False, share_id="", link="")
        controller._commit(lambda state: state["jobs"].append(prepared))
        controller.close().result(timeout=10)
        self.controller = None
        self.stop_backend()
        self.start_backend()
        controller = self.new_controller()
        self.assertEqual(controller.snapshot()["jobs"][0]["status"], "paused")
        controller.resume(prepared["id"]).result(timeout=5)
        self.wait_idle()
        result = controller.snapshot()["jobs"][0]
        self.assertEqual(result["status"], "done")
        self.assertEqual(result["file_id"], file_id)
        self.assertEqual(result["offset"], len(payload))
        downloaded = self.http("GET", urlsplit(result["link"]).path)[2]
        self.assertEqual(hashlib.sha256(downloaded).digest(), hashlib.sha256(payload).digest())
        children = json.loads(self.http("GET", "/api/v1/folders?parent_id=" + self.folder, headers={"Authorization": "Bearer " + self.token})[2])
        self.assertEqual(len(children), 1)

    def directory_source(self):
        source = self.root / "Проект 日本語"
        (source / "a").mkdir(parents=True)
        (source / "b").mkdir()
        (source / "empty/nested").mkdir(parents=True)
        (source / "a/report.txt").write_bytes(os.urandom(1048576 + 131))
        (source / "b/report.txt").write_bytes(b"different report")
        (source / "zero.txt").write_bytes(b"")
        return source

    def check_directory(self, result, source):
        self.assertEqual(result["status"], "done")
        self.assertEqual(result["offset"], result["size"])
        self.assertEqual(self.http("GET", urlsplit(result["link"]).path)[0], 200)
        for entry in result["directory"]["files"]:
            self.assertTrue(entry["complete"])
            code, _, body = self.http("GET", urlsplit(result["link"]).path + "/nodes/" + entry["job"]["file_id"])
            self.assertEqual(code, 200)
            self.assertEqual(hashlib.sha256(body).digest(), hashlib.sha256((source / entry["path"]).read_bytes()).digest())
        ids = {"": result["file_id"]}
        for folder in result["directory"]["folders"]:
            ids[folder["path"]] = folder["id"]
            node = json.loads(self.http("GET", "/api/v1/nodes/" + folder["id"], headers=self.auth)[2])
            self.assertEqual(node["parent_id"], ids[folder["path"].rpartition("/")[0]])
        self.assertIn("empty/nested", ids)
        self.assertEqual(json.loads(self.http("GET", "/api/v1/folders?parent_id=" + ids["empty/nested"], headers=self.auth)[2]), [])

    def test_directory_is_one_queue_item_link_and_completion_with_empty_folders(self):
        source = self.directory_source()
        controller = self.new_controller()
        controller.add_files([str(source)]).result(timeout=20)
        self.wait_idle()
        result, = controller.snapshot()["jobs"]
        self.check_directory(result, source)
        completion, = self.completions
        self.assertEqual(len(completion["links"]), 1)
        saved, = self.store.load()["jobs"]
        self.assertEqual(saved["directory"], result["directory"])
        self.assertNotIn(self.token, self.store.path.read_text())
        self.assertNotIn("/s/", self.store.path.read_text())

    def test_directory_lost_creates_and_restart_resume_keep_original_ids(self):
        source = self.directory_source()
        prepared = self.call("prepare_directory", source=str(source), parent=self.folder)
        # The backend accepts requests; the client never receives these checkpoints.
        def create(parent, name, key):
            headers = {**self.auth, "Idempotency-Key": key}
            code, _, body = self.http("POST", "/api/v1/folders", {"parent_id": parent, "name": name}, headers)
            self.assertEqual(code, 201)
            return json.loads(body)["id"]
        ids = {"": create(self.folder, source.name, prepared["id"])}
        for folder in prepared["directory"]["folders"]:
            ids[folder["path"]] = create(ids[folder["path"].rpartition("/")[0]], folder["path"].split("/")[-1], folder["key"])
        child = prepared["directory"]["files"][0]
        job = child["job"]
        tus = {"Authorization": "Bearer " + self.token, "Tus-Resumable": "1.0.0",
               "Upload-Length": str(job["size"]), "Idempotency-Key": job["id"],
               "Upload-Metadata": "filename " + base64.b64encode(job["name"].encode()).decode()
               + ",folder_id " + base64.b64encode(ids[child["path"].rpartition("/")[0]].encode()).decode()}
        code, response, _ = self.http("POST", "/api/v1/uploads", headers=tus)
        self.assertEqual(code, 201)
        file_id = response["sharefile-file-id"]
        patch = {"Authorization": "Bearer " + self.token, "Tus-Resumable": "1.0.0", "Upload-Offset": "0",
                 "X-HTTP-Method-Override": "PATCH", "Content-Type": "application/offset+octet-stream"}
        self.assertEqual(self.http("POST", urlsplit(response["location"]).path, (source / child["path"]).read_bytes()[:70000], patch)[0], 204)
        controller = self.new_controller()
        prepared.update(status="uploading", message="", batch=prepared["id"], days=7, never=False, share_id="", link="")
        controller._commit(lambda state: state["jobs"].append(prepared))
        controller.close().result(timeout=10)
        self.controller = None
        self.stop_backend()
        self.start_backend()
        controller = self.new_controller()
        self.assertEqual(controller.snapshot()["jobs"][0]["status"], "paused")
        controller.resume(prepared["id"]).result(timeout=5)
        self.wait_idle()
        result, = controller.snapshot()["jobs"]
        self.check_directory(result, source)
        self.assertEqual(result["file_id"], ids[""])
        self.assertEqual(result["directory"]["files"][0]["job"]["file_id"], file_id)
        children = json.loads(self.http("GET", "/api/v1/folders?parent_id=" + self.folder, headers=self.auth)[2])
        self.assertEqual(len(children), 1)

    def test_directory_cancel_recovers_root_and_rejects_changed_source(self):
        source = self.directory_source()
        prepared = self.call("prepare_directory", source=str(source), parent=self.folder)
        (source / "added.txt").write_text("changed")
        with self.assertRaises(ClientError):
            self.call("upload_directory", directory_job=prepared)
        self.assertEqual(json.loads(self.http("GET", "/api/v1/folders?parent_id=" + self.folder, headers=self.auth)[2]), [])
        code, _, body = self.http("POST", "/api/v1/folders", {"parent_id": self.folder, "name": source.name},
                                 {**self.auth, "Idempotency-Key": prepared["id"]})
        self.assertEqual(code, 201)
        root_id = json.loads(body)["id"]
        self.call("cancel_directory", directory_job=prepared)
        self.assertEqual(self.http("GET", "/api/v1/nodes/" + root_id, headers=self.auth)[0], 404)


if __name__ == "__main__":
    unittest.main()
