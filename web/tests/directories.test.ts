import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { directoryFromFiles, readDrop, validateDirectory } from '../src/directories';
import type { DirectorySelection } from '../src/directories';
import { UploadManager } from '../src/uploads';

class StorageFixture implements Storage {
  private data = new Map<string, string>();
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
const parent = 'p'.repeat(43);
const selection = (): DirectorySelection => ({
  name: 'Проект 日本語',
  folders: ['a', 'b', 'empty', 'empty/nested'],
  completeTree: true,
  files: [
    { path: 'a/report.txt', file: new File(['first'], 'report.txt', { lastModified: 123 }) },
    { path: 'b/report.txt', file: new File(['second'], 'report.txt', { lastModified: 123 }) },
    { path: 'zero.txt', file: new File([], 'zero.txt', { lastModified: 123 }) },
  ],
});
function serverFixture() {
  let counter = 0;
  const folders = new Map<string, { id: string; kind: string; name: string; parent_id: string }>();
  const keys = new Map<string, string>();
  const uploads = new Map<
    string,
    { id: string; parent_id: string; size: number; data: number[]; name: string }
  >();
  let shares = 0,
    loseFolder = false,
    losePatch = false,
    loseShare = false;
  const id = () => `id${++counter}`.padEnd(43, 'x');
  const transport = vi.fn(async (path: string, init: RequestInit = {}) => {
    const headers = new Headers(init.headers);
    if (path === '/api/v1/folders') {
      const input = JSON.parse(init.body as string);
      const key = headers.get('Idempotency-Key')!;
      let node = folders.get(keys.get(key) ?? '');
      if (!node) {
        node = { ...input, id: id(), kind: 'folder' };
        folders.set(node!.id, node!);
        keys.set(key, node!.id);
      }
      if (loseFolder) {
        loseFolder = false;
        throw new TypeError('Lost creation');
      }
      return Response.json(node, { status: 201 });
    }
    if (path.startsWith('/api/v1/nodes/')) {
      const nodeID = path.split('/').at(-1)!;
      if (init.method === 'DELETE') {
        folders.delete(nodeID);
        return new Response(null, { status: 204 });
      }
      const file = [...uploads.values()].find((upload) => upload.id === nodeID);
      return Response.json(folders.get(nodeID) ?? { ...file, kind: 'file' });
    }
    if (path === '/api/v1/shares') {
      expect([...uploads.values()].every((file) => file.data.length === file.size)).toBe(true);
      shares++;
      if (loseShare) throw new TypeError('Lost share');
      return Response.json({ url: `https://public.example/s/${'s'.repeat(43)}` });
    }
    if (path === '/api/v1/uploads') {
      const key = headers.get('Idempotency-Key')!;
      let path = keys.get(key);
      if (!path) {
        path = `/api/v1/uploads/${id()}`;
        const metadata = Object.fromEntries(
          headers
            .get('Upload-Metadata')!
            .split(',')
            .map((field) => {
              const [key, value] = field.split(' ');
              return [
                key,
                new TextDecoder().decode(Uint8Array.from(atob(value), (c) => c.charCodeAt(0))),
              ];
            }),
        );
        uploads.set(path, {
          id: id(),
          parent_id: metadata.folder_id,
          name: metadata.filename,
          size: Number(headers.get('Upload-Length')),
          data: [],
        });
        keys.set(key, path);
      }
      return new Response(null, { status: 201, headers: { Location: path } });
    }
    const file = uploads.get(path)!;
    if (init.method === 'DELETE') {
      uploads.delete(path);
      return new Response(null, { status: 204 });
    }
    if (init.method === 'HEAD')
      return new Response(null, {
        headers: {
          'Upload-Offset': String(file.data.length),
          'Upload-Length': String(file.size),
          'Sharefile-File-ID': file.id,
          'Sharefile-Chunk-Size': '4',
          'Sharefile-Upload-State': file.data.length === file.size ? 'complete' : 'open',
        },
      });
    expect(Number(headers.get('Upload-Offset'))).toBe(file.data.length);
    file.data.push(...new Uint8Array(await (init.body as Blob).arrayBuffer()));
    if (losePatch) {
      losePatch = false;
      throw new TypeError('Lost PATCH');
    }
    return new Response(null, {
      status: 204,
      headers: { 'Upload-Offset': String(file.data.length), 'Sharefile-File-ID': file.id },
    });
  });
  return {
    transport,
    folders,
    uploads,
    get shares() {
      return shares;
    },
    loseFolder() {
      loseFolder = true;
    },
    losePatch() {
      losePatch = true;
    },
    loseShare() {
      loseShare = true;
    },
  };
}
beforeEach(() => vi.stubGlobal('window', { location: { origin: 'https://private.example' } }));
afterEach(() => vi.unstubAllGlobals());
describe('Whole-directory uploads', () => {
  it('preserves empty folders, duplicate basenames and one link after all files', async () => {
    const server = serverFixture(),
      storage = new StorageFixture();
    const manager = new UploadManager('alice', vi.fn(), server.transport, storage);
    await manager.addDirectory(selection(), parent, 'Uploads');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('complete'));
    expect(manager.getSnapshot()).toHaveLength(1);
    expect(server.folders.size).toBe(5);
    const files = [...server.uploads.values()];
    expect(files.map((file) => new TextDecoder().decode(new Uint8Array(file.data)))).toEqual([
      'first',
      'second',
      '',
    ]);
    expect(files[0].parent_id).not.toBe(files[1].parent_id);
    expect(server.shares).toBe(1);
    expect(manager.getSnapshot()[0].link).toContain('/s/');
    expect(storage.getItem('sharefile:uploads:v1:alice')).toBe('[]');
  });
  it('recovers lost folder and PATCH responses across reload without creating duplicates', async () => {
    const server = serverFixture(),
      storage = new StorageFixture();
    const manager = new UploadManager('alice', vi.fn(), server.transport, storage);
    server.loseFolder();
    await manager.addDirectory(selection(), parent, 'Uploads');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('error'));
    server.losePatch();
    manager.resume(manager.getSnapshot()[0].id);
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('error'));
    const reloaded = new UploadManager('alice', vi.fn(), server.transport, storage);
    const wrong = selection();
    wrong.files[0].file = new File(['wrong'], 'report.txt', { lastModified: 123 });
    await expect(reloaded.reselectDirectory(reloaded.getSnapshot()[0].id, wrong)).rejects.toThrow(
      'изменился',
    );
    await reloaded.addDirectory(selection(), parent, 'Uploads');
    await vi.waitFor(() => expect(reloaded.getSnapshot()[0].status).toBe('complete'));
    expect(server.folders.size).toBe(5);
    expect(server.uploads.size).toBe(3);
    expect(server.shares).toBe(1);
  });
  it('does not retry a lost share response, including after reload', async () => {
    const server = serverFixture(),
      storage = new StorageFixture();
    server.loseShare();
    const manager = new UploadManager('alice', vi.fn(), server.transport, storage);
    await manager.addDirectory({ name: 'empty', folders: [], files: [] }, parent, 'Uploads');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('uncertain'));
    const reloaded = new UploadManager('alice', vi.fn(), server.transport, storage);
    reloaded.resume(reloaded.getSnapshot()[0].id);
    expect(reloaded.getSnapshot()[0].status).toBe('uncertain');
    expect(server.shares).toBe(1);
  });
  it('cancels a folder whose creation response was lost using the persisted key', async () => {
    const server = serverFixture();
    server.loseFolder();
    const manager = new UploadManager('alice', vi.fn(), server.transport, new StorageFixture());
    await manager.addDirectory({ name: 'empty', folders: [], files: [] }, parent, 'Uploads');
    await vi.waitFor(() => expect(manager.getSnapshot()[0].status).toBe('error'));
    await manager.remove(manager.getSnapshot()[0].id);
    expect(manager.getSnapshot()).toEqual([]);
    expect(server.folders.size).toBe(0);
  });
  it('rejects path traversal and preserves paths from directory inputs', () => {
    expect(() =>
      validateDirectory({
        name: 'root',
        folders: [],
        files: [{ path: '../outside', file: new File([], 'outside') }],
      }),
    ).toThrow();
    const file = new File(['text'], 'report.txt');
    Object.defineProperty(file, 'webkitRelativePath', { value: 'root/nested/report.txt' });
    expect(directoryFromFiles([file])).toEqual({
      name: 'root',
      folders: ['nested'],
      files: [{ path: 'nested/report.txt', file }],
    });
  });
  it('ignores malformed directory checkpoints without breaking the queue', () => {
    const storage = new StorageFixture();
    storage.setItem(
      'sharefile:uploads:v1:alice',
      JSON.stringify([
        {
          id: 'saved',
          key: 'saved-key',
          name: 'root',
          size: 0,
          modified: 0,
          fingerprint: '',
          folder: parent,
          offset: 0,
          status: 'paused',
          directory: { folders: [], files: 'invalid' },
        },
      ]),
    );
    expect(
      new UploadManager('alice', vi.fn(), serverFixture().transport, storage).getSnapshot(),
    ).toEqual([]);
  });
  it('reads every entry across batches and captures an empty dropped directory', async () => {
    const leaves = Array.from({ length: 105 }, (_, i) => ({
      name: `file-${i}.txt`,
      isFile: true,
      isDirectory: false,
      file(resolve: (file: File) => void) {
        resolve(new File([], this.name));
      },
    }));
    let reads = 0;
    const directory = {
      name: 'root',
      isFile: false,
      isDirectory: true,
      createReader: () => ({
        readEntries: (resolve: (entries: unknown[]) => void) =>
          resolve(reads++ === 0 ? leaves.slice(0, 100) : reads === 2 ? leaves.slice(100) : []),
      }),
    };
    const empty = {
      name: 'empty',
      isDirectory: true,
      createReader: () => ({ readEntries: (resolve: (entries: unknown[]) => void) => resolve([]) }),
    };
    const dropped = {
      items: [directory, empty].map((entry) => ({
        kind: 'file',
        webkitGetAsEntry: () => entry,
        getAsFile: () => null,
      })),
      files: [],
    };
    const result = await readDrop(dropped as unknown as DataTransfer);
    expect(result.directories[0].files).toHaveLength(105);
    expect(result.directories[1]).toMatchObject({ name: 'empty', files: [], folders: [] });
  });
});
