import { bytes } from './utils';

// Only confirmed bytes from the current run enter the rate; recovered HEAD offsets do not.
export class TransferMeter {
  private samples: { time: number; bytes: number }[] = [];
  constructor(private now = () => performance.now() / 1000) {}
  start() {
    this.samples = [{ time: this.now(), bytes: 0 }];
  }
  record(bytes: number) {
    const last = this.samples.at(-1);
    if (!last || bytes < last.bytes) this.start();
    if (bytes !== this.samples.at(-1)?.bytes) this.samples.push({ time: this.now(), bytes });
    this.trim();
  }
  private trim() {
    const cutoff = this.now() - 10;
    while (this.samples.length > 2 && this.samples[1].time <= cutoff) this.samples.shift();
  }
  rate() {
    this.trim();
    const first = this.samples[0],
      last = this.samples.at(-1),
      now = this.now();
    if (!first || !last || now - last.time >= 10 || now <= first.time) return 0;
    return Math.max(0, (last.bytes - first.bytes) / (now - first.time));
  }
}

type ProgressJob = { id: string; batch?: string; size: number; offset: number; status: string };
export function selectionJobs<T extends ProgressJob>(jobs: T[]): T[] {
  const active = new Set(
    jobs.filter((job) => job.status !== 'complete').map((job) => job.batch ?? job.id),
  );
  if (!active.size && jobs.length) active.add(jobs.at(-1)!.batch ?? jobs.at(-1)!.id);
  return jobs.filter((job) => active.has(job.batch ?? job.id));
}

export function progressSummary(jobs: ProgressJob[], rate: number) {
  const size = jobs.reduce((sum, job) => sum + job.size, 0);
  const offset = jobs.reduce((sum, job) => sum + Math.max(0, Math.min(job.offset, job.size)), 0);
  const completed = jobs.filter((job) => job.status === 'complete').length;
  const blocked = jobs.some(
    (job) => !['queued', 'uploading', 'sharing', 'complete'].includes(job.status),
  );
  const percent = size ? Math.floor((offset / size) * 100) : completed === jobs.length ? 100 : 0;
  const seconds = rate > 0 && !blocked && size > offset ? Math.ceil((size - offset) / rate) : null;
  return { size, offset, completed, percent, seconds };
}

export function remainingTime(seconds: number) {
  const rounded = Math.max(1, Math.ceil(seconds));
  if (rounded < 60) return `${rounded} с`;
  if (rounded < 3600) return `${Math.ceil(rounded / 60)} мин`;
  const minutes = Math.ceil(rounded / 60);
  return `${Math.floor(minutes / 60)} ч${minutes % 60 ? ` ${minutes % 60} мин` : ''}`;
}

export function transferDetails(rate: number, seconds: number | null) {
  return rate > 0
    ? `${bytes(rate)}/с · ${seconds === null ? 'Оставшееся время —' : `Осталось ≈ ${remainingTime(seconds)}`}`
    : 'Скорость — · Оцениваем оставшееся время…';
}
