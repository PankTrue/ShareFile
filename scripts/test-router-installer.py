#!/usr/bin/env python3
"""Exercise downloads, checksum rejection and update rollback in temporary roots."""

import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
ENTWARE = ROOT / "deploy/entware"


class RouterInstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="sharefile-installer-test-")
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.opt = self.base / "opt"
        for name in ["bin", "sbin", "etc", "tmp"]:
            (self.opt / name).mkdir(parents=True)
        self.mounts = self.base / "mounts"
        self.mounts.write_text(f"/dev/test {self.opt} ext4 rw,relatime 0 0\n")
        self.marker = self.base / "executed"
        self.command("id", "echo 0")
        self.command("uname", "echo aarch64")
        self.command("opkg", "exit 0")
        self.command("sleep", "exit 0")
        sha = shutil.which("sha256sum")
        if sha:
            self.command("sha256sum", f'exec "{sha}" "$@"')
        else:
            self.command("sha256sum", f'exec "{shutil.which("shasum")}" -a 256 "$@"')

    def command(self, name, body):
        path = self.opt / "bin" / name
        path.write_text("#!/bin/sh\n" + body + "\n")
        path.chmod(0o755)

    def script(self, name, destination):
        # Only relocate the router filesystem; execute the production shell logic.
        source = (ENTWARE / name).read_text().replace("/opt", str(self.opt)).replace("/proc/mounts", str(self.mounts))
        destination.write_text(source)
        return destination

    def run_script(self, script, *args):
        return subprocess.run(["sh", str(script), *args], capture_output=True, text=True, env=dict(os.environ, SHAREFILE_REPOSITORY="PankTrue/ShareFile"))

    def download_fixture(self, *, bad_checksum=False, unsafe=False, release=None, healthy=True):
        assets = self.base / "assets"
        assets.mkdir()
        archive = assets / "sharefile-router.tar.gz"
        with tarfile.open(archive, "w:gz", format=tarfile.USTAR_FORMAT) as bundle:
            scripts = [("first-install.sh", f"#!/bin/sh\necho first > '{self.marker}'\n")]
            if release is None:
                scripts.append(("update.sh", f"#!/bin/sh\necho update > '{self.marker}'\n"))
            else:
                for path in sorted(release.iterdir()):
                    bundle.add(path, arcname=path.name)
            for name, content in scripts:
                data = content.encode()
                info = tarfile.TarInfo(name)
                info.size = len(data)
                bundle.addfile(info, io.BytesIO(data))
            if unsafe:
                info = tarfile.TarInfo("../escaped")
                info.size = 1
                bundle.addfile(info, io.BytesIO(b"x"))
        digest = "0" * 64 if bad_checksum else hashlib.sha256(archive.read_bytes()).hexdigest()
        (assets / (archive.name + ".sha256")).write_text(f"{digest}  {archive.name}\n")
        python = shutil.which("python3")
        downloader = self.base / "curl.py"
        downloader.write_text(f'''import pathlib, shutil, sys
args = sys.argv[1:]
if "--head" in args:
    print("https://github.com/PankTrue/ShareFile/releases/tag/v1.2.3", end="")
elif args[-1].endswith("/readyz"):
    sys.exit({0 if healthy else 22})
else:
    source = pathlib.Path({str(assets)!r}) / args[-1].rsplit("/", 1)[-1]
    shutil.copyfile(source, args[args.index("--output") + 1])
''')
        self.command("curl", f'exec "{python}" "{downloader}" "$@"')
        return self.script("install.sh", self.base / "install.sh")

    def test_latest_release_is_verified_then_installed(self):
        result = self.run_script(self.download_fixture())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.marker.read_text().strip(), "first")
        self.assertIn("v1.2.3 installed", result.stdout)

    def test_fixed_version_updates_existing_installation(self):
        script = self.download_fixture()
        (self.opt / "etc/sharefile.json").write_text("{}")
        result = self.run_script(script, "--version", "v1.2.3")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.marker.read_text().strip(), "update")

    def test_bad_checksum_never_runs_installation(self):
        result = self.run_script(self.download_fixture(bad_checksum=True))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum mismatch", result.stderr)
        self.assertFalse(self.marker.exists())

    def test_unsafe_archive_never_runs_installation(self):
        result = self.run_script(self.download_fixture(unsafe=True))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Unsafe release archive", result.stderr)
        self.assertFalse(self.marker.exists())
        self.assertFalse((self.base / "escaped").exists())

    def test_missing_release_never_runs_installation(self):
        script = self.download_fixture()
        self.command("curl", "exit 22")
        result = self.run_script(script)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.marker.exists())

    def update_fixture(self, *, healthy):
        web = self.opt / "share/sharefile/web"
        web.mkdir(parents=True)
        (web / "index.html").write_text("old UI")
        service = self.opt / "etc/sharefile-service"
        service.mkdir()
        (service / "run").write_text("old run")
        init = self.opt / "etc/init.d"
        init.mkdir()
        old_init = init / "S90sharefile"
        lifecycle = f"printf 'old %s\\n' \"$1\" >> '{self.base / 'lifecycle'}'\n"
        old_init.write_text("#!/bin/sh\n" + lifecycle + "exit 0\n")
        old_init.chmod(0o755)
        (self.opt / "sbin/sharefile").write_text("old binary")
        data = self.base / "user-data"
        data.mkdir()
        (data / "account-and-file").write_text("preserve existing data")
        (data / ".sharefile-volume").write_text("existing-volume\n")
        for name in ("metadata", "objects", "uploads"):
            (data / name).mkdir()
        (data / "metadata/sharefile.db").write_bytes(b"existing account and link metadata")
        (data / "objects/existing-file").write_bytes(b"existing stored file")
        (data / "uploads/partial-upload").write_bytes(b"existing unfinished upload")
        tls = self.opt / "etc/sharefile"
        tls.mkdir()
        for name in ("lan-ca.pem", "lan-ca-key.pem", "admin-cert.pem", "admin-key.pem"):
            (tls / name).write_text(f"existing {name}")
            (tls / name).chmod(0o600)
        config = {"data_dir": str(data), "volume_id": "existing-volume", "public_addr": "127.0.0.1:8080", "public_url": "https://192.168.1.1:8081", "web_dir": str(web)}
        config_path = self.opt / "etc/sharefile.json"
        config_path.write_text(json.dumps(config))
        config_path.chmod(0o600)
        release = self.base / "release"
        (release / "dist").mkdir(parents=True)
        (release / "dist/index.html").write_text("new UI")
        (release / "service").mkdir()
        (release / "service/run").write_text("new run")
        (release / "sharefile-linux-arm64").write_text("#!/bin/sh\nexit 0\n# new binary\n")
        lifecycle = f"printf 'new %s\\n' \"$1\" >> '{self.base / 'lifecycle'}'\n"
        (release / "S90sharefile").write_text("#!/bin/sh\n" + lifecycle + "exit 0\n# new init\n")
        self.command("curl", "exit 0" if healthy else "exit 22")
        return self.script("update.sh", release / "update.sh"), config_path, data

    def protected_files(self, config, data):
        files = [config, *(self.opt / "etc/sharefile").iterdir(), *(path for path in data.rglob("*") if path.is_file())]
        return {path: (path.read_bytes(), path.stat().st_mode) for path in files}

    def test_downloaded_release_can_be_installed_twice_over_existing_application(self):
        updater, config, data = self.update_fixture(healthy=True)
        protected = self.protected_files(config, data)
        web = self.opt / "share/sharefile/web"
        (web / "obsolete-asset.js").write_text("old hashed asset")
        installer = self.download_fixture(release=updater.parent)
        for _ in range(2):
            result = self.run_script(installer)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("v1.2.3 installed", result.stdout)
            self.assertEqual(self.protected_files(config, data), protected)
            self.assertFalse(self.marker.exists(), "Reinstallation must not run first-install.sh")
            self.assertEqual((web / "index.html").read_text(), "new UI")
            self.assertFalse((web / "obsolete-asset.js").exists())
            self.assertIn("new binary", (self.opt / "sbin/sharefile").read_text())
            self.assertTrue(os.access(self.opt / "sbin/sharefile", os.X_OK))
        backups = sorted((self.opt / "var/backups/sharefile").glob("release.*"))
        self.assertEqual(len(backups), 2)
        self.assertEqual(sorted((backup / "web/index.html").read_text() for backup in backups), ["new UI", "old UI"])
        self.assertEqual((self.base / "lifecycle").read_text().splitlines(), [
            "old stop", "new start", "new check", "new stop", "new start", "new check",
        ])

    def test_downloaded_update_failure_is_reported_and_rolls_back(self):
        updater, config, data = self.update_fixture(healthy=False)
        protected = self.protected_files(config, data)
        installer = self.download_fixture(release=updater.parent, healthy=False)
        result = self.run_script(installer)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("restoring previous application files", result.stderr)
        self.assertNotIn("v1.2.3 installed", result.stdout)
        self.assertFalse(self.marker.exists())
        self.assertEqual(self.protected_files(config, data), protected)
        self.assertEqual((self.opt / "sbin/sharefile").read_text(), "old binary")
        self.assertEqual((self.opt / "share/sharefile/web/index.html").read_text(), "old UI")
        self.assertEqual((self.opt / "etc/sharefile-service/run").read_text(), "old run")
        self.assertEqual((self.base / "lifecycle").read_text().splitlines(), [
            "old stop", "new start", "new stop", "old start",
        ])

    def test_incomplete_existing_installation_is_not_reinitialized(self):
        updater, config, data = self.update_fixture(healthy=True)
        protected = self.protected_files(config, data)
        (self.opt / "etc/sharefile-service/run").unlink()
        installer = self.download_fixture(release=updater.parent)
        result = self.run_script(installer)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Incomplete existing installation", result.stderr)
        self.assertFalse(self.marker.exists())
        self.assertEqual(self.protected_files(config, data), protected)
        self.assertEqual((self.opt / "sbin/sharefile").read_text(), "old binary")
        self.assertFalse((self.base / "lifecycle").exists())

    def test_incompatible_update_binary_leaves_existing_application_running(self):
        script, config, data = self.update_fixture(healthy=True)
        protected = self.protected_files(config, data)
        (script.parent / "sharefile-linux-arm64").write_text("#!/bin/sh\nexit 1\n")
        result = self.run_script(script)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("binary cannot run", result.stderr)
        self.assertEqual(self.protected_files(config, data), protected)
        self.assertEqual((self.opt / "sbin/sharefile").read_text(), "old binary")
        self.assertEqual((self.opt / "share/sharefile/web/index.html").read_text(), "old UI")
        self.assertFalse((self.base / "lifecycle").exists())

    def test_update_preserves_config_data_and_previous_application(self):
        script, config, data = self.update_fixture(healthy=True)
        original = config.read_bytes()
        result = self.run_script(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(config.read_bytes(), original)
        self.assertEqual((data / "account-and-file").read_text(), "preserve existing data")
        self.assertIn("new binary", (self.opt / "sbin/sharefile").read_text())
        self.assertEqual((self.opt / "share/sharefile/web/index.html").read_text(), "new UI")
        backups = list((self.opt / "var/backups/sharefile").glob("release.*"))
        self.assertEqual((backups[0] / "sharefile").read_text(), "old binary")

    def test_failed_readiness_restores_previous_application(self):
        script, config, data = self.update_fixture(healthy=False)
        original = config.read_bytes()
        result = self.run_script(script)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("restoring previous application files", result.stderr)
        self.assertEqual((self.opt / "sbin/sharefile").read_text(), "old binary")
        self.assertEqual((self.opt / "share/sharefile/web/index.html").read_text(), "old UI")
        self.assertEqual((self.opt / "etc/sharefile-service/run").read_text(), "old run")
        self.assertEqual(config.read_bytes(), original)
        self.assertEqual((data / "account-and-file").read_text(), "preserve existing data")


if __name__ == "__main__":
    unittest.main(verbosity=2)
