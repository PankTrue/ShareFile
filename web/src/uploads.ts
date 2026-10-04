import { request } from './api';
import { errorMessage } from './api';

export type UploadJob = {
  id: string;
  key: string;
  name: string;
  sourceName?: string;
  size: number;
  modified: number;
  fingerprint: string;
  folder: string;
  folderName: string;
  replace?: string;
  path?: string;
  fileID?: string;
  offset: number;
  status: 'queued' | 'uploading' | 'paused' | 'reselect' | 'error' | 'complete';
  error?: string;
};
type Transport = (path: string, init?: RequestInit) => Promise<Response>;

export function uploadPath(location: string) {
  const url = new URL(location, window.location.origin);
  // Location is the public URL even on the private listener. Keep the request on the current origin.
  if (
    !/^\/api\/v1\/uploads\/[A-Za-z0-9_-]{43}$/.test(url.pathname) ||
    url.search ||
    url.hash ||
    url.username ||
    url.password
  ) {
    throw new Error('Сервер вернул некорректный адрес загрузки.');
  }
  return url.pathname;
}

function base64(value: string) {
  const encoded = new TextEncoder().encode(value);
  return btoa(Array.from(encoded, (byte) => String.fromCharCode(byte)).join(''));
}

// Read only small samples. This catches accidental reselection without hashing a multi-GB file in RAM.
export async function fingerprint(file: File) {
  const sample = 64 * 1024;
  const info = new TextEncoder().encode(JSON.stringify([file.name, file.size, file.lastModified]));
  const first = new Uint8Array(await file.slice(0, sample).arrayBuffer());
  const last = new Uint8Array(await file.slice(Math.max(sample, file.size - sample)).arrayBuffer());
  const input = new Uint8Array(info.length + first.length + last.length);
  input.set(info);
  input.set(first, info.length);
  input.set(last, info.length + first.length);
  const digest = await crypto.subtle.digest('SHA-256', input);
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('');
}

