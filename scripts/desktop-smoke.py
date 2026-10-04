"""Real backend + desktop JSON bridge, disposable fixtures, no user credentials."""
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import urllib.parse

ROOT = Path(__file__).resolve().parents[1]
BACKEND = ROOT / "backend/bin/sharefile"
HELPER = ROOT / "clients/macos/build/ShareFile.app/Contents/MacOS/sharefile-client"


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    with tempfile.TemporaryDirectory(prefix="sharefile-desktop-smoke-") as tmp:
        root = Path(tmp)
        port, admin_port = free_port(), free_port()
        origin = f"http://127.0.0.1:{port}"
        config = root / "config.json"
        config.write_text(json.dumps({
            "data_dir": str(root / "storage"), "volume_id": "desktop-test",
            "public_addr": f"127.0.0.1:{port}", "admin_addr": f"127.0.0.1:{admin_port}",
            "public_url": origin, "admin_url": f"http://127.0.0.1:{admin_port}",
            "insecure_local": True, "reserve_bytes": 0, "chunk_bytes": 1048576,
        }))
        def cli(command, extra=(), input_text=None):
            subprocess.run([str(BACKEND), command, "-config", str(config), *extra],
                           input=input_text, text=True, check=True, capture_output=True, timeout=20)
        cli("init-storage")
        cli("admin-create", ("-username", "desktop-test", "-password-stdin"), "desktop-smoke-only-password\n")
        process = None

        def call_http(method, path, body=None, headers=None):
            if isinstance(body, dict):
                body = json.dumps(body).encode()
                headers = {**(headers or {}), "Content-Type": "application/json"}
            connection = http.client.HTTPConnection("127.0.0.1", port, timeout=20)
            try:
                connection.request(method, path, body=body, headers=headers or {})
                response = connection.getresponse()
                return response.status, {k.lower(): v for k, v in response.getheaders()}, response.read()
            finally:
                connection.close()

        def start():
            nonlocal process
            process = subprocess.Popen([str(BACKEND), "serve", "-config", str(config)],
                                       stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            for _ in range(150):
                try:
                    if call_http("GET", "/readyz")[0] == 200:
                        return
                except OSError:
                    pass
                if process.poll() is not None:
                    raise RuntimeError("Disposable backend failed to start")
                time.sleep(0.05)
            raise RuntimeError("Backend startup timeout")

        def stop():
            nonlocal process
            process.send_signal(signal.SIGTERM)
            process.wait(timeout=35)
            process.stderr.close()
            assert process.returncode == 0
            process = None

        token = ""
        def helper(operation, success=True, **extra):
            request = {"operation": operation, "origin": origin, "token": token, **extra}
            completed = subprocess.run([str(HELPER)], input=json.dumps(request),
                                       text=True, capture_output=True, timeout=90, check=True)
            events = [json.loads(line) for line in completed.stdout.splitlines()]
            assert events
            if success:
                assert events[-1]["type"] == "result", events[-1].get("error")
                return events[-1].get("result")
            assert events[-1]["type"] == "error"
            return events[-1]["error"]

        try:
            start()
            code, headers, body = call_http("POST", "/api/v1/auth/login", {
                "username": "desktop-test", "password": "desktop-smoke-only-password"})
            assert code == 200
            browser = json.loads(body)
            auth = {"Cookie": headers["set-cookie"].split(";")[0], "X-CSRF-Token": browser["csrf_token"]}
            pairing = helper("pair_start", name="Disposable macOS test")
            assert urllib.parse.urlsplit(pairing["verification_url"]).path == "/device/connect"
            assert pairing["interval"] >= 12
            request_id = pairing["request"]["id"]
            assert call_http("POST", f"/api/v1/device-requests/{request_id}/approve", headers=auth)[0] == 204
            token = helper("pair_poll", request_id=request_id, secret=pairing["request"]["secret"])["token"]
            assert token
            session = helper("session")
            home = session["user"]["home_id"]
            folder = next(n for n in helper("folders", parent=home) if n["name"] == "Uploads")
            assert folder["effective_role"] == 3
            payload = os.urandom(2 * 1048576 + 137)
            source = root / "Отчёт с пробелами 日本語.bin"
            source.write_bytes(payload)
            job = helper("prepare", source=str(source), parent=folder["id"])
            tus = {
                "Authorization": "Bearer " + token, "Tus-Resumable": "1.0.0",
                "Upload-Length": str(len(payload)), "Idempotency-Key": job["id"],
                "Upload-Metadata": "filename " + base64.b64encode(source.name.encode()).decode()
                + ",folder_id " + base64.b64encode(folder["id"].encode()).decode(),
            }
            code, headers, _ = call_http("POST", "/api/v1/uploads", headers=tus)
            assert code == 201
            original_file_id = headers["sharefile-file-id"]
            path = urllib.parse.urlsplit(headers["location"]).path
            connection = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
            connection.connect()
            connection.putrequest("POST", path)
            for key, value in {
                "Authorization": "Bearer " + token, "Tus-Resumable": "1.0.0",
                "Upload-Offset": "0", "X-HTTP-Method-Override": "PATCH",
                "Content-Type": "application/offset+octet-stream", "Content-Length": "1000000",
            }.items():
                connection.putheader(key, value)
            connection.endheaders()
            connection.send(payload[:70000])
            connection.close()
            assert call_http("HEAD", path, headers={"Authorization": "Bearer " + token, "Tus-Resumable": "1.0.0"})[1]["upload-offset"] == "70000"
            # The client has no creation response checkpoint: reuse its persisted idempotency key.
            checkpoint = root / "checkpoint.json"
            checkpoint.write_text(json.dumps(job))
            stop()
            start()
            resumed = helper("upload", job=json.loads(checkpoint.read_text()))
            assert resumed["file_id"] == original_file_id and resumed["offset"] == len(payload)
            code, _, downloaded = call_http("GET", "/api/v1/nodes/" + resumed["file_id"] + "/content", headers={"Authorization": "Bearer " + token})
            assert code == 200 and hashlib.sha256(downloaded).digest() == hashlib.sha256(payload).digest()
            children = call_http("GET", "/api/v1/folders?parent_id=" + folder["id"], headers={"Authorization": "Bearer " + token})
            assert len(json.loads(children[2])) == 1
            share = helper("share", job=resumed, options={"days": 7})
            assert call_http("GET", urllib.parse.urlsplit(share["url"]).path)[2] == payload
            protected = helper("share", job=resumed, options={"never": True, "password": "desktop-link-test-password"})
            assert call_http("GET", urllib.parse.urlsplit(protected["url"]).path)[0] == 401
            empty = root / "пустой файл.txt"
            empty.touch()
            zero_job = helper("prepare", source=str(empty), parent=folder["id"])
            zero_job = helper("upload", job=zero_job)
            zero_share = helper("share", job=zero_job, options={"days": 1})
            assert call_http("GET", urllib.parse.urlsplit(zero_share["url"]).path)[2] == b""
            native_app = HELPER.with_name("ShareFile")
            native = subprocess.run([str(native_app), "--integration-test"], input=json.dumps({
                "origin": origin, "token": token, "folder": folder["id"], "files": [str(source), str(empty)],
            }), text=True, capture_output=True, timeout=90, check=True)
            native_result = json.loads(native.stdout)
            assert len(native_result["links"]) == 2
            assert call_http("GET", urllib.parse.urlsplit(native_result["links"][0]).path)[2] == payload
            assert call_http("GET", urllib.parse.urlsplit(native_result["links"][1]).path)[2] == b""
            devices = json.loads(call_http("GET", "/api/v1/devices", headers=auth)[2])
            assert call_http("DELETE", "/api/v1/devices/" + devices[0]["id"], headers=auth)[0] == 204
            revoked = helper("session", success=False)
            assert "Подключ" in revoked and token not in revoked
            stop()
            print("PASS: browser device approval, real TCP interruption + backend restart + idempotent resume, SHA-256, Unicode/empty files, public/protected links, native Swift queue/process bridge, secret-free native persistence, device revocation")
        finally:
            if process is not None:
                stop()


if __name__ == "__main__":
    main()
