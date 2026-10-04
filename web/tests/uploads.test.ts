import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { UploadManager, uploadPath } from '../src/uploads';

class MemoryStorage implements Storage {
  data = new Map<string, string>();
  get length() {
    return this.data.size;
  }
  clear() {
    this.data.clear();
  }
  getItem(key: string) {
    return this.data.get(key) ?? null;
  }
  key(index: number) {
    return [...this.data.keys()][index] ?? null;
  }
  removeItem(key: string) {
    this.data.delete(key);
  }
  setItem(key: string, value: string) {
    this.data.set(key, value);
  }
}
const uploadID = 'u'.repeat(43),
  fileID = 'f'.repeat(43),
  folderID = 'd'.repeat(43);
function fixture() {
  let offset = 0,
    exists = false,
    creations = 0,
    lostCreate = false,
    lostPatch = false;
  const data: number[] = [];
  const offsets: number[] = [];
  const keys: string[] = [];
  let length = 0;
  const transport = vi.fn(async (path: string, init: RequestInit = {}) => {
    const headers = new Headers(init.headers);
    if (path === '/api/v1/uploads') {
      expect(init.method).toBe('POST');
      length = Number(headers.get('Upload-Length'));
      keys.push(headers.get('Idempotency-Key')!);
      if (!exists) {
        exists = true;
        creations++;
      }
      if (lostCreate) {
        lostCreate = false;
        throw new TypeError('Connection lost');
      }
      return new Response(null, {
        status: 201,
        headers: { Location: `https://public.example/api/v1/uploads/${uploadID}` },
      });
    }
    expect(path).toBe(`/api/v1/uploads/${uploadID}`);
    if (init.method === 'HEAD')
      return new Response(null, {
        headers: {
          'Upload-Offset': String(offset),
          'Upload-Length': String(length),
          'Sharefile-Chunk-Size': '4',
          'Sharefile-File-ID': fileID,
          'Sharefile-Upload-State': offset === length ? 'complete' : 'open',
        },
      });
    if (init.method === 'DELETE') return new Response(null, { status: 204 });
    expect(headers.get('X-HTTP-Method-Override')).toBe('PATCH');
    expect(headers.get('Tus-Resumable')).toBe('1.0.0');
    expect(Number(headers.get('Upload-Offset'))).toBe(offset);
    offsets.push(offset);
    const chunk = new Uint8Array(await (init.body as Blob).arrayBuffer());
    data.push(...chunk);
    offset += chunk.length;
    if (lostPatch) {
      lostPatch = false;
      throw new TypeError('Response lost');
    }
    return new Response(null, {
      status: 204,
      headers: { 'Upload-Offset': String(offset), 'Sharefile-File-ID': fileID },
    });
  });
  return {
    transport,
    data,
    offsets,
    keys,
    get creations() {
      return creations;
    },
    loseCreation() {
      lostCreate = true;
    },
    losePatch() {
      lostPatch = true;
    },
  };
}
const file = () => new File(['1234567890'], 'отчёт.txt', { lastModified: 12345 });
beforeEach(() => vi.stubGlobal('window', { location: { origin: 'https://private.example' } }));
afterEach(() => vi.unstubAllGlobals());
describe('Resumable uploads', () => {
  it('uses bounded server-sized chunks, Unicode metadata and same-origin paths', async () => {
    const server = fixture(),
      complete = vi.fn(),
      storage = new MemoryStorage();
    const manager = new UploadManager('alice', complete, server.transport, storage);
    await manager.add([file()], folderID, 'Документы');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('complete'));
    expect(server.offsets).toEqual([0, 4, 8]);
    expect(new TextDecoder().decode(new Uint8Array(server.data))).toBe('1234567890');
    const metadata = new Headers(server.transport.mock.calls[0][1]?.headers).get(
      'Upload-Metadata',
    )!;
    expect(
      new TextDecoder().decode(
        Uint8Array.from(atob(metadata.split(',')[0].split(' ')[1]), (c) => c.charCodeAt(0)),
      ),
    ).toBe('отчёт.txt');
    expect(complete).toHaveBeenCalledOnce();
    expect(storage.getItem('sharefile:uploads:v1:alice')).toBe('[]');
  });
  it('recovers a lost PATCH response using the durable HEAD offset without duplicates', async () => {
    const server = fixture();
    server.losePatch();
    const manager = new UploadManager('alice', vi.fn(), server.transport, new MemoryStorage());
    await manager.add([file()], folderID, 'Документы');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('error'));
    expect(manager.getSnapshot()[0].offset).toBe(0);
    manager.resume(manager.getSnapshot()[0].id);
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('complete'));
    expect(server.offsets).toEqual([0, 4, 8]);
    expect(server.data.length).toBe(10);
  });
  it('reuses an idempotency key when creation succeeds but its response is lost', async () => {
    const server = fixture();
    server.loseCreation();
    const manager = new UploadManager('alice', vi.fn(), server.transport, new MemoryStorage());
    await manager.add([file()], folderID, 'Документы');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('error'));
    manager.resume(manager.getSnapshot()[0].id);
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('complete'));
    expect(server.creations).toBe(1);
    expect(server.keys[0]).toBe(server.keys[1]);
  });
  it('survives reload, rejects another file and resumes from the server position', async () => {
    const server = fixture(),
      storage = new MemoryStorage();
    server.losePatch();
    const manager = new UploadManager('alice', vi.fn(), server.transport, storage);
    await manager.add([file()], folderID, 'Документы');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('error'));
    const resumed = new UploadManager('alice', vi.fn(), server.transport, storage);
    const job = resumed.getSnapshot()[0];
    expect(job.status).toBe('reselect');
    await expect(
      resumed.reselect(job.id, new File(['abcdefghij'], 'отчёт.txt', { lastModified: 12345 })),
    ).rejects.toThrow('тот же исходный файл');
    await resumed.reselect(job.id, file());
    await vi.waitFor(() => expect(resumed.getSnapshot()[0].status).toBe('complete'));
    expect(server.data.length).toBe(10);
    expect(new UploadManager('bob', vi.fn(), server.transport, storage).getSnapshot()).toEqual([]);
  });
  it('handles zero-byte files and uploads added after lifecycle suspension', async () => {
    const server = fixture(),
      manager = new UploadManager('alice', vi.fn(), server.transport, new MemoryStorage());
    manager.pauseAll();
    await manager.add([new File([], 'empty.txt')], folderID, 'Документы');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('complete'));
    expect(server.offsets).toEqual([]);
  });
  it('validates upload locations before forwarding credentials', () => {
    expect(uploadPath(`https://public.example/api/v1/uploads/${uploadID}`)).toBe(
      `/api/v1/uploads/${uploadID}`,
    );
    expect(() => uploadPath('https://evil.example/steal')).toThrow();
    expect(() => uploadPath(`/api/v1/uploads/${uploadID}?redirect=other`)).toThrow();
    expect(() => uploadPath(`https://name:password@example/api/v1/uploads/${uploadID}`)).toThrow();
  });

  it('pauses an in-flight request and can resume immediately without losing the queue', async () => {
    const server = fixture();
    let blocked = true,
      started = false;
    const transport = async (path: string, init: RequestInit = {}) => {
      if (blocked && new Headers(init.headers).get('X-HTTP-Method-Override') === 'PATCH') {
        blocked = false;
        started = true;
        await new Promise<void>((_, reject) =>
          init.signal!.addEventListener(
            'abort',
            () => reject(new DOMException('Paused', 'AbortError')),
            { once: true },
          ),
        );
      }
      return server.transport(path, init);
    };
    const manager = new UploadManager('alice', vi.fn(), transport, new MemoryStorage());
    await manager.add([file()], folderID, 'Документы');
    await vi.waitFor(() => expect(started).toBe(true));
    const id = manager.getSnapshot()[0].id;
    manager.pause(id);
    expect(manager.getSnapshot()[0].status).toBe('paused');
    manager.resume(id);
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('complete'));
    expect(server.data.length).toBe(10);
  });

  it('keeps the existing name when replacing from a differently named local file', async () => {
    const server = fixture(),
      manager = new UploadManager('alice', vi.fn(), server.transport, new MemoryStorage());
    await manager.add([file()], folderID, 'Документы', fileID, 'existing.txt');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('complete'));
    const metadata = new Headers(server.transport.mock.calls[0][1]?.headers).get(
      'Upload-Metadata',
    )!;
    expect(atob(metadata.split(',')[0].split(' ')[1])).toBe('existing.txt');
    expect(manager.getSnapshot()[0].sourceName).toBe('отчёт.txt');
  });
});
