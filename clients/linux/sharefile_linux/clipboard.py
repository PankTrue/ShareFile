"""Durable, private PNG sources for clipboard uploads; no clipboard monitoring."""
import os
from pathlib import Path
import struct
import tempfile
import threading
from datetime import datetime
import uuid

from .bridge import ClientError

MAX_BYTES = 32 * 1024 * 1024
MAX_PIXELS = 25_000_000


class ClipboardImages:
    def __init__(self, directory):
        self.directory = Path(directory).absolute()
        self._preparing = set()
        self._lock = threading.RLock()

    def write(self, png):
        if (not isinstance(png, bytes) or not 33 <= len(png) <= MAX_BYTES
                or png[:8] != b"\x89PNG\r\n\x1a\n" or png[12:16] != b"IHDR"):
            raise ClientError("Изображение повреждено или слишком большое (максимум 32 МиБ)")
        width, height = struct.unpack(">II", png[16:24])
        if not width or not height or width * height > MAX_PIXELS:
            raise ClientError("Изображение слишком большое (максимум 25 Мп)")
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        if self.directory.is_symlink():
            raise ClientError("Не удалось открыть каталог изображений")
        self.directory.chmod(0o700)
        path = self.directory / ("Screenshot-" + datetime.now().strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex + ".png")
        staging = None
        try:
            with tempfile.NamedTemporaryFile(dir=self.directory, prefix=".image-", delete=False) as file:
                staging = Path(file.name)
                os.fchmod(file.fileno(), 0o600)
                file.write(png)
                file.flush()
                os.fsync(file.fileno())
            with self._lock:
                self._preparing.add(str(path))
                try:
                    os.replace(staging, path)
                except OSError:
                    self._preparing.discard(str(path))
                    raise
            staging = None
            return str(path)
        finally:
            if staging is not None:
                staging.unlink(missing_ok=True)

    def release(self, source):
        with self._lock:
            self._preparing.discard(source)

    def remove(self, source):
        path = Path(source)
        if path.parent != self.directory or not path.name.startswith("Screenshot-") or path.suffix != ".png":
            return
        try:
            path.unlink(missing_ok=True)
        except OSError:
            pass  # Retry cleanup on the next checkpoint or launch.

    def reconcile(self, jobs):
        with self._lock:
            keep = {job["source"] for job in jobs if job["status"] not in ("done", "uploaded", "uncertain")} | self._preparing
            if self.directory.is_symlink():
                return
            try:
                for path in self.directory.glob("Screenshot-*.png"):
                    if str(path) not in keep:
                        self.remove(str(path))
            except OSError:
                pass
