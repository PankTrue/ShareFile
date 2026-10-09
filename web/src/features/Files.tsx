import { useEffect, useRef, useState } from 'react';
import { api, APIError, errorMessage, idPath } from '../api';
import { useResource, useTask } from '../hooks';
import { useApp } from '../context';
import { bytes, date, roles, validName } from '../utils';
import type { FileNode, SearchNode, SearchResult } from '../types';
import { emptySearch, searchParams } from '../search';
import { Button, Confirm, ErrorBox, IconButton, Modal, PageTitle, State } from '../components/UI';
import { Icon } from '../components/Icon';
import { FolderPicker, Pager } from '../components/FolderPicker';
import { useUploads } from '../components/Uploads';
import { AccessDialog, ShareDialog } from './Sharing';
import { directoryFromFiles, readDrop } from '../directories';
import type { DirectorySelection } from '../directories';
import { FilePreview } from '../components/FilePreview';

type Dialog =
  | { kind: 'new' }
  | { kind: 'rename' | 'move' | 'delete' | 'share' | 'access' | 'replace'; node: FileNode };
export function Files({
  folderID,
  spaces = false,
  onFolder,
  uploadRevision,
  onQueue,
}: {
  folderID: string;
  spaces?: boolean;
  onFolder: (id: string) => void;
  uploadRevision: number;
  onQueue: () => void;
}) {
  const { session } = useApp();
  const uploads = useUploads();
  const [offset, setOffset] = useState(0);
  const [filters, setFilters] = useState(emptySearch);
  const [search, setSearch] = useState<string>();
  const [searchError, setSearchError] = useState('');
  const [dialog, setDialog] = useState<Dialog>();
  const [preview, setPreview] = useState<{ files: FileNode[]; index: number }>();
  const [uploadError, setUploadError] = useState('');
  const [dragging, setDragging] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const directoryInput = useRef<HTMLInputElement>(null);
  const rows = useResource<FileNode[]>(
    search === undefined
      ? `/api/v1/folders?parent_id=${idPath(folderID)}&limit=100&offset=${offset}&refresh=${uploadRevision}`
      : null,
  );
  const results = useResource<SearchResult>(
    search !== undefined
      ? `/api/v1/search?${search}&limit=100&offset=${offset}&refresh=${uploadRevision}`
      : null,
  );
  const searching = search !== undefined;
  const resource = searching ? results : rows;
  const current = useResource<FileNode>(folderID ? `/api/v1/nodes/${idPath(folderID)}` : null);
  const [trail, setTrail] = useState<FileNode[]>([]);
  const editor = folderID && (current.value?.effective_role ?? 0) >= 2;
  const manager = folderID && (current.value?.effective_role ?? 0) >= 3;
  useEffect(() => {
    const controller = new AbortController();
    setTrail([]);
    async function load() {
      let id = folderID;
      const path: FileNode[] = [];
      while (id && path.length < 32 && !controller.signal.aborted) {
        try {
          const node = await api<FileNode>(
            `/api/v1/nodes/${idPath(id)}`,
            'GET',
            undefined,
            controller.signal,
          );
          path.unshift(node);
          id = node.parent_id ?? '';
        } catch (error) {
          if (error instanceof APIError && (error.status === 403 || error.status === 404)) break;
          throw error;
        }
      }
      if (!controller.signal.aborted) setTrail(path);
    }
    void load().catch(() => {});
    return () => controller.abort();
  }, [folderID]);
  function go(id: string) {
    setOffset(0);
    setSearch(undefined);
    setFilters(emptySearch);
    setSearchError('');
    onFolder(id);
  }
  async function add(files: File[]) {
    if (!editor || !current.value) return;
    setUploadError('');
    onQueue();
    try {
      if (files.some((file) => !validName(file.name)))
        throw new Error('Имя файла содержит недопустимые символы или превышает 240 байт.');
      await uploads.add(files, folderID, current.value.name);
    } catch (e) {
      setUploadError(errorMessage(e));
    }
  }
  async function addDirectory(selection: DirectorySelection) {
    if (!current.value || !editor) return;
    if (!manager) throw new Error('Для загрузки папки с общей ссылкой нужны права управляющего.');
    onQueue();
    await uploads.addDirectory(selection, folderID, current.value.name);
  }
  const filtered = searching
    ? (results.value?.items ?? [])
    : (rows.value ?? []).filter((node) => !spaces || node.id !== session.user.home_id);
  function refresh() {
    resource.refresh();
    current.refresh();
  }
  return (
    <>
      <PageTitle
        eyebrow={spaces ? 'СОВМЕСТНАЯ РАБОТА' : 'ВАШЕ ХРАНИЛИЩЕ'}
        title={spaces ? 'Доступные папки' : (current.value?.name ?? 'Мои файлы')}
        description={
          spaces
            ? 'Личные и общие папки, к которым у вас есть доступ.'
            : 'Сохраняйте файлы на своём диске и делитесь ими по ссылке.'
        }
      >
        {folderID && current.value && (
          <a className="button secondary" href={`/api/v1/nodes/${idPath(folderID)}/archive`}>
            <Icon name="download" />
            Скачать всё (ZIP)
          </a>
        )}
        {editor && (
          <>
            <Button icon="plus" onClick={() => setDialog({ kind: 'new' })}>
              Новая папка
            </Button>
            <Button variant="primary" icon="upload" onClick={() => input.current?.click()}>
              Загрузить файлы
            </Button>
            {manager && (
              <Button icon="folder" onClick={() => directoryInput.current?.click()}>
                Загрузить папку
              </Button>
            )}
          </>
        )}
        {manager && current.value && (
          <Button icon="link" onClick={() => setDialog({ kind: 'share', node: current.value! })}>
            Поделиться папкой
          </Button>
        )}
        {!folderID && session.user.admin && (
          <Button variant="primary" icon="plus" onClick={() => setDialog({ kind: 'new' })}>
            Создать общую папку
          </Button>
        )}
      </PageTitle>
      <input
        ref={input}
        className="visually-hidden"
        type="file"
        multiple
        aria-label="Выбрать файлы для загрузки"
        onChange={(e) => {
          void add(Array.from(e.target.files ?? []));
          e.target.value = '';
        }}
      />
      <input
        ref={(node) => {
          directoryInput.current = node;
          if (node) node.webkitdirectory = true;
        }}
        className="visually-hidden"
        type="file"
        multiple
        aria-label="Выбрать папку для загрузки"
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          e.target.value = '';
          setUploadError('');
          void (async () => {
            await addDirectory(directoryFromFiles(files));
          })().catch((error) => setUploadError(errorMessage(error)));
        }}
      />
      <ErrorBox message={uploadError || current.error} />
      <section
        className={`file-card ${dragging ? 'dragging' : ''}`}
        onDragOver={(e) => {
          e.preventDefault();
          if (editor) setDragging(true);
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false);
        }}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          if (!editor) return;
          setUploadError('');
          const dropped = readDrop(e.dataTransfer);
          void dropped
            .then(async ({ files, directories }) => {
              for (const directory of directories) await addDirectory(directory);
              if (files.length) await add(files);
            })
            .catch((error) => setUploadError(errorMessage(error)));
        }}
      >
        <div className="file-toolbar">
          <nav className="breadcrumbs" aria-label="Путь к папке">
            <button onClick={() => go('')}>
              <Icon name="folder" />
              Папки
            </button>
            {trail.map((node) => (
              <span key={node.id}>
                <Icon name="chevron" />
                <button
                  aria-current={node.id === folderID ? 'page' : undefined}
                  onClick={() => go(node.id)}
                >
                  {node.id === session.user.home_id ? 'Мои файлы' : node.name}
                </button>
              </span>
            ))}
          </nav>
          <div className="toolbar-actions">
            {manager && current.value && (
              <IconButton
                icon="shield"
                label="Доступ к текущей папке"
                onClick={() => setDialog({ kind: 'access', node: current.value! })}
              />
            )}
            {manager && current.value && (
              <IconButton
                icon="link"
                label="Ссылка на текущую папку"
                onClick={() => setDialog({ kind: 'share', node: current.value! })}
              />
            )}
            <IconButton icon="refresh" label="Обновить файлы" onClick={refresh} />
          </div>
        </div>
        <div className="file-subtoolbar">
          <span className="muted">
            {resource.loading ? 'Загружаем содержимое' : `${filtered.length} на странице`}
            {current.value && (
              <span className="role-label">{roles[current.value.effective_role ?? 0]}</span>
            )}
          </span>
        </div>
        <form
          className="file-search"
          aria-label="Поиск файлов"
          onSubmit={(event) => {
            event.preventDefault();
            try {
              const params = searchParams(filters);
              setSearchError('');
              setOffset(0);
              setSearch(params);
              results.refresh();
            } catch (error) {
              setSearchError(errorMessage(error));
            }
          }}
        >
          <div className="file-search-heading">
            <label className="search-input">
              <Icon name="search" />
              <input
                value={filters.query}
                onChange={(e) => setFilters({ ...filters, query: e.target.value })}
                placeholder="Найти во всех папках"
                aria-label="Поиск по всем доступным папкам"
              />
            </label>
            <Button type="submit" icon="search">
              Найти
            </Button>
            {searching && (
              <Button
                onClick={() => {
                  setSearch(undefined);
                  setFilters(emptySearch);
                  setSearchError('');
                  setOffset(0);
                }}
              >
                Сбросить поиск
              </Button>
            )}
          </div>
          <details className="search-filters">
            <summary>Фильтры по типу, дате и размеру</summary>
            <div className="search-filter-fields">
              <label>
                Тип
                <select
                  aria-label="Тип"
                  value={filters.type}
                  onChange={(e) => setFilters({ ...filters, type: e.target.value })}
                >
                  <option value="">Все файлы и папки</option>
                  <option value="file">Все файлы</option>
                  <option value="folder">Папки</option>
                  <option value="image">Изображения</option>
                  <option value="document">Документы и текст</option>
                  <option value="audio">Аудио</option>
                  <option value="video">Видео</option>
                  <option value="archive">Архивы</option>
                  <option value="other">Другие файлы</option>
                </select>
              </label>
              <label>
                Добавлено с
                <input
                  type="date"
                  min="1970-01-01"
                  value={filters.after}
                  onChange={(e) => setFilters({ ...filters, after: e.target.value })}
                />
              </label>
              <label>
                Добавлено по
                <input
                  type="date"
                  min="1970-01-01"
                  value={filters.before}
                  onChange={(e) => setFilters({ ...filters, before: e.target.value })}
                />
              </label>
              <label>
                Размер от
                <input
                  type="number"
                  min="0"
                  step="any"
                  value={filters.minSize}
                  onChange={(e) => setFilters({ ...filters, minSize: e.target.value })}
                />
              </label>
              <label>
                Размер до
                <input
                  type="number"
                  min="0"
                  step="any"
                  value={filters.maxSize}
                  onChange={(e) => setFilters({ ...filters, maxSize: e.target.value })}
                />
              </label>
              <label>
                Единица размера
                <select
                  aria-label="Единица размера"
                  value={filters.sizeUnit}
                  onChange={(e) => setFilters({ ...filters, sizeUnit: e.target.value })}
                >
                  <option value="1">Б</option>
                  <option value="1024">КБ</option>
                  <option value="1048576">МБ</option>
                  <option value="1073741824">ГБ</option>
                </select>
              </label>
            </div>
            <p className="hint">
              Тип файла определяется по расширению. Размер применяется только к файлам, даты
              включают выбранные дни.
            </p>
          </details>
          <ErrorBox message={searchError} />
          {searching && (
            <p className="search-scope" role="status">
              Результаты по всем доступным папкам
            </p>
          )}
        </form>
        <State
          loading={resource.loading}
          error={resource.error}
          retry={resource.refresh}
          empty={!filtered.length}
          title={searching ? 'Ничего не найдено' : 'Здесь пока пусто'}
          description={
            searching
              ? 'Измените запрос или фильтры и повторите поиск.'
              : editor
                ? 'Загрузите первые файлы или создайте папку.'
                : 'Здесь появятся доступные вам файлы и папки.'
          }
          action={
            editor && !searching ? (
              <Button variant="primary" icon="upload" onClick={() => input.current?.click()}>
                Выбрать файлы
              </Button>
            ) : undefined
          }
        />
        {!resource.loading && !resource.error && filtered.length > 0 && (
          <div className="table-scroll">
            <table className="file-table">
              <thead>
                <tr>
                  <th>Название</th>
                  <th>Размер</th>
                  <th>Добавлено</th>
                  <th>
                    <span className="visually-hidden">Действия</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((node) => (
                  <tr key={node.id}>
                    <td>
                      <div className="file-name">
                        <span
                          className={`file-symbol ${node.kind === 'folder' ? 'folder' : /\.(png|jpe?g|webp|svg|gif)$/i.test(node.name) ? 'picture' : 'document'}`}
                        >
                          <Icon
                            name={
                              node.kind === 'folder'
                                ? 'folder'
                                : /\.(png|jpe?g|webp|svg|gif)$/i.test(node.name)
                                  ? 'image'
                                  : 'file'
                            }
                          />
                        </span>
                        {node.kind === 'folder' ? (
                          <button className="name-button" onClick={() => go(node.id)}>
                            {node.name}
                          </button>
                        ) : (
                          <button
                            className="name-button"
                            title={`Открыть предпросмотр ${node.name}`}
                            onClick={() => {
                              const files = filtered.filter((item) => item.kind === 'file');
                              setPreview({
                                files,
                                index: files.findIndex((item) => item.id === node.id),
                              });
                            }}
                          >
                            {node.name}
                          </button>
                        )}
                        {node.kind === 'folder' && <span className="folder-type">Папка</span>}
                      </div>
                      {searching &&
                        (() => {
                          const path = (node as SearchNode).path;
                          const parent = path.at(-1);
                          const label = path
                            .map((folder) =>
                              folder.id === session.user.home_id ? 'Мои файлы' : folder.name,
                            )
                            .join(' / ');
                          return parent ? (
                            <button
                              className="search-location"
                              aria-label={`Открыть папку для ${node.name}`}
                              title={label}
                              onClick={() => go(parent.id)}
                            >
                              {label}
                            </button>
                          ) : (
                            <span className="search-location">Доступная папка</span>
                          );
                        })()}
                    </td>
                    <td>{node.kind === 'folder' ? '—' : bytes(node.size)}</td>
                    <td>{date(node.created_at)}</td>
                    <td>
                      <div className="row-actions">
                        {node.kind === 'file' && (
                          <a
                            className="icon-button"
                            href={`/api/v1/nodes/${idPath(node.id)}/content`}
                            title={`Скачать ${node.name}`}
                            aria-label={`Скачать ${node.name}`}
                          >
                            <Icon name="download" />
                          </a>
                        )}
                        {(node.effective_role ?? 0) >= 3 && (
                          <IconButton
                            icon="link"
                            label={`Поделиться ${node.name}`}
                            onClick={() => setDialog({ kind: 'share', node })}
                          />
                        )}
                        {(node.effective_role ?? 0) >= 2 && (
                          <details className="action-menu">
                            <summary aria-label={`Действия ${node.name}`}>
                              <Icon name="more" />
                            </summary>
                            <div className="menu-items">
                              {node.parent_id && (
                                <>
                                  <button onClick={() => setDialog({ kind: 'rename', node })}>
                                    <Icon name="edit" />
                                    Переименовать
                                  </button>
                                  <button onClick={() => setDialog({ kind: 'move', node })}>
                                    <Icon name="move" />
                                    Переместить
                                  </button>
                                </>
                              )}
                              {node.kind === 'folder' && (node.effective_role ?? 0) >= 3 && (
                                <button onClick={() => setDialog({ kind: 'access', node })}>
                                  <Icon name="shield" />
                                  Права доступа
                                </button>
                              )}
                              {node.kind === 'file' && (
                                <button onClick={() => setDialog({ kind: 'replace', node })}>
                                  <Icon name="upload" />
                                  Заменить файлом
                                </button>
                              )}
                              {node.id !== session.user.home_id && (
                                <button
                                  className="text-danger"
                                  onClick={() => setDialog({ kind: 'delete', node })}
                                >
                                  <Icon name="trash" />В корзину
                                </button>
                              )}
                            </div>
                          </details>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <Pager
          offset={offset}
          count={searching ? (results.value?.items.length ?? 0) : (rows.value?.length ?? 0)}
          hasMore={searching ? (results.value?.has_more ?? false) : rows.value?.length === 100}
          onChange={setOffset}
        />
        {editor && (
          <div className="drop-hint">
            <Icon name="upload" />
            <span>
              {manager ? 'Перетащите файлы или папку' : 'Перетащите файлы'} в эту область · загрузку
              можно приостановить
            </span>
          </div>
        )}
        {dragging && (
          <div className="drop-overlay">
            <Icon name="upload" />
            <strong>Отпустите файлы для загрузки</strong>
          </div>
        )}
      </section>
      <div className="page-note">
        <Icon name="lock" />
        <span>
          Файлы доступны по вашим правам. При загрузке папки создаётся общая ссылка на 7 дней.
        </span>
      </div>
      {dialog && (
        <FileDialog
          key={`${dialog.kind}:${'node' in dialog ? dialog.node.id : folderID}`}
          dialog={dialog}
          parent={folderID}
          onClose={() => {
            setDialog(undefined);
            document
              .querySelectorAll<HTMLDetailsElement>('.action-menu[open]')
              .forEach((menu) => (menu.open = false));
          }}
          onSaved={refresh}
          onQueue={onQueue}
        />
      )}
      {preview && (
        <FilePreview
          files={preview.files}
          initialIndex={preview.index}
          onClose={() => setPreview(undefined)}
        />
      )}
    </>
  );
}

function FileDialog({
  dialog,
  parent,
  onClose,
  onSaved,
  onQueue,
}: {
  dialog: Dialog;
  parent: string;
  onClose: () => void;
  onSaved: () => void;
  onQueue: () => void;
}) {
  const { notify } = useApp();
  const uploads = useUploads();
  const [name, setName] = useState('node' in dialog ? dialog.node.name : '');
  const [target, setTarget] = useState('node' in dialog ? (dialog.node.parent_id ?? '') : parent);
  const targetNode = useResource<FileNode>(
    dialog.kind === 'move' && target ? `/api/v1/nodes/${idPath(target)}` : null,
  );
  const [file, setFile] = useState<File>();
  const task = useTask();
  if (dialog.kind === 'share') return <ShareDialog node={dialog.node} onClose={onClose} />;
  if (dialog.kind === 'access')
    return (
      <AccessDialog
        node={dialog.node}
        onClose={() => {
          onSaved();
          onClose();
        }}
      />
    );
  if (dialog.kind === 'delete')
    return (
      <Confirm
        title="Переместить в корзину?"
        description={`«${dialog.node.name}» можно восстановить в течение 7 дней. Публичные ссылки на удалённое содержимое будут отозваны.`}
        label="В корзину"
        danger
        busy={task.busy}
        error={task.error}
        onClose={onClose}
        onConfirm={() =>
          void task.run(async () => {
            await api(`/api/v1/nodes/${idPath(dialog.node.id)}`, 'DELETE');
            onSaved();
            onClose();
            notify('Перемещено в корзину');
          })
        }
      />
    );
  const title =
    dialog.kind === 'new'
      ? 'Новая папка'
      : dialog.kind === 'rename'
        ? 'Переименовать'
        : dialog.kind === 'replace'
          ? 'Заменить файл'
          : 'Переместить';
  return (
    <Modal
      title={title}
      subtitle={'node' in dialog ? dialog.node.name : undefined}
      onClose={onClose}
      busy={task.busy}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void task.run(async () => {
            if (dialog.kind === 'replace') {
              if (!file || !validName(file.name))
                throw new Error('Выберите файл с допустимым именем.');
              await uploads.add(
                [file],
                dialog.node.parent_id!,
                dialog.node.name,
                dialog.node.id,
                dialog.node.name,
              );
              onQueue();
            } else if (dialog.kind === 'new') {
              if (!validName(name))
                throw new Error('Укажите допустимое имя папки длиной до 240 байт.');
              await api('/api/v1/folders', 'POST', { parent_id: parent, name });
              notify('Папка создана');
            } else {
              if (!validName(name)) throw new Error('Укажите допустимое имя длиной до 240 байт.');
              await api(`/api/v1/nodes/${idPath(dialog.node.id)}`, 'PATCH', {
                parent_id: dialog.kind === 'move' ? target : dialog.node.parent_id,
                name,
              });
              notify(dialog.kind === 'move' ? 'Перемещено' : 'Имя изменено');
            }
            onSaved();
            onClose();
          });
        }}
      >
        {dialog.kind !== 'replace' && (
          <label>
            Название
            <input autoFocus value={name} onChange={(e) => setName(e.target.value)} required />
          </label>
        )}
        {dialog.kind === 'move' && (
          <FolderPicker value={target} onChange={setTarget} exclude={dialog.node.id} />
        )}
        {dialog.kind === 'replace' && (
          <>
            <div className="info-box">
              <Icon name="info" />
              <p>
                После успешной загрузки старый файл попадёт в корзину, его прямые публичные ссылки
                будут отозваны. Новый файл получит прежнее имя.
              </p>
            </div>
            <label>
              Новый файл
              <input type="file" required onChange={(e) => setFile(e.target.files?.[0])} />
            </label>
          </>
        )}
        <ErrorBox message={task.error} />
        <div className="modal-footer">
          <Button onClick={onClose} disabled={task.busy}>
            Отмена
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={
              task.busy ||
              (dialog.kind === 'move' && (!target || (targetNode.value?.effective_role ?? 0) < 2))
            }
          >
            {task.busy
              ? 'Подождите…'
              : dialog.kind === 'replace'
                ? 'Загрузить замену'
                : 'Сохранить'}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
