import { request } from './api';
import { errorMessage } from './api';
import { validateDirectory } from './directories';
import type { DirectorySelection } from './directories';
import { validName } from './utils';

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
  status:
    'queued' | 'uploading' | 'paused' | 'reselect' | 'error' | 'complete' | 'sharing' | 'uncertain';
  error?: string;
  directory?: {
    folders: { path: string; key: string; id?: string }[];
    files: { path: string; job: UploadJob }[];
  };
  // Public links are kept in memory only, never in checkpoints.
  link?: string;
};
type Transport = (path: string, init?: RequestInit) => Promise<Response>;

function validSavedDirectory(value: unknown) {
  if (!value || typeof value !== 'object') return false;
  const directory = value as NonNullable<UploadJob['directory']>;
  if (
    !Array.isArray(directory.folders) ||
    !Array.isArray(directory.files) ||
    directory.folders.length + directory.files.length > 10000
  )
    return false;
  const validPath = (path: unknown): path is string =>
    typeof path === 'string' && path.split('/').length <= 128 && path.split('/').every(validName);
  const identifier = /^[A-Za-z0-9_-]{43}$/;
  if (
    !directory.folders.every(
      (folder) =>
        folder &&
        validPath(folder.path) &&
        typeof folder.key === 'string' &&
        (!folder.id || identifier.test(folder.id)),
    )
  )
    return false;
  if (
    !directory.files.every(
      (entry) =>
        entry &&
        validPath(entry.path) &&
        entry.job &&
        typeof entry.job.id === 'string' &&
        typeof entry.job.key === 'string' &&
        entry.job.name === entry.path.split('/').at(-1) &&
        Number.isSafeInteger(entry.job.size) &&
        entry.job.size >= 0 &&
        Number.isSafeInteger(entry.job.offset) &&
        entry.job.offset >= 0 &&
        entry.job.offset <= entry.job.size &&
        Number.isFinite(entry.job.modified) &&
        typeof entry.job.fingerprint === 'string' &&
        typeof entry.job.folder === 'string' &&
        (!entry.job.path || /^\/api\/v1\/uploads\/[A-Za-z0-9_-]{43}$/.test(entry.job.path)) &&
        (!entry.job.fileID || identifier.test(entry.job.fileID)) &&
        !entry.job.directory,
    )
  )
    return false;
  const paths = [
    ...directory.folders.map((folder) => folder.path),
    ...directory.files.map((entry) => entry.path),
  ];
  const folders = new Set(['', ...directory.folders.map((folder) => folder.path)]);
  return (
    new Set(paths).size === paths.length &&
    paths.every((path) =>
      folders.has(path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : ''),
    )
  );
}

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
              (!job.directory || validSavedDirectory(job.directory)) &&
              (!job.fileID || /^[A-Za-z0-9_-]{43}$/.test(job.fileID)) &&
              (!job.path || /^\/api\/v1\/uploads\/[A-Za-z0-9_-]{43}$/.test(job.path)),
          )
          .slice(-100)
          .filter((job) => job.status !== 'complete')
          .map((job) => ({
            ...job,
            status:
              job.status === 'sharing' || job.status === 'uncertain' ? 'uncertain' : 'reselect',
            offset: Number.isSafeInteger(job.offset)
              ? Math.max(0, Math.min(job.offset, job.size))
              : 0,
            error:
              job.status === 'sharing' || job.status === 'uncertain'
                ? 'Папка загружена. Проверьте ссылки этой папки в Web UI перед созданием новой.'
                : undefined,
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
        JSON.stringify(
          this.jobs
            .filter((job) => job.status !== 'complete')
            .map((job) => ({ ...job, link: undefined })),
        ),
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
  async addDirectory(selection: DirectorySelection, folder: string, folderName: string) {
    validateDirectory(selection);
    const saved = this.jobs.find(
      (job) =>
        job.directory &&
        job.status === 'reselect' &&
        job.folder === folder &&
        job.name === selection.name,
    );
    if (saved) {
      await this.reselectDirectory(saved.id, selection);
      return;
    }
    if (this.jobs.filter((job) => job.status !== 'complete').length >= 100)
      throw new Error('В очереди уже 100 загрузок. Завершите или отмените часть загрузок.');
    const entries: { path: string; job: UploadJob }[] = [];
    let size = 0;
    for (const entry of selection.files) {
      size += entry.file.size;
      if (!Number.isSafeInteger(size)) throw new Error('Суммарный размер каталога слишком велик.');
      entries.push({
        path: entry.path,
        job: {
          id: crypto.randomUUID(),
          key: crypto.randomUUID(),
          name: entry.file.name,
          sourceName: entry.file.name,
          size: entry.file.size,
          modified: entry.file.lastModified,
          fingerprint: await fingerprint(entry.file),
          folder,
          folderName,
          offset: 0,
          status: 'queued',
        },
      });
    }
    const job: UploadJob = {
      id: crypto.randomUUID(),
      key: crypto.randomUUID(),
      name: selection.name,
      size,
      modified: 0,
      fingerprint: '',
      folder,
      folderName,
      offset: 0,
      status: 'queued',
      directory: {
        folders: selection.folders
          .map((path) => ({ path, key: crypto.randomUUID() }))
          .sort((a, b) => a.path.split('/').length - b.path.split('/').length),
        files: entries,
      },
    };
    entries.forEach((entry, i) => this.files.set(entry.job.id, selection.files[i].file));
    this.jobs.push(job);
    this.suspended = false;
    this.emit();
    void this.pump();
  }
  async reselectDirectory(id: string, selection: DirectorySelection) {
    validateDirectory(selection);
    const job = this.jobs.find((job) => job.id === id);
    if (!job?.directory) return;
    if (job.name !== selection.name || job.directory.files.length !== selection.files.length)
      throw new Error('Выберите тот же исходный каталог.');
    if (
      selection.completeTree &&
      JSON.stringify([...selection.folders].sort()) !==
        JSON.stringify(job.directory.folders.map((folder) => folder.path).sort())
    )
      throw new Error('Структура исходного каталога изменилась. Добавьте его заново.');
    const sources = new Map(selection.files.map((entry) => [entry.path, entry.file]));
    for (const entry of job.directory.files) {
      const file = sources.get(entry.path);
      if (
        !file ||
        file.size !== entry.job.size ||
        file.lastModified !== entry.job.modified ||
        (await fingerprint(file)) !== entry.job.fingerprint
      )
        throw new Error(
          'Исходный каталог изменился. Выберите тот же каталог или добавьте его заново.',
        );
    }
    for (const entry of job.directory.files) this.files.set(entry.job.id, sources.get(entry.path)!);
    this.resume(id);
  }
  private hasSource(job: UploadJob) {
    return job.directory
      ? job.directory.files.every(
          (entry) => entry.job.status === 'complete' || this.files.has(entry.job.id),
        )
      : this.files.has(job.id);
  }
  pause(id: string) {
    const active = this.jobs.some((job) => job.id === id && job.status === 'uploading');
    this.update(id, { status: 'paused' });
    if (active) this.controller?.abort();
  }
  resume(id: string) {
    const job = this.jobs.find((job) => job.id === id);
    if (!job || ['sharing', 'uncertain'].includes(job.status)) return;
    this.suspended = false;
    this.update(id, { status: this.hasSource(job) ? 'queued' : 'reselect', error: undefined });
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
    if (job.directory && !['complete', 'uncertain'].includes(job.status)) {
      const root = job.fileID ?? (await this.createFolder(job.folder, job.name, job.key));
      const folders = new Map([
        ['', root],
        ...job.directory.folders
          .filter((folder) => folder.id)
          .map((folder) => [folder.path, folder.id!] as [string, string]),
      ]);
      for (const entry of job.directory.files) {
        const parent = entry.path.includes('/')
          ? entry.path.slice(0, entry.path.lastIndexOf('/'))
          : '';
        if (
          entry.job.status !== 'complete' &&
          folders.has(parent) &&
          entry.job.folder === folders.get(parent)
        ) {
          const path = entry.job.path ?? (await this.createUpload(entry.job));
          await this.transport(path, {
            method: 'DELETE',
            headers: { 'Tus-Resumable': '1.0.0' },
          }).catch((error) => {
            if (!(error instanceof Error && 'status' in error && error.status === 404)) throw error;
          });
        }
      }
      await this.transport(`/api/v1/nodes/${root}`, { method: 'DELETE' });
    } else if (job.path && job.status !== 'complete')
      await this.transport(job.path, {
        method: 'DELETE',
        headers: { 'Tus-Resumable': '1.0.0' },
      }).catch((error) => {
        if (!(error instanceof Error && 'status' in error && error.status === 404)) throw error;
      });
    this.jobs = this.jobs.filter((job) => job.id !== id);
    this.files.delete(id);
    job.directory?.files.forEach((entry) => this.files.delete(entry.job.id));
    this.emit();
  }
  private async pump() {
    if (this.running || this.suspended) return;
    const job = this.jobs.find((job) => job.status === 'queued' && this.hasSource(job));
    if (!job) return;
    this.running = true;
    this.controller = new AbortController();
    const signal = this.controller.signal;
    this.update(job.id, { status: 'uploading', error: undefined });
    try {
      if (job.directory) {
        await this.uploadDirectory(job, signal);
        return;
      }
      await this.uploadFile(job, signal, (patch) => this.update(job.id, patch));
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
  private async uploadFile(
    job: UploadJob,
    signal: AbortSignal,
    update: (patch: Partial<UploadJob>) => void,
  ) {
    let path = job.path;
    const headers = { 'Tus-Resumable': '1.0.0' };
    if (!path) {
      path = await this.createUpload(job, signal);
      update({ path });
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
    update({ offset, fileID: head.headers.get('Sharefile-File-ID') ?? undefined });
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
      update({
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
      throw new Error('Сервер ещё не завершил сохранение файла. Продолжите загрузку для проверки.');
    update({ status: 'complete' });
  }
  private async uploadDirectory(job: UploadJob, signal: AbortSignal) {
    const directory = job.directory!;
    if (!job.fileID) {
      job.fileID = await this.createFolder(job.folder, job.name, job.key, signal);
      this.update(job.id, { fileID: job.fileID });
      this.onComplete();
    }
    const ids = new Map([['', job.fileID]]);
    await this.verifyNode(job.fileID, job.folder, 'folder', signal);
    const parentPath = (path: string) =>
      path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '';
    for (const folder of directory.folders) {
      if (!folder.id) {
        folder.id = await this.createFolder(
          ids.get(parentPath(folder.path))!,
          folder.path.split('/').at(-1)!,
          folder.key,
          signal,
        );
        this.emit();
      }
      await this.verifyNode(folder.id, ids.get(parentPath(folder.path))!, 'folder', signal);
      ids.set(folder.path, folder.id);
    }
    for (const entry of directory.files) {
      if (entry.job.status === 'complete') {
        await this.verifyNode(entry.job.fileID!, ids.get(parentPath(entry.path))!, 'file', signal);
        continue;
      }
      entry.job.folder = ids.get(parentPath(entry.path))!;
      await this.uploadFile(entry.job, signal, (patch) => {
        Object.assign(entry.job, patch);
        this.update(job.id, {
          offset: directory.files.reduce((sum, entry) => sum + entry.job.offset, 0),
        });
      });
      this.files.delete(entry.job.id);
    }
    if (signal.aborted) throw new DOMException('Paused', 'AbortError');
    await this.verifyNode(job.fileID, job.folder, 'folder', signal);
    // Persist before sending: a lost response must never trigger an automatic duplicate share.
    this.update(job.id, { status: 'sharing' });
    try {
      const response = await this.transport('/api/v1/shares', {
        method: 'POST',
        signal,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          node_id: job.fileID,
          expires_at: Math.floor(Date.now() / 1000) + 7 * 86400,
        }),
      });
      const result = (await response.json()) as { url: string };
      const link = new URL(result.url);
      if (
        !['http:', 'https:'].includes(link.protocol) ||
        link.username ||
        link.password ||
        !/^\/s\/[A-Za-z0-9_-]{43}$/.test(link.pathname) ||
        link.search ||
        link.hash
      )
        throw new Error('Сервер вернул некорректную ссылку.');
      this.update(job.id, { status: 'complete', link: result.url });
    } catch {
      this.update(job.id, {
        status: 'uncertain',
        error:
          'Папка загружена. Ответ о создании ссылки не получен; проверьте ссылки этой папки в Web UI перед созданием новой.',
      });
    }
    this.onComplete();
  }
  private async createFolder(parent: string, name: string, key: string, signal?: AbortSignal) {
    const response = await this.transport('/api/v1/folders', {
      method: 'POST',
      signal,
      headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
      body: JSON.stringify({ parent_id: parent, name }),
    });
    const node = (await response.json()) as { id: string };
    if (!/^[A-Za-z0-9_-]{43}$/.test(node.id))
      throw new Error('Сервер вернул некорректный каталог.');
    return node.id;
  }
  private async createUpload(job: UploadJob, signal?: AbortSignal) {
    const metadata = `filename ${base64(job.name)},folder_id ${base64(job.folder)}${job.replace ? `,replace_id ${base64(job.replace)}` : ''}`;
    const response = await this.transport('/api/v1/uploads', {
      method: 'POST',
      signal,
      headers: {
        'Tus-Resumable': '1.0.0',
        'Upload-Length': String(job.size),
        'Upload-Metadata': metadata,
        'Idempotency-Key': job.key,
      },
    });
    return uploadPath(response.headers.get('Location') ?? '');
  }
  private async verifyNode(id: string, parent: string, kind: string, signal: AbortSignal) {
    const response = await this.transport(`/api/v1/nodes/${id}`, { signal });
    const node = (await response.json()) as { parent_id: string; kind: string };
    if (node.parent_id !== parent || node.kind !== kind)
      throw new Error(
        'Загружаемая папка или её содержимое перемещены. Проверьте папку в хранилище.',
      );
  }
}
