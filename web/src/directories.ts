import { validName } from './utils';

export type DirectorySelection = {
  name: string;
  folders: string[];
  files: { path: string; file: File }[];
  completeTree?: boolean;
};
export const directoryLimit = 10000;
export function validateDirectory(selection: DirectorySelection) {
  if (!validName(selection.name)) throw new Error('Недопустимое имя каталога.');
  if (selection.folders.length + selection.files.length > directoryLimit)
    throw new Error('В каталоге больше 10000 объектов. Разделите его на несколько папок.');
  const paths = new Set<string>();
  for (const path of [...selection.folders, ...selection.files.map((entry) => entry.path)]) {
    const parts = path.split('/');
    if (parts.length > 128 || parts.some((part) => !validName(part)) || paths.has(path))
      throw new Error('Недопустимый или повторяющийся путь в каталоге.');
    paths.add(path);
  }
  const folders = new Set(['', ...selection.folders]);
  for (const path of paths) {
    if (!folders.has(path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : ''))
      throw new Error('В каталоге отсутствует родительская папка.');
  }
  for (const entry of selection.files) {
    if (entry.path.split('/').at(-1) !== entry.file.name || !Number.isSafeInteger(entry.file.size))
      throw new Error('Недопустимое имя или размер файла.');
  }
}
export function directoryFromFiles(files: File[]): DirectorySelection {
  const name = files[0]?.webkitRelativePath.split('/')[0];
  if (!name) throw new Error('Выберите каталог. Пустой каталог можно перетащить из Finder.');
  const folders = new Set<string>();
  const entries = files.map((file) => {
    const [root, ...parts] = file.webkitRelativePath.split('/');
    if (root !== name || parts.length === 0) throw new Error('Выберите один каталог.');
    for (let i = 1; i < parts.length; i++) folders.add(parts.slice(0, i).join('/'));
    return { path: parts.join('/'), file };
  });
  const selection = { name, folders: [...folders], files: entries };
  validateDirectory(selection);
  return selection;
}
async function readDirectory(entry: FileSystemDirectoryEntry): Promise<DirectorySelection> {
  const selection: DirectorySelection = {
    name: entry.name,
    folders: [],
    files: [],
    completeTree: true,
  };
  async function walk(entry: FileSystemEntry, path: string) {
    if (
      path.split('/').length > 128 ||
      selection.folders.length + selection.files.length >= directoryLimit
    )
      throw new Error('Каталог слишком велик или содержит слишком много уровней вложенности.');
    if (entry.isFile) {
      const file = await new Promise<File>((resolve, reject) =>
        (entry as FileSystemFileEntry).file(resolve, reject),
      );
      selection.files.push({ path, file });
    } else if (entry.isDirectory) {
      if (path) selection.folders.push(path);
      const reader = (entry as FileSystemDirectoryEntry).createReader();
      // Chromium returns entries in batches of at most 100; read until the empty batch.
      while (true) {
        const children = await new Promise<FileSystemEntry[]>((resolve, reject) =>
          reader.readEntries(resolve, reject),
        );
        if (!children.length) break;
        for (const child of children)
          await walk(child, path ? `${path}/${child.name}` : child.name);
      }
    } else throw new Error('Не удалось прочитать объект каталога.');
  }
  await walk(entry, '');
  validateDirectory(selection);
  return selection;
}
export function readDrop(data: DataTransfer) {
  // Capture entries synchronously: the browser protects DataTransfer after the drop handler returns.
  const items = Array.from(data.items).filter((item) => item.kind === 'file');
  const entries = items.map((item) => ({
    entry: item.webkitGetAsEntry?.(),
    file: item.getAsFile(),
  }));
  const fallback = Array.from(data.files);
  return (async () => {
    const files: File[] = [];
    const directories: DirectorySelection[] = [];
    if (!entries.length) return { files: fallback, directories };
    for (const item of entries) {
      if (item.entry?.isDirectory)
        directories.push(await readDirectory(item.entry as FileSystemDirectoryEntry));
      else if (item.file) files.push(item.file);
      else
        throw new Error('Браузер не смог прочитать каталог. Используйте кнопку «Загрузить папку».');
    }
    return { files, directories };
  })();
}