export class UploadManager {
  private jobs: UploadJob[] = [];
  private files = new Map<string, File>();
  private listeners = new Set<() => void>();
  private controller?: AbortController;
  private running = false;
  private suspended = false;
  private storageKey: string;
  private storage?: Storage;
  constructor(
    userID: string,
    private onComplete: () => void,
    private transport: Transport = request,
    storage?: Storage,
  ) {
    this.storageKey = `sharefile:uploads:v1:${userID}`;
    try {
      this.storage = storage ?? window.localStorage;
      const saved: unknown = JSON.parse(this.storage?.getItem(this.storageKey) ?? '[]');
      if (Array.isArray(saved))
        this.jobs = saved
          .filter(
            (job): job is UploadJob =>
              job &&
              typeof job.id === 'string' &&
              typeof job.key === 'string' &&
              typeof job.name === 'string' &&
              Number.isSafeInteger(job.size) &&
              job.size >= 0 &&
              typeof job.folder === 'string' &&
              typeof job.fingerprint === 'string' &&
              typeof job.modified === 'number' &&
              (!job.path || /^\/api\/v1\/uploads\/[A-Za-z0-9_-]{43}$/.test(job.path)),
          )
          .slice(-100)
          .filter((job) => job.status !== 'complete')
          .map((job) => ({
            ...job,
            status: 'reselect',
            offset: Number.isSafeInteger(job.offset)
              ? Math.max(0, Math.min(job.offset, job.size))
              : 0,
            error: undefined,
          }));
    } catch {
      /* Private mode and storage failures do not prevent uploads. */
    }
  }
  getSnapshot = () => this.jobs;
  subscribe = (callback: () => void) => {
    this.listeners.add(callback);
    return () => {
      this.listeners.delete(callback);
    };
  };
  private emit() {
    this.jobs = [...this.jobs];
    try {
      this.storage?.setItem(
        this.storageKey,
        JSON.stringify(this.jobs.filter((job) => job.status !== 'complete')),
      );
    } catch {
      /* Storage is optional. */
    }
    this.listeners.forEach((listener) => listener());
  }
  private update(id: string, patch: Partial<UploadJob>) {
    this.jobs = this.jobs.map((job) => (job.id === id ? { ...job, ...patch } : job));
    this.emit();
  }
  async add(
    files: File[],
    folder: string,
    folderName: string,
    replace?: string,
    destinationName?: string,
  ) {
    this.suspended = false;
    for (const file of files) {
      if (this.jobs.filter((job) => job.status !== 'complete').length >= 100)
        throw new Error('В очереди уже 100 файлов. Завершите или отмените часть загрузок.');
      if (!Number.isSafeInteger(file.size))
        throw new Error('Этот размер файла не поддерживается браузером.');
      const job: UploadJob = {
        id: crypto.randomUUID(),
        key: crypto.randomUUID(),
        name: destinationName ?? file.name,
        sourceName: file.name,
        size: file.size,
        modified: file.lastModified,
        fingerprint: await fingerprint(file),
        folder,
        folderName,
        replace,
        offset: 0,
        status: 'queued',
      };
      this.files.set(job.id, file);
      this.jobs.push(job);
      this.emit();
    }
    void this.pump();
  }
  async reselect(id: string, file: File) {
    const job = this.jobs.find((job) => job.id === id);
    if (!job) return;
    if (
      file.name !== (job.sourceName ?? job.name) ||
      file.size !== job.size ||
      file.lastModified !== job.modified ||
      (await fingerprint(file)) !== job.fingerprint
    ) {
      throw new Error(
        'Выберите тот же исходный файл: должны совпадать имя, размер, дата изменения и содержимое образцов.',
      );
    }
    this.files.set(id, file);
    this.resume(id);
  }
  pause(id: string) {
    const active = this.jobs.some((job) => job.id === id && job.status === 'uploading');
    this.update(id, { status: 'paused' });
    if (active) this.controller?.abort();
  }
  resume(id: string) {
    this.suspended = false;
    this.update(id, { status: this.files.has(id) ? 'queued' : 'reselect', error: undefined });
    void this.pump();
  }
  pauseAll() {
    this.suspended = true;
    this.controller?.abort();
    this.jobs = this.jobs.map((job) =>
      ['uploading', 'queued'].includes(job.status) ? { ...job, status: 'paused' } : job,
    );
    this.emit();
  }
  async remove(id: string) {
    const job = this.jobs.find((job) => job.id === id);
    if (!job) return;
    this.pause(id);
    // Wait for a current PATCH to settle before deleting its resource.
    while (this.running && this.controller?.signal.aborted)
      await new Promise((resolve) => setTimeout(resolve, 25));
    if (job.path && job.status !== 'complete')
      await this.transport(job.path, {
        method: 'DELETE',
        headers: { 'Tus-Resumable': '1.0.0' },
      }).catch((error) => {
        if (!(error instanceof Error && 'status' in error && error.status === 404)) throw error;
      });
    this.jobs = this.jobs.filter((job) => job.id !== id);
    this.files.delete(id);
    this.emit();
  }
  private async pump() {
    if (this.running || this.suspended) return;
    const job = this.jobs.find((job) => job.status === 'queued' && this.files.has(job.id));
    if (!job) return;
    this.running = true;
    this.controller = new AbortController();
    const signal = this.controller.signal;
    this.update(job.id, { status: 'uploading', error: undefined });
    try {
      let path = job.path;
      const headers = { 'Tus-Resumable': '1.0.0' };
      if (!path) {
        const metadata = `filename ${base64(job.name)},folder_id ${base64(job.folder)}${job.replace ? `,replace_id ${base64(job.replace)}` : ''}`;
        const response = await this.transport('/api/v1/uploads', {
          method: 'POST',
          signal,
          headers: {
            ...headers,
            'Upload-Length': String(job.size),
            'Upload-Metadata': metadata,
            'Idempotency-Key': job.key,
          },
        });
        path = uploadPath(response.headers.get('Location') ?? '');
        this.update(job.id, { path });
      }
      const head = await this.transport(path, { method: 'HEAD', signal, headers });
      let offset = Number(head.headers.get('Upload-Offset'));
      const length = Number(head.headers.get('Upload-Length'));
      if (
        !head.headers.has('Upload-Offset') ||
        !head.headers.has('Upload-Length') ||
        !Number.isSafeInteger(offset) ||
        offset < 0 ||
        offset > job.size ||
        length !== job.size
      )
        throw new Error('Состояние загрузки на сервере не совпадает с исходным файлом.');
      const serverChunk = Number(head.headers.get('Sharefile-Chunk-Size'));
      if (!Number.isSafeInteger(serverChunk) || serverChunk < 1)
        throw new Error('Сервер не сообщил размер части файла.');
      const chunk = Math.min(serverChunk, 4 * 1024 * 1024);
      this.update(job.id, { offset, fileID: head.headers.get('Sharefile-File-ID') ?? undefined });
      const file = this.files.get(job.id)!;
      while (offset < job.size) {
        const end = Math.min(offset + chunk, job.size);
        const response = await this.transport(path, {
          // Tus method override is supported by KeenDNS relays that restrict PATCH.
          method: 'POST',
          signal,
          headers: {
            ...headers,
            'X-HTTP-Method-Override': 'PATCH',
            'Content-Type': 'application/offset+octet-stream',
            'Upload-Offset': String(offset),
          },
          body: file.slice(offset, end),
        });
        const confirmed = Number(response.headers.get('Upload-Offset'));
        if (!response.headers.has('Upload-Offset') || confirmed !== end)
          throw new Error(
            'Сервер не подтвердил переданные данные. Возобновите загрузку для проверки позиции.',
          );
        offset = confirmed;
        this.update(job.id, {
          offset,
          fileID: response.headers.get('Sharefile-File-ID') ?? undefined,
        });
      }
      // HEAD also lets a retry finish when the last PATCH succeeded but its response was lost.
      const completed = await this.transport(path, { method: 'HEAD', signal, headers });
      if (
        completed.headers.get('Sharefile-Upload-State') !== 'complete' ||
        Number(completed.headers.get('Upload-Offset')) !== job.size
      )
        throw new Error(
          'Сервер ещё не завершил сохранение файла. Продолжите загрузку для проверки.',
        );
      this.update(job.id, { status: 'complete' });
      this.files.delete(job.id);
      this.onComplete();
    } catch (error) {
      if (!signal.aborted) this.update(job.id, { status: 'error', error: errorMessage(error) });
    } finally {
      this.running = false;
      this.controller = undefined;
      void this.pump();
    }
  }
}
