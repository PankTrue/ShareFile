"""Transient, monotonic measurements of confirmed bytes; never checkpointed."""
from .localization import tr
import math
import time


class TransferMeter:
    def __init__(self, clock=time.monotonic):
        self.clock = clock
        self.samples = [(clock(), 0)]

    def record(self, transferred):
        if transferred < self.samples[-1][1]:
            self.samples = [(self.clock(), 0)]
        if transferred != self.samples[-1][1]:
            self.samples.append((self.clock(), transferred))
        self._trim()

    def _trim(self):
        while len(self.samples) > 2 and self.samples[1][0] <= self.clock() - 10:
            self.samples.pop(0)

    def rate(self):
        self._trim()
        first, last, now = self.samples[0], self.samples[-1], self.clock()
        if now - last[0] >= 10 or now <= first[0]:
            return 0
        return max(0, (last[1] - first[1]) / (now - first[0]))


def selection_jobs(jobs):
    batches = {job.get("batch") or job["id"] for job in jobs if job["status"] != "done"}
    if not batches and jobs:
        batches.add(jobs[-1].get("batch") or jobs[-1]["id"])
    return [job for job in jobs if (job.get("batch") or job["id"]) in batches]


def summary(jobs):
    size = sum(job["size"] for job in jobs)
    offset = sum(max(0, min(job["size"], job["offset"])) for job in jobs)
    completed = sum(job["status"] == "done" for job in jobs)
    rate = sum(job.get("rate", 0) for job in jobs)
    blocked = any(job["status"] not in ("queued", "uploading", "uploaded", "sharing", "done") for job in jobs)
    seconds = math.ceil((size - offset) / rate) if rate > 0 and size > offset and not blocked else None
    percent = int(offset * 100 / size) if size else (100 if completed == len(jobs) else 0)
    if completed == len(jobs):
        detail = tr("Загрузка завершена")
    elif any(job["status"] in ("error", "uncertain", "cancelling") for job in jobs):
        detail = tr("Требуется действие · Оставшееся время —")
    elif blocked:
        detail = tr("Приостановлено · Оставшееся время —")
    elif size == offset:
        detail = tr("Данные переданы · Завершаем загрузку…")
    elif any(job["status"] == "uploading" for job in jobs):
        detail = transfer_details(rate, seconds)
    else:
        detail = tr("В очереди · Оставшееся время —")
    return dict(size=size, offset=offset, completed=completed, count=len(jobs), percent=percent, detail=detail)


def size_label(size):
    for unit in (tr("Б"), tr("КиБ"), tr("МиБ"), tr("ГиБ"), tr("ТиБ")):
        if size < 1024 or unit == tr("ТиБ"):
            return f"{size:.1f} {unit}" if unit != tr("Б") else f"{size:.0f} {unit}"
        size /= 1024


def remaining_time(seconds):
    seconds = max(1, math.ceil(seconds))
    if seconds < 60:
        return tr("{0} с", seconds)
    if seconds < 3600:
        return tr("{0} мин", math.ceil(seconds / 60))
    minutes = math.ceil(seconds / 60)
    return tr("{0} ч", minutes // 60) + (tr(" {0} мин", minutes % 60) if minutes % 60 else "")


def transfer_details(rate, seconds):
    if rate <= 0:
        return tr("Скорость — · Оцениваем оставшееся время…")
    remaining = tr("Оставшееся время —") if seconds is None else tr("Осталось ≈ {0}", remaining_time(seconds))
    return tr("{0}/с · {1}", size_label(rate), remaining)
