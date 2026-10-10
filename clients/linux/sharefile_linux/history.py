from .localization import tr
"""Bounded metadata index; public capabilities live only in Secret Service."""
import copy
import json
import os
from pathlib import Path
import re
import tempfile
import time
from urllib.parse import urlsplit

from .bridge import ClientError
from .state import require_id

LIMIT = 200
FIELDS = ("origin", "owner", "share_id", "job_id", "name", "created", "expires", "revoked")


def validate_url(value):
    url = urlsplit(value)
    if (url.scheme not in ("https", "http") or not url.netloc or url.username or url.password
            or url.query or url.fragment or not re.fullmatch(r"/s/[A-Za-z0-9_-]{43}", url.path)
            or (url.scheme == "http" and url.hostname not in ("localhost", "127.0.0.1", "::1"))):
        raise ClientError(tr("Некорректная ссылка в защищённой истории"))
    return value


class HistoryStore:
    def __init__(self, path, credentials):
        self.path, self.credentials = Path(path), credentials
        self.entries = []
        self.available = True

    @staticmethod
    def key(entry):
        return "history:" + entry["owner"] + ":" + entry["share_id"]

    def load(self):
        try:
            if not self.path.exists():
                return
            if self.path.stat().st_size > 2 << 20:
                raise ValueError()
            entries = json.loads(self.path.read_text(encoding="utf-8"))
            if not isinstance(entries, list) or len(entries) > LIMIT:
                raise ValueError()
            output, identities = [], set()
            for raw in entries:
                entry = {key: raw[key] for key in FIELDS}
                for key in ("owner", "share_id", "job_id"):
                    require_id(entry[key])
                url = urlsplit(entry["origin"])
                if (url.scheme not in ("http", "https") or not url.netloc or url.username
                        or url.password or url.path or url.query or url.fragment
                        or not isinstance(entry["name"], str) or len(entry["name"]) > 1024
                        or type(entry["revoked"]) is not bool
                        or type(entry["created"]) is not int or not 0 < entry["created"] <= 253402300799
                        or type(entry["expires"]) is not int or not 0 <= entry["expires"] <= 253402300799):
                    raise ValueError()
                identity = (entry["origin"], self.key(entry))
                if identity in identities:
                    raise ValueError()
                identities.add(identity)
                output.append(entry)
            self.entries = output
        except (OSError, ValueError, KeyError, TypeError, ClientError):
            self.available = False
            raise ClientError(tr("Не удалось прочитать history.json. История сохранена для восстановления")) from None

    def _save(self, entries):
        directory = self.path.parent
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.chmod(directory, 0o700)
        staging = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=directory,
                                             prefix=".history-", delete=False) as file:
                staging = Path(file.name)
                os.chmod(staging, 0o600)
                json.dump([{key: entry[key] for key in FIELDS} for entry in entries], file, ensure_ascii=True)
                file.flush()
                os.fsync(file.fileno())
            os.replace(staging, self.path)
            staging = None
        finally:
            if staging is not None:
                staging.unlink(missing_ok=True)

    def add(self, origin, owner, job, link):
        if not self.available:
            raise ClientError(tr("История недоступна. Ссылка доступна в текущей очереди"))
        entry = {"origin": origin, "owner": owner, "share_id": link["share"]["id"],
                 "job_id": job["id"], "name": job["name"], "created": link["share"].get("created_at", int(time.time())),
                 "expires": link["share"].get("expires_at") or (0 if job["never"] else int(time.time()) + job["days"] * 86400),
                 "revoked": False}
        for field in ("owner", "job_id", "share_id"):
            require_id(entry[field])
        validate_url(link["url"])
        self.credentials.write(origin, link["url"], self.key(entry))
        previous = [item for item in self.entries if (item["origin"], self.key(item)) != (origin, self.key(entry))]
        entries = [entry, *previous][:LIMIT]
        try:
            self._save(entries)
        except Exception:
            if not any(item["origin"] == origin and self.key(item) == self.key(entry) for item in self.entries):
                try:
                    self.credentials.delete(origin, self.key(entry))
                except Exception:
                    pass
            raise
        self.entries = entries
        for removed in previous[LIMIT - 1:]:
            self.credentials.delete(removed["origin"], self.key(removed))

    def for_account(self, origin, owner):
        return copy.deepcopy([entry for entry in self.entries if entry["origin"] == origin and entry["owner"] == owner])

    def find(self, origin, owner, identity):
        return next(entry for entry in self.entries if entry["origin"] == origin
                    and entry["owner"] == owner and entry["share_id"] == identity)

    def link(self, entry):
        if entry["revoked"] or (entry["expires"] and entry["expires"] <= time.time()):
            raise ClientError(tr("Ссылка отозвана или срок действия истёк"))
        value = self.credentials.read(entry["origin"], self.key(entry))
        if not value:
            raise ClientError(tr("Ссылка недоступна в системном хранилище секретов"))
        return validate_url(value)

    def revoked(self, entry):
        # Disable immediately after confirmed DELETE, even if local persistence fails.
        entry["revoked"] = True
        try:
            self._save(self.entries)
        finally:
            self.credentials.delete(entry["origin"], self.key(entry))
