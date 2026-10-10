import copy
import os
from concurrent.futures import ThreadPoolExecutor
import secrets
import socket
import threading
import time

from .bridge import BridgeCall, Cancelled, ClientError, safe_message
from .history import HistoryStore
from .clipboard import ClipboardImages
from .state import CHECKPOINT, UNCERTAIN, require_id, validate_job


class Controller:
    """Serial scheduler, with synchronous cancellation and durable checkpoints."""

    def __init__(self, helper, store, credentials, dispatch=lambda callback: callback(),
                 changed=lambda snapshot: None, completed=lambda batch: None,
                 open_browser=lambda url: None, bridge_factory=BridgeCall):
        self.helper, self.store, self.credentials = helper, store, credentials
        self.dispatch, self.changed, self.completed = dispatch, changed, completed
        self.open_browser, self.bridge_factory = open_browser, bridge_factory
        self.state = store.load()  # Corrupt state stops startup; it is never overwritten.
        self.store.save(self.state)
        self.clipboard_images = ClipboardImages(store.path.parent / "clipboard")
        self.clipboard_images.reconcile(self.state["jobs"])
        self.history = HistoryStore(store.path.with_name("history.json"), credentials)
        history_error = ""
        try:
            self.history.load()
        except Exception as error:
            history_error = safe_message(error)
        self.token = self.password = self.pairing_url = ""
        self.notice = history_error
        self._lock = threading.RLock()
        self._executor = ThreadPoolExecutor(max_workers=1, thread_name_prefix="sharefile")
        self._pending = 0
        self._working = self._closing = False
        self._close_future = None
        self._active = None
        self._active_id = ""
        self._stop = threading.Event()
        self._pair_stop = threading.Event()
        self._reported = set()
        self._reported_failures = set()
        self._pending_files = []

    def snapshot(self):
        with self._lock:
            return {**copy.deepcopy(self.state), "connected": bool(self.token),
                    "history": self.history.for_account(self.state["settings"]["origin"], self.state["settings"]["owner"]),
                    "notice": self.notice, "pairing_url": self.pairing_url,
                    "active_id": self._active_id, "busy": self._pending > 0,
                    "pending_files": len(self._pending_files)}

    def _publish(self):
        snapshot = self.snapshot()
        self.dispatch(lambda: self.changed(snapshot))

    def set_notice(self, text):
        with self._lock:
            self.notice = text
        self._publish()

    def _commit(self, change):
        with self._lock:
            next_state = copy.deepcopy(self.state)
            change(next_state)
            self.store.save(next_state)
            self.state = next_state
            self.clipboard_images.reconcile(self.state["jobs"])
        self._publish()

    def _update(self, identity, **fields):
        def update(state):
            job = next(job for job in state["jobs"] if job["id"] == identity)
            job.update(fields)
        self._commit(update)

    def _job(self, identity):
        with self._lock:
            return copy.deepcopy(next(job for job in self.state["jobs"] if job["id"] == identity))

    def _request(self, operation, progress=lambda job: None, stop=None, **extra):
        with self._lock:
            call = self.bridge_factory(self.helper)
            self._active = call
            request = {"operation": operation, "origin": self.state["settings"]["origin"],
                       "token": self.token, **extra}
            if self._closing or (stop is not None and stop.is_set()):
                call.cancel()
        try:
            return call.run(request, progress)
        finally:
            with self._lock:
                self._active = None

    def _submit(self, operation):
        with self._lock:
            if self._closing:
                return None
            self._pending += 1
        self._publish()

        def run():
            try:
                if not self._closing:
                    return operation()
            except Exception as error:
                with self._lock:
                    self.notice = safe_message(error)
                raise
            finally:
                with self._lock:
                    self._pending -= 1
                self._publish()
                self._pump()
        return self._executor.submit(run)

    def restore(self):
        def restore():
            origin = self.state["settings"]["origin"]
            if origin:
                token = self.credentials.read(origin)
                if token:
                    self._attach(origin, token)
            self._add_pending()
        return self._submit(restore)

    def _attach(self, origin, token, save_token=False):
        session = self._request("session", origin=origin, token=token)["user"]
        owner, home = session["id"], session["home_id"]
        require_id(owner)
        require_id(home)
        settings = copy.deepcopy(self.state["settings"])
        if self.state["jobs"] and (settings["origin"] != origin or settings["owner"] != owner):
            raise ClientError("Очередь принадлежит другому серверу или аккаунту. Удалите записи перед сменой подключения")
        if settings["origin"] != origin or settings["owner"] != owner or not settings["folder"]:
            folders = self._request("folders", origin=origin, token=token, parent=home)
            uploads = next((folder for folder in folders if folder["name"] == "Uploads" and folder["effective_role"] >= 3), None)
            settings.update(folder=uploads["id"] if uploads else home,
                            folder_name=uploads["name"] if uploads else "Личная папка")
        settings.update(origin=origin, owner=owner, username=session["username"])
        password = self.credentials.read(origin, "password")
        if save_token:
            self.credentials.write(origin, token)
        self._commit(lambda state: state.update(settings=settings))
        self.token, self.password = token, password
        self.notice = "Linux подключён. Можно загружать файлы и папки"

    def connect(self, origin):
        self._pair_stop.clear()

        def pair():
            if self.token:
                raise ClientError("Сначала отключите текущее подключение")
            normalized = self._request("normalize", origin=origin, token="", stop=self._pair_stop)
            if self.state["jobs"] and normalized != self.state["settings"]["origin"]:
                raise ClientError("Очередь связана с текущим сервером. Удалите записи перед сменой адреса")
            pairing = self._request("pair_start", origin=normalized, token="",
                                    name=("Linux · " + socket.gethostname())[:80], stop=self._pair_stop)
            self.pairing_url = pairing["verification_url"]
            self.notice = "Подтвердите подключение в браузере"
            self._publish()
            self.dispatch(lambda: self.open_browser(pairing["verification_url"]))
            try:
                while time.time() < pairing["request"]["expires_at"]:
                    if self._pair_stop.wait(max(12, pairing["interval"])):
                        raise Cancelled()
                    result = self._request("pair_poll", origin=normalized, token="",
                                           request_id=pairing["request"]["id"],
                                           secret=pairing["request"]["secret"], stop=self._pair_stop)
                    if result.get("token"):
                        if self._pair_stop.is_set():
                            raise Cancelled()
                        self._attach(normalized, result["token"], save_token=True)
                        self._add_pending()
                        return
                raise ClientError("Время подтверждения истекло. Подключитесь заново")
            finally:
                self.pairing_url = ""
        return self._submit(pair)

    def cancel_pairing(self):
        self._pair_stop.set()
        with self._lock:
            if self._pending and not self._working and self._active:
                self._active.cancel()

    def disconnect(self):
        def disconnect():
            if any(job["status"] == "queued" for job in self.state["jobs"]):
                raise ClientError("Приостановите очередь перед отключением")
            self.credentials.delete(self.state["settings"]["origin"])
            self.token = self.password = ""
            self.notice = "Подключение удалено с компьютера. Отозвать устройство можно в Web UI"
        self.pause_all()
        return self._submit(disconnect)

    def configure(self, days, never, password, silent, notify):
        def configure():
            if not 1 <= days <= 365 or (password and not 12 <= len(password.encode("utf-8")) <= 256):
                raise ClientError("Срок ссылки: 1–365 дней; пароль: 12–256 байт UTF-8")
            origin = self.state["settings"]["origin"]
            if password and not self.token:
                raise ClientError("Сначала подключите хранилище")
            if origin:
                if password:
                    self.credentials.write(origin, password, "password")
                else:
                    self.credentials.delete(origin, "password")
            self._commit(lambda state: state["settings"].update(days=days, never=never,
                         silent_uploads=silent, notify_uploads=notify))
            self.password = password
            self.notice = "Настройки сохранены"
        return self._submit(configure)

    def folders(self, parent=""):
        return self._submit(lambda: self._request("folders", parent=parent))

    def select_folder(self, identity):
        def select():
            require_id(identity)
            folder = self._request("folder", parent=identity)
            if folder["kind"] != "folder" or folder["effective_role"] < 3:
                raise ClientError("Для загрузки и публичной ссылки нужны права управляющего")
            self._commit(lambda state: state["settings"].update(folder=folder["id"], folder_name=folder["name"]))
        return self._submit(select)

    def _add_pending(self):
        if self.token and self._pending_files:
            paths, self._pending_files = self._pending_files, []
            self._add(paths)

    def add_files(self, paths):
        return self._submit(lambda: self._add(list(paths)))

    def add_clipboard(self, png):
        def add():
            if not self.token:
                raise ClientError("Сначала подключите хранилище")
            source = self.clipboard_images.write(png)
            try:
                self._add([source])
            finally:
                self.clipboard_images.release(source)
                self.clipboard_images.reconcile(self.state["jobs"])
        return self._submit(add)

    def _add(self, paths):
        if not self.token:
            if len(self._pending_files) + len(paths) > 100:
                raise ClientError("Можно выбрать не более 100 файлов или папок")
            self._pending_files.extend(paths)
            raise ClientError("Подключите хранилище. Выбранные файлы и папки добавятся после подтверждения")
        settings = self.state["settings"]
        if len(self.state["jobs"]) + len(paths) > 100:
            raise ClientError("В очереди может быть не более 100 файлов или папок. Удалите завершённые записи")
        batch, prepared, errors = secrets.token_urlsafe(32), [], []
        for path in paths:
            try:
                operation = "prepare_directory" if os.path.isdir(path) else "prepare"
                job = self._request(operation, source=path, parent=settings["folder"])
                job.update(status="queued", message="", batch=batch, days=settings["days"],
                           never=settings["never"], share_id="", link="")
                validate_job(job)
                prepared.append(job)
            except Exception as error:
                errors.append(safe_message(error))
        if prepared:
            self._commit(lambda state: state["jobs"].extend(prepared))
        if errors:
            self.notice = f"Не добавлено объектов: {len(errors)}. " + errors[0]

    def _pump(self):
        with self._lock:
            if self._closing or self._working or self._pending or not self.token:
                return
            job = next((job for job in self.state["jobs"] if job["status"] == "queued"), None)
            if job is None:
                return
            self._working = True
            self._active_id = job["id"]
            self._stop = threading.Event()
            self._executor.submit(self._run_one, job["id"], self._stop)

    def _run_one(self, identity, stop):
        try:
            with self._lock:
                if self._job(identity)["status"] != "queued" or stop.is_set():
                    return
                self._update(identity, status="uploading", message="")
            job = self._job(identity)

            def checkpoint(progress):
                fields = {key: progress.get(key, "" if key != "offset" else 0)
                          for key in ("upload", "file_id", "offset")}
                if "directory" in progress:
                    fields["directory"] = progress["directory"]
                self._update(identity, **fields)
            operation, parameters = self._transfer("upload", job)
            result = self._request(operation, **parameters, progress=checkpoint, stop=stop)
            checkpoint(result)
            with self._lock:
                if stop.is_set() or self._job(identity)["status"] == "paused":
                    return
                self._update(identity, status="uploaded")
            self._share(identity, stop)
        except Exception as error:
            try:
                job = self._job(identity)
                if job["status"] == "sharing":
                    self._update(identity, status="uncertain", message=UNCERTAIN)
                elif job["status"] not in ("paused", "uncertain"):
                    self._update(identity, status="paused" if isinstance(error, Cancelled) else "error",
                                 message=safe_message(error))
            except Exception as persistence_error:
                self.notice = safe_message(persistence_error)
                # Do not schedule further network mutations after a checkpoint failure.
                self.token = ""
                with self._lock:
                    failed = next(job for job in self.state["jobs"] if job["id"] == identity)
                    failed.update(status="error", message=self.notice)
        finally:
            with self._lock:
                self._working = False
                self._active_id = ""
            self._finish_batch(self._job(identity)["batch"])
            self._publish()
            self._pump()

    def _share(self, identity, stop=None):
        job = self._job(identity)
        self._update(identity, status="sharing", message="", link="")
        try:
            link = self._request("share", job={key: job[key] for key in CHECKPOINT}, stop=stop,
                                 options={"days": job["days"], "never": job["never"], "password": self.password})
            require_id(link["share"]["id"])
        except Exception:
            self._update(identity, status="uncertain", message=UNCERTAIN)
            raise
        warning = ""
        try:
            settings = self.state["settings"]
            # The serial worker owns history mutations. Never hold the snapshot lock
            # while Secret Service may wait for the user to unlock their keyring.
            self.history.add(settings["origin"], settings["owner"], job, link)
        except Exception:
            warning = "Ссылка создана, но не сохранена в защищённой истории. Скопируйте её из очереди"
            self.notice = warning
        self._update(identity, status="done", link=link["url"], share_id=link["share"]["id"], message=warning)

    def _finish_batch(self, batch):
        with self._lock:
            jobs = [job for job in self.state["jobs"] if job["batch"] == batch]
            if not jobs:
                return
            settings = self.state["settings"]
            if all(job["status"] == "done" and job["link"] for job in jobs) and batch not in self._reported:
                self._reported.add(batch)
                result = {"names": [job["name"] for job in jobs], "links": [job["link"] for job in jobs],
                          "notify": settings["notify_uploads"], "failed": 0}
                self.dispatch(lambda: self.completed(result))
            elif (all(job["status"] in ("done", "error", "uncertain") for job in jobs)
                  and any(job["status"] in ("error", "uncertain") for job in jobs)
                  and batch not in self._reported_failures):
                self._reported_failures.add(batch)
                result = {"names": [], "links": [], "notify": settings["notify_uploads"],
                          "failed": sum(job["status"] != "done" for job in jobs)}
                self.dispatch(lambda: self.completed(result))

    def pause(self, identity):
        with self._lock:
            job = self._job(identity)
            if job["status"] in ("queued", "uploading"):
                if self._active_id == identity:
                    self._stop.set()
                    if self._active:
                        self._active.cancel()
                self._update(identity, status="paused")

    def pause_all(self):
        with self._lock:
            self._stop.set()
            if self._working and self._active:
                self._active.cancel()
            self._commit(lambda state: [job.update(status="paused") for job in state["jobs"]
                         if job["status"] in ("queued", "uploading")])

    def resume(self, identity):
        def resume():
            if not self.token:
                raise ClientError("Сначала подключите хранилище")
            if self._job(identity)["status"] in ("paused", "error"):
                self._update(identity, status="queued", message="")
        return self._submit(resume)

    def remove(self, identity):
        def remove():
            job = self._job(identity)
            if job["status"] not in ("done", "uploaded", "uncertain"):
                if not self.token:
                    raise ClientError("Для отмены серверной загрузки нужно подключение")
                self._update(identity, status="cancelling")
                try:
                    operation, parameters = self._transfer("cancel", job)
                    self._request(operation, **parameters)
                except Exception:
                    self._update(identity, status="paused", message="Отмена не подтверждена. Повторите отмену")
                    raise
            self._commit(lambda state: state.update(jobs=[job for job in state["jobs"] if job["id"] != identity]))
        return self._submit(remove)

    @staticmethod
    def _transfer(operation, job):
        checkpoint = {key: job[key] for key in CHECKPOINT}
        if job.get("directory") is not None:
            checkpoint["directory"] = job["directory"]
            return operation + "_directory", {"directory_job": checkpoint}
        return operation, {"job": checkpoint}

    def clear_finished(self):
        return self._submit(lambda: self._commit(lambda state: state.update(
            jobs=[job for job in state["jobs"] if job["status"] != "done"])))

    def new_link(self, identity):
        def share():
            if not self.token or self._job(identity)["status"] not in ("done", "uploaded", "uncertain"):
                raise ClientError("Подключите хранилище и завершите загрузку файла")
            self._share(identity)
        return self._submit(share)

    def _revoke_share(self, share_id):
        if not self.token:
            raise ClientError("Подключите тот же сервер и аккаунт для отзыва ссылки")
        self._request("revoke_share", share_id=share_id)
        settings = self.state["settings"]
        entry = next((entry for entry in self.history.entries if entry["origin"] == settings["origin"]
                      and entry["owner"] == settings["owner"] and entry["share_id"] == share_id), None)
        with self._lock:
            for job in self.state["jobs"]:
                if job["share_id"] == share_id:
                    job.update(link="", share_id="")
        try:
            try:
                with self._lock:
                    self.store.save(self.state)
            finally:
                if entry:
                    self.history.revoked(entry)
        except Exception:
            raise ClientError("Ссылка отозвана на сервере, но локальная история не сохранена. Проверьте Secret Service и диск") from None
        self.notice = "Ссылка отозвана"

    def revoke(self, identity):
        return self._submit(lambda: self._revoke_share(self._job(identity)["share_id"]))

    def history_link(self, identity, callback):
        def read():
            settings = self.state["settings"]
            entry = self.history.find(settings["origin"], settings["owner"], identity)
            url = self.history.link(entry)
            self.dispatch(lambda: callback(url))
        return self._submit(read)

    def revoke_history(self, identity):
        def revoke():
            settings = self.state["settings"]
            entry = self.history.find(settings["origin"], settings["owner"], identity)
            if entry["revoked"]:
                raise ClientError("Ссылка уже отозвана")
            self._revoke_share(identity)
        return self._submit(revoke)

    def close(self):
        with self._lock:
            if self._close_future is not None:
                return self._close_future
            self._closing = True
            self._pair_stop.set()
            if self._active:
                self._active.cancel()
            try:
                self.pause_all()
            except Exception as error:
                # A previously durable uploading checkpoint recovers as paused.
                # Disk failure must not prevent terminating the live helper.
                self.notice = safe_message(error)
            self._close_future = self._executor.submit(lambda: None)
        self._executor.shutdown(wait=False)
        return self._close_future
