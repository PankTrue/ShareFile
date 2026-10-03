"""Isolated local process smoke test; uses only temporary storage and loopback."""
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

BINARY = str(Path(__file__).resolve().parents[1] / "backend/bin/sharefile")


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    with tempfile.TemporaryDirectory(prefix="sharefile-smoke-") as tmp:
        root = Path(tmp)
        public_port, private_port = free_port(), free_port()
        config = root / "config.json"
        settings = {
            "data_dir": str(root / "storage"), "volume_id": "smoke-volume",
            "public_addr": f"127.0.0.1:{public_port}", "admin_addr": f"127.0.0.1:{private_port}",
            "public_url": f"http://127.0.0.1:{public_port}", "admin_url": f"http://127.0.0.1:{private_port}",
            "insecure_local": True, "reserve_bytes": 0, "upload_slots": 2, "download_slots": 2,
            "chunk_bytes": 1048576, "log_file": str(root / "server.jsonl"),
        }
        config.write_text(json.dumps(settings))
        # A disposable test password; never use it for a real installation.
        password = "smoke-password-test-only"

        def cli(command, input_text=None, cfg=config, extra=(), success=True):
            result = subprocess.run(
                [BINARY, command, "-config", str(cfg), *extra], input=input_text,
                text=True, capture_output=True, timeout=15,
            )
            if success and result.returncode:
                raise RuntimeError(result.stderr)
            if not success and result.returncode == 0:
                raise RuntimeError("Expected process lock refusal")

        cli("init-storage")
        cli("admin-create", input_text=password + "\n", extra=("-username", "admin", "-password-stdin"))
        process = None

        def call(method, path, body=None, headers=None, port=public_port):
            if isinstance(body, dict):
                body = json.dumps(body).encode()
                headers = {**(headers or {}), "Content-Type": "application/json"}
            conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
            try:
                conn.request(method, path, body=body, headers=headers or {})
                response = conn.getresponse()
                return response.status, {k.lower(): v for k, v in response.getheaders()}, response.read()
            finally:
                conn.close()

        def start():
            nonlocal process
            process = subprocess.Popen(
                [BINARY, "serve", "-config", str(config)], stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE, text=True,
            )
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError(process.stderr.read())
                try:
                    if call("GET", "/readyz")[0] == 200:
                        return
                except OSError:
                    pass
                time.sleep(0.05)
            raise RuntimeError("Server did not become ready")

        def stop():
            nonlocal process
            process.send_signal(signal.SIGTERM)
            process.wait(timeout=35)
            if process.returncode != 0:
                raise RuntimeError(process.stderr.read())
            process.stderr.close()
            process = None

        try:
            start()
            code, headers, data = call("POST", "/api/v1/auth/login", {"username": "admin", "password": password})
            assert code == 200
            login = json.loads(data)
            auth = {"Cookie": headers["set-cookie"].split(";")[0], "X-CSRF-Token": login["csrf_token"]}
            assert call("GET", "/api/v1/admin/users", headers=auth)[0] == 404
            assert call("GET", "/api/v1/admin/users", headers=auth, port=private_port)[0] == 200
            cli("backup", extra=("-archive", str(root / "busy.tar")), success=False)
            payload = os.urandom(2 * 1048576)
            b64 = lambda text: base64.b64encode(text.encode()).decode()
            tus = {
                **auth, "Tus-Resumable": "1.0.0", "Upload-Length": str(len(payload)),
                "Upload-Metadata": "filename " + b64("smoke.bin") + ",folder_id " + b64(login["user"]["home_id"]),
            }
            code, headers, _ = call("POST", "/api/v1/uploads", headers=tus)
            assert code == 201
            url = urllib.parse.urlsplit(headers["location"]).path
            node = headers["sharefile-file-id"]
            # Break a real TCP PATCH after 70,000 bytes of the declared body.
            conn = http.client.HTTPConnection("127.0.0.1", public_port, timeout=10)
            conn.connect()
            conn.putrequest("PATCH", url)
            for key, value in {
                **auth, "Tus-Resumable": "1.0.0", "Upload-Offset": "0",
                "Content-Type": "application/offset+octet-stream", "Content-Length": "1000000",
            }.items():
                conn.putheader(key, value)
            conn.endheaders()
            conn.send(payload[:70000])
            conn.close()
            code, headers, _ = call("HEAD", url, headers=tus)
            assert code == 200 and headers["upload-offset"] == "70000", headers
            stop()
            start()
            code, headers, _ = call("HEAD", url, headers=tus)
            assert code == 200 and headers["upload-offset"] == "70000"
            offset = 70000
            while offset < len(payload):
                chunk = payload[offset:offset + 1048576]
                code, headers, _ = call("PATCH", url, chunk, {
                    **auth, "Tus-Resumable": "1.0.0", "Upload-Offset": str(offset),
                    "Content-Type": "application/offset+octet-stream",
                })
                assert code == 204
                offset = int(headers["upload-offset"])
            code, _, data = call("GET", "/api/v1/nodes/" + node + "/content", headers=auth)
            assert code == 200 and hashlib.sha256(data).digest() == hashlib.sha256(payload).digest()
            code, _, data = call("GET", "/api/v1/nodes/" + node + "/content", headers={**auth, "Range": "bytes=70000-70127"})
            assert code == 206 and data == payload[70000:70128]
            code, _, data = call("POST", "/api/v1/shares", {"node_id": node}, auth)
            assert code == 201
            share_path = urllib.parse.urlsplit(json.loads(data)["url"]).path
            assert call("GET", share_path)[2] == payload
            stop()
            cli("backup", extra=("-archive", str(root / "backup.tar")))
            restore_config = root / "restore.json"
            restore_config.write_text(json.dumps({**settings, "data_dir": str(root / "restored")}))
            cli("restore", cfg=restore_config, extra=("-archive", str(root / "backup.tar")))
            assert (root / "restored/objects" / node).read_bytes() == payload
            print("PASS: real process, listener isolation, process lock, interrupted TCP upload, restart/resume, SHA-256, Range, public share, graceful SIGTERM, CLI backup/restore")
        finally:
            if process is not None:
                process.terminate()
                try:
                    process.wait(timeout=35)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                process.stderr.close()


if __name__ == "__main__":
    main()
