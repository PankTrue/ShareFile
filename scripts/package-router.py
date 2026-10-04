#!/usr/bin/env python3
"""Package the prebuilt Linux ARM64 server, Web UI and Entware scripts."""

import hashlib
from pathlib import Path
import shutil
import tarfile

ROOT = Path(__file__).resolve().parent.parent
OUTPUT = ROOT / "backend/bin"
ENTWARE = ROOT / "deploy/entware"
ARCHIVE = OUTPUT / "sharefile-router.tar.gz"
ENTRIES = [
    "sharefile.lan.example.json", "sharefile.ssh.example.json", "sharefile.example.json",
    "install.sh", "first-install.sh", "update.sh", "enable-lan.sh", "tls.sh",
    "S90sharefile", "service/run",
]


def main():
    sources = [(OUTPUT / "sharefile-linux-arm64", "sharefile-linux-arm64")]
    sources += [(ENTWARE / name, name) for name in ENTRIES]
    sources += [(ROOT / "web/dist", "dist")]
    for source, _ in sources:
        if not source.exists():
            raise SystemExit(f"Missing release input: {source}; run make router-release")
        if source.is_symlink() or (source.is_dir() and any(path.is_symlink() for path in source.rglob("*"))):
            raise SystemExit(f"Symlinks are not allowed in release inputs: {source}")
    if not (ROOT / "web/dist/index.html").is_file():
        raise SystemExit("Missing Web UI index.html; run make web-build")
    with tarfile.open(ARCHIVE, "w:gz", format=tarfile.USTAR_FORMAT) as archive:
        for source, name in sources:
            archive.add(source, arcname=name)
    with ARCHIVE.open("rb") as source:
        digest = hashlib.file_digest(source, "sha256").hexdigest()
    ARCHIVE.with_name(ARCHIVE.name + ".sha256").write_text(f"{digest}  {ARCHIVE.name}\n")
    shutil.copyfile(ENTWARE / "install.sh", OUTPUT / "install-sharefile.sh")
    print(f"Release: {ARCHIVE} ({ARCHIVE.stat().st_size / 1024 / 1024:.1f} MiB)")


if __name__ == "__main__":
    main()
