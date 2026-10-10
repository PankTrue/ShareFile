from .localization import tr, normalize
import copy
import json
import os
from pathlib import Path
import re
import tempfile
import unicodedata

from .bridge import ClientError

IDENTIFIER = re.compile(r"[A-Za-z0-9_-]{43}\Z")
CHECKPOINT = ("id", "source", "name", "size", "modified", "fingerprint", "folder",
              "upload", "file_id", "offset")
JOB_FIELDS = CHECKPOINT + ("status", "message", "batch", "days", "never", "share_id")
DEFAULTS = {"origin": "", "owner": "", "username": "", "folder": "", "folder_name": "Uploads",
            "days": 7, "never": False, "silent_uploads": True, "notify_uploads": True, "language": "en"}
STATUSES = {"queued", "uploading", "paused", "uploaded", "sharing", "uncertain", "done", "error", "cancelling"}
UNCERTAIN = tr("Сервер мог создать ссылку. Проверьте ссылки в Web UI перед созданием новой")


def require_id(value):
    if not isinstance(value, str) or not IDENTIFIER.fullmatch(value):
        raise ClientError(tr("Некорректный идентификатор в очереди или ответе сервера"))


def validate_job(job):
    for field in ("id", "folder", "batch"):
        require_id(job.get(field))
    for field in ("source", "name", "fingerprint", "upload", "file_id", "share_id", "message"):
        if not isinstance(job.get(field), str):
            raise ClientError(tr("Некорректный формат очереди"))
    for field in ("size", "modified", "offset", "days"):
        if type(job.get(field)) is not int:
            raise ClientError(tr("Некорректный размер или срок в очереди"))
    if not 0 <= job["offset"] <= job["size"] <= (1 << 63) - 1 or not 1 <= job["days"] <= 365:
        raise ClientError(tr("Некорректный размер или срок в очереди"))
    if (not Path(job["source"]).is_absolute() or Path(job["source"]).name != job["name"]
            or not re.fullmatch(r"[a-f0-9]{64}", job["fingerprint"])):
        raise ClientError(tr("Некорректный путь или отпечаток файла в очереди"))
    if job["upload"] and not re.fullmatch(r"/api/v1/uploads/[A-Za-z0-9_-]{43}", job["upload"]):
        raise ClientError(tr("Некорректный адрес загрузки в очереди"))
    for field in ("file_id", "share_id"):
        if job[field]:
            require_id(job[field])
    if job.get("status") not in STATUSES or type(job.get("never")) is not bool:
        raise ClientError(tr("Неизвестное состояние очереди"))
    if job.get("directory") is not None:
        validate_directory(job)


def directory_checkpoint(directory):
    # Nested jobs use the same explicit credential-free allowlist as file uploads.
    return {"folders": [{key: folder[key] for key in ("path", "key", "id")}
                        for folder in directory["folders"]],
            "files": [{"path": entry["path"], "complete": entry["complete"],
                       "job": {key: entry["job"][key] for key in CHECKPOINT}}
                      for entry in directory["files"]]}


def validate_directory(job):
    directory = directory_checkpoint(job["directory"])
    if (job["upload"] or not isinstance(directory["folders"], list)
            or not isinstance(directory["files"], list)
            or len(directory["folders"]) + len(directory["files"]) > 10000):
        raise ClientError(tr("Некорректная структура папки в очереди"))
    paths, parents, identities, total = set(), {""}, {job["id"]}, 0

    def path(value):
        if (not isinstance(value, str) or value in paths or not value or len(value.split("/")) > 128
                or any(not part or part in (".", "..") or part.strip() != part
                       or "\\" in part or len(part.encode()) > 240 or any(unicodedata.category(char) == "Cc" for char in part)
                       for part in value.split("/")) or value.rpartition("/")[0] not in parents):
            # Parent paths are validated explicitly below without resolving local symlinks.
            raise ClientError(tr("Некорректный путь в структуре папки"))
        paths.add(value)

    for folder in directory["folders"]:
        path(folder["path"])
        require_id(folder["key"])
        if folder["key"] in identities:
            raise ClientError(tr("Повторяющийся ключ загрузки папки"))
        identities.add(folder["key"])
        if folder["id"]:
            require_id(folder["id"])
        parents.add(folder["path"])
    for entry in directory["files"]:
        path(entry["path"])
        child = entry["job"]
        validate_job({**child, "batch": job["batch"], "days": job["days"], "never": job["never"],
                      "status": "queued", "message": "", "share_id": ""})
        if (Path(child["source"]) != Path(job["source"]) / entry["path"] or child["id"] in identities
                or type(entry["complete"]) is not bool or (entry["complete"] and
                    (not child["file_id"] or child["offset"] != child["size"]))):
            raise ClientError(tr("Некорректный файл в структуре папки"))
        identities.add(child["id"])
        total += child["size"]
    if total != job["size"] or len(json.dumps(directory, ensure_ascii=False).encode()) > 16 << 20:
        raise ClientError(tr("Некорректный размер структуры папки"))


def persisted_job(job):
    output = {key: job[key] for key in JOB_FIELDS}
    if job.get("directory") is not None:
        output["directory"] = directory_checkpoint(job["directory"])
    return output


class StateStore:
    def __init__(self, path=None):
        state_home = Path(os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state"))
        self.path = Path(path) if path is not None else state_home / "sharefile/state.json"

    def load(self):
        if not self.path.exists():
            return {"version": 1, "settings": copy.deepcopy(DEFAULTS), "jobs": []}
        try:
            if self.path.stat().st_size > 64 << 20:
                raise ValueError()
            raw = json.loads(self.path.read_text(encoding="utf-8"))
            if raw["version"] != 1 or not isinstance(raw["settings"], dict) or not isinstance(raw["jobs"], list) or len(raw["jobs"]) > 100:
                raise ValueError()
            settings = {key: raw["settings"].get(key, value) for key, value in DEFAULTS.items()}
            settings["language"] = normalize(settings["language"])
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
                job = persisted_job(raw_job)
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
            raise ClientError(tr("Не удалось прочитать очередь. Сохраните state.json перед восстановлением")) from None

    def save(self, state):
        # Explicit allowlist: public capabilities, tokens and passwords never reach disk.
        output = {"version": 1, "settings": {key: state["settings"][key] for key in DEFAULTS},
                  "jobs": [persisted_job(job) for job in state["jobs"]]}
        payload = json.dumps(output, ensure_ascii=True)
        if len(payload.encode()) > 64 << 20:
            raise ClientError(tr("Очередь слишком большая. Удалите завершённые записи"))
        directory = self.path.parent
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.chmod(directory, 0o700)
        staging = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=directory,
                                             prefix=".state-", delete=False) as file:
                staging = Path(file.name)
                os.chmod(staging, 0o600)
                file.write(payload)
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
