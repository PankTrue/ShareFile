#!/usr/bin/env python3
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parent


def main():
    output = ROOT / "build"
    output.mkdir(exist_ok=True)
    for arch in ("amd64", "arm64"):
        name = "ShareFile-Linux-" + ("x86_64" if arch == "amd64" else "arm64")
        package = output / name
        if package.exists():
            shutil.rmtree(package)
        package.mkdir()
        environment = {**os.environ, "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"}
        subprocess.run([os.environ.get("GO", "go"), "build", "-trimpath", "-ldflags=-s -w",
                        "-o", str(package / "sharefile-client"), "./cmd/sharefile-client"],
                       cwd=ROOT.parent / "core", env=environment, check=True)
        for entry in ("sharefile", "install.py", "uninstall.sh"):
            shutil.copy2(ROOT / entry, package / entry)
            (package / entry).chmod(0o755)
        shutil.copytree(ROOT / "sharefile_linux", package / "sharefile_linux", ignore=shutil.ignore_patterns("__pycache__"))
        shutil.copytree(ROOT / "resources", package / "resources")
        shutil.copy2(ROOT.parent / "localization/en.json", package / "sharefile_linux/en.json")
        shutil.copy2(ROOT.parents[1] / "docs/linux.md", package / "README.md")
        shutil.copy2(ROOT.parents[1] / "docs/desktop-localization.md", package / "desktop-localization.md")
        archive = output / (name + ".tar.gz")
        with tarfile.open(archive, "w:gz") as file:
            file.add(package, arcname=name)
        checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
        (output / (archive.name + ".sha256")).write_text(checksum + "  " + archive.name + "\n")
        print(archive)


if __name__ == "__main__":
    main()
