import copy
import json
import os
from pathlib import Path
import re
import tempfile

from .bridge import ClientError

IDENTIFIER = re.compile(r"[A-Za-z0-9_-]{43}\Z")
CHECKPOINT = ("id", "source", "name", "size", "modified", "fingerprint", "folder",
              "upload", "file_id", "offset")
JOB_FIELDS = CHECKPOINT + ("status", "message", "batch", "days", "never", "share_id")
DEFAULTS = {"origin": "", "owner": "", "username": "", "folder": "", "folder_name": "Uploads",
            "days": 7, "never": False, "silent_uploads": True, "notify_uploads": True}
STATUSES = {"queued", "uploading", "paused", "uploaded", "sharing", "uncertain", "done", "error", "cancelling"}
UNCERTAIN = "Сервер мог создать ссылку. Проверьте ссылки в Web UI перед созданием новой"


def require_id(value):
    if not isinstance(value, str) or not IDENTIFIER.fullmatch(value):
        raise ClientError("Некорректный идентификатор в очереди или ответе сервера")


def validate_job(job):
    for field in ("id", "folder", "batch"):
        require_id(job.get(field))
    for field in ("source", "name", "fingerprint", "upload", "file_id", "share_id", "message"):
        if not isinstance(job.get(field), str):
            raise ClientError("Некорректный формат очереди")
    for field in ("size", "modified", "offset", "days"):
        if type(job.get(field)) is not int:
            raise ClientError("Некорректный размер или срок в очереди")
    if not 0 <= job["offset"] <= job["size"] <= (1 << 63) - 1 or not 1 <= job["days"] <= 365:
        raise ClientError("Некорректный размер или срок в очереди")
    if (not Path(job["source"]).is_absolute() or Path(job["source"]).name != job["name"]
            or not re.fullmatch(r"[a-f0-9]{64}", job["fingerprint"])):
        raise ClientError("Некорректный путь или отпечаток файла в очереди")
    if job["upload"] and not re.fullmatch(r"/api/v1/uploads/[A-Za-z0-9_-]{43}", job["upload"]):
        raise ClientError("Некорректный адрес загрузки в очереди")
    for field in ("file_id", "share_id"):
        if job[field]:
            require_id(job[field])
    if job.get("status") not in STATUSES or type(job.get("never")) is not bool:
        raise ClientError("Неизвестное состояние очереди")


class StateStore:
    def __init__(self, path=None):
        state_home = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state"))
        self.path = Path(path) if path is not None else state_home / "sharefile/state.json"

    def load(self):
        if not self.path.exists():
            return {"version": 1, "settings": copy.deepcopy(DEFAULTS), "jobs": []}
        try:
            if self.path.stat().st_size > 4 << 20:
                raise ValueError()
            raw = json.loads(self.path.read_text(encoding="utf-8"))
            if raw["version"] != 1 or not isinstance(raw["settings"], dict) or not isinstance(raw["jobs"], list) or len(raw["jobs"]) > 100:
                raise ValueError()
            settings = {key: raw["settings"].get(key, value) for key, value in DEFAULTS.items()}
            for key, default in DEFAULTS.items():
                if type(settings[key]) is not type(default):
                    raise ValueError()
            if not 1 <= settings["days"] <= 365:
                raise ValueError()
            for key in ("owner", "folder"):
                if settings[key]:
                    require_id(settings[key])
            jobs = []
            for raw_job in raw["jobs"]:
                job = {key: raw_job[key] for key in JOB_FIELDS}
                validate_job(job)
                if job["status"] in ("queued", "uploading", "cancelling"):
                    job["status"] = "paused"
                elif job["status"] == "sharing":
                    job.update(status="uncertain", message=UNCERTAIN)
                job["link"] = ""
                jobs.append(job)
            if len({job["id"] for job in jobs}) != len(jobs):
                raise ValueError()
            return {"version": 1, "settings": settings, "jobs": jobs}
        except (KeyError, ValueError, TypeError, ClientError):
            raise ClientError("Не удалось прочитать очередь. Сохраните state.json перед восстановлением") from None

    def save(self, state):
        # Explicit allowlist: public capabilities, tokens and passwords never reach disk.
        output = {"version": 1, "settings": {key: state["settings"][key] for key in DEFAULTS},
                  "jobs": [{key: job[key] for key in JOB_FIELDS} for job in state["jobs"]]}
        directory = self.path.parent
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.chmod(directory, 0o700)
        staging = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=directory,
                                             prefix=".state-", delete=False) as file:
                staging = Path(file.name)
                os.chmod(staging, 0o600)
                json.dump(output, file, ensure_ascii=True)
                file.flush()
                os.fsync(file.fileno())
            os.replace(staging, self.path)
            staging = None
            if hasattr(os, "O_DIRECTORY"):
                descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
        finally:
            if staging is not None:
                staging.unlink(missing_ok=True)
