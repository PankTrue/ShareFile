import { t } from '../i18n';
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
import { BulkActions } from './BulkActions';

type Dialog =
  | { kind: 'new' }
  | { kind: 'rename' | 'move' | 'delete' | 'share' | 'access' | 'replace'; node: FileNode };
export function Files({
  folderID,
  spaces = false,
  collection,
  onFolder,
  uploadRevision,
  onQueue,
}: {
  folderID: string;
  spaces?: boolean;
  collection?: 'favorites' | 'recent';
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
  const favoriteTask = useTask();
  const collectionTitle = collection === 'favorites' ? t('Избранное') : t('Недавние');
  const selectionScope = `${collection ?? folderID}:${search ?? 'browse'}:${offset}`;
  const [selection, setSelection] = useState<{ scope: string; ids: string[] }>({
    scope: selectionScope,
    ids: [],
  });
  if (selection.scope !== selectionScope) setSelection({ scope: selectionScope, ids: [] });
  const input = useRef<HTMLInputElement>(null);
  const directoryInput = useRef<HTMLInputElement>(null);
  const rows = useResource<FileNode[]>(
    search === undefined && !collection
      ? `/api/v1/folders?parent_id=${idPath(folderID)}&limit=100&offset=${offset}&refresh=${uploadRevision}`
      : null,
  );
  const results = useResource<SearchResult>(
    search !== undefined || collection
      ? `/api/v1/search?${search ?? ''}&view=${collection ?? ''}&limit=100&offset=${offset}&refresh=${uploadRevision}`
      : null,
  );
  const searching = search !== undefined;
  const listing = searching || Boolean(collection);
  const resource = listing ? results : rows;
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
  const filtered = listing
    ? (results.value?.items ?? [])
    : (rows.value ?? []).filter((node) => !spaces || node.id !== session.user.home_id);
  const selected =
    selection.scope === selectionScope
      ? filtered.filter((node) => selection.ids.includes(node.id))
      : [];
  const allSelected = filtered.length > 0 && selected.length === filtered.length;
  function select(ids: string[]) {
    setSelection({ scope: selectionScope, ids });
  }
  function refresh() {
    resource.refresh();
    current.refresh();
  }
  function toggleFavorite(node: FileNode) {
    void favoriteTask.run(async () => {
      const favorite = !node.favorite;
      await api(`/api/v1/nodes/${idPath(node.id)}/favorite`, favorite ? 'PUT' : 'DELETE');
      const update = (item: FileNode) => (item.id === node.id ? { ...item, favorite } : item);
      rows.mutate((items) => items?.map(update));
      results.mutate((result) =>
        result
          ? { ...result, items: result.items.map((item) => ({ ...item, ...update(item) })) }
          : result,
      );
      current.mutate((item) => (item ? update(item) : item));
      if (collection === 'favorites' && !favorite) {
        select([]);
        if (filtered.length === 1 && offset > 0) setOffset(Math.max(0, offset - 100));
        else results.refresh();
      }
    });
  }
  function favoriteButton(node: FileNode, currentFolder = false) {
    return (
      <IconButton
        icon="star"
        className={node.favorite ? 'favorite-active' : ''}
        label={`${node.favorite ? t('Убрать из избранного') : t('Добавить в избранное')} ${currentFolder ? t('текущую папку') : node.name}`}
        aria-pressed={Boolean(node.favorite)}
        disabled={favoriteTask.busy}
        onClick={() => toggleFavorite(node)}
      />
    );
  }
  return (
    <>
      <PageTitle
        eyebrow={
          collection ? t('БЫСТРЫЙ ДОСТУП') : spaces ? t('СОВМЕСТНАЯ РАБОТА') : t('ВАШЕ ХРАНИЛИЩЕ')
        }
        title={
          collection
            ? collectionTitle
            : spaces
              ? t('Доступные папки')
              : (current.value?.name ?? t('Мои файлы'))
        }
        description={
          collection === 'favorites'
            ? t('Ваши избранные файлы и рабочие папки.')
            : collection === 'recent'
              ? t('Последние завершённые загрузки из доступных папок. Новые файлы — сверху.')
              : spaces
                ? t('Личные и общие папки, к которым у вас есть доступ.')
                : t('Сохраняйте файлы на своём диске и делитесь ими по ссылке.')
        }
      >
        {folderID && current.value && (
          <a className="button secondary" href={`/api/v1/nodes/${idPath(folderID)}/archive`}>
            <Icon name="download" />
            {t('Скачать всё (ZIP)')}
          </a>
        )}
        {editor && (
          <>
            <Button icon="plus" onClick={() => setDialog({ kind: 'new' })}>
              {t('Новая папка')}
            </Button>
            <Button variant="primary" icon="upload" onClick={() => input.current?.click()}>
              {t('Загрузить файлы')}
            </Button>
            {manager && (
              <Button icon="folder" onClick={() => directoryInput.current?.click()}>
                {t('Загрузить папку')}
              </Button>
            )}
          </>
        )}
        {manager && current.value && (
          <Button icon="link" onClick={() => setDialog({ kind: 'share', node: current.value! })}>
            {t('Поделиться папкой')}
          </Button>
        )}
        {!folderID && !collection && session.user.admin && (
          <Button variant="primary" icon="plus" onClick={() => setDialog({ kind: 'new' })}>
            {t('Создать общую папку')}
          </Button>
        )}
      </PageTitle>
      <input
        ref={input}
        className="visually-hidden"
        type="file"
        multiple
        aria-label={t('Выбрать файлы для загрузки')}
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
        aria-label={t('Выбрать папку для загрузки')}
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          e.target.value = '';
          setUploadError('');
          void (async () => {
            await addDirectory(directoryFromFiles(files));
          })().catch((error) => setUploadError(errorMessage(error)));
        }}
      />
      <ErrorBox message={uploadError || current.error || favoriteTask.error} />
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
          <nav className="breadcrumbs" aria-label={t('Путь к папке')}>
            <button onClick={() => go('')}>
              <Icon name="folder" />
              {t('Папки')}
            </button>
            {collection && (
              <span>
                <Icon name="chevron" />
                {collectionTitle}
              </span>
            )}
            {trail.map((node) => (
              <span key={node.id}>
                <Icon name="chevron" />
                <button
                  aria-current={node.id === folderID ? 'page' : undefined}
                  onClick={() => go(node.id)}
                >
                  {node.id === session.user.home_id ? t('Мои файлы') : node.name}
                </button>
              </span>
            ))}
          </nav>
          <div className="toolbar-actions">
            {current.value && favoriteButton(current.value, true)}
            {manager && current.value && (
              <IconButton
                icon="shield"
                label={t('Доступ к текущей папке')}
                onClick={() => setDialog({ kind: 'access', node: current.value! })}
              />
            )}
            {manager && current.value && (
              <IconButton
                icon="link"
                label={t('Ссылка на текущую папку')}
                onClick={() => setDialog({ kind: 'share', node: current.value! })}
              />
            )}
            <IconButton icon="refresh" label={t('Обновить файлы')} onClick={refresh} />
          </div>
        </div>
        <div className="file-subtoolbar">
          <span className="muted">
            {resource.loading ? t('Загружаем содержимое') : t('{0} на странице', [filtered.length])}
            {current.value && (
              <span className="role-label">{t(roles[current.value.effective_role ?? 0])}</span>
            )}
          </span>
        </div>
        <form
          className="file-search"
          aria-label={t('Поиск файлов')}
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
                placeholder={
                  collection
                    ? t('Найти в разделе «{0}»', [collectionTitle])
                    : t('Найти во всех папках')
                }
                aria-label={
                  collection
                    ? t('Поиск в разделе {0}', [collectionTitle])
                    : t('Поиск по всем доступным папкам')
                }
              />
            </label>
            <Button type="submit" icon="search">
              {t('Найти')}
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
                {t('Сбросить поиск')}
              </Button>
            )}
          </div>
          <details className="search-filters">
            <summary>{t('Фильтры по типу, дате и размеру')}</summary>
            <div className="search-filter-fields">
              <label>
                {t('Тип')}
                <select
                  aria-label={t('Тип')}
                  value={filters.type}
                  onChange={(e) => setFilters({ ...filters, type: e.target.value })}
                >
                  <option value="">
                    {collection === 'recent' ? t('Все файлы') : t('Все файлы и папки')}
                  </option>
                  <option value="file">{t('Все файлы')}</option>
                  {collection !== 'recent' && <option value="folder">{t('Папки')}</option>}
                  <option value="image">{t('Изображения')}</option>
                  <option value="document">{t('Документы и текст')}</option>
                  <option value="audio">{t('Аудио')}</option>
                  <option value="video">{t('Видео')}</option>
                  <option value="archive">{t('Архивы')}</option>
                  <option value="other">{t('Другие файлы')}</option>
                </select>
              </label>
              <label>
                {t('Добавлено с')}
                <input
                  type="date"
                  min="1970-01-01"
                  value={filters.after}
                  onChange={(e) => setFilters({ ...filters, after: e.target.value })}
                />
              </label>
              <label>
                {t('Добавлено по')}
                <input
                  type="date"
                  min="1970-01-01"
                  value={filters.before}
                  onChange={(e) => setFilters({ ...filters, before: e.target.value })}
                />
              </label>
              <label>
                {t('Размер от')}
                <input
                  type="number"
                  min="0"
                  step="any"
                  value={filters.minSize}
                  onChange={(e) => setFilters({ ...filters, minSize: e.target.value })}
                />
              </label>
              <label>
                {t('Размер до')}
                <input
                  type="number"
                  min="0"
                  step="any"
                  value={filters.maxSize}
                  onChange={(e) => setFilters({ ...filters, maxSize: e.target.value })}
                />
              </label>
              <label>
                {t('Единица размера')}
                <select
                  aria-label={t('Единица размера')}
                  value={filters.sizeUnit}
                  onChange={(e) => setFilters({ ...filters, sizeUnit: e.target.value })}
                >
                  <option value="1">{t('Б')}</option>
                  <option value="1024">{t('КБ')}</option>
                  <option value="1048576">{t('МБ')}</option>
                  <option value="1073741824">{t('ГБ')}</option>
                </select>
              </label>
            </div>
            <p className="hint">
              {t(
                'Тип файла определяется по расширению. Размер применяется только к файлам, даты включают выбранные дни.',
              )}
            </p>
          </details>
          <ErrorBox message={searchError} />
          {searching && (
            <p className="search-scope" role="status">
              {collection
                ? t('Результаты в разделе «{0}»', [collectionTitle])
                : t('Результаты по всем доступным папкам')}
            </p>
          )}
        </form>
        {!resource.loading && !resource.error && selected.length > 0 && (
          <BulkActions
            nodes={selected}
            parent={folderID}
            onClear={() => select([])}
            onSaved={() => {
              select([]);
              refresh();
            }}
            onCollected={(folder) => setDialog({ kind: 'share', node: folder })}
          />
        )}
        <State
          loading={resource.loading}
          error={resource.error}
          retry={resource.refresh}
          empty={!filtered.length}
          title={
            searching
              ? t('Ничего не найдено')
              : collection === 'favorites'
                ? t('В избранном пока пусто')
                : collection === 'recent'
                  ? t('Пока нет загрузок')
                  : t('Здесь пока пусто')
          }
          description={
            searching
              ? t('Измените запрос или фильтры и повторите поиск.')
              : collection === 'favorites'
                ? t(
                    'Нажмите звёздочку рядом с файлом или рабочей папкой, чтобы сохранить быстрый доступ.',
                  )
                : collection === 'recent'
                  ? t(
                      'Загрузите файл в личную или общую папку — он появится здесь после завершения.',
                    )
                  : editor
                    ? t('Загрузите первые файлы или создайте папку.')
                    : t('Здесь появятся доступные вам файлы и папки.')
          }
          action={
            editor && !searching ? (
              <Button variant="primary" icon="upload" onClick={() => input.current?.click()}>
                {t('Выбрать файлы')}
              </Button>
            ) : undefined
          }
        />
        {!resource.loading && !resource.error && filtered.length > 0 && (
          <div className="table-scroll">
            <table className="file-table">
              <thead>
                <tr>
                  <th>
                    <label className="select-all">
                      <input
                        type="checkbox"
                        aria-label={t('Выбрать всё на странице')}
                        checked={allSelected}
                        ref={(node) => {
                          if (node) node.indeterminate = selected.length > 0 && !allSelected;
                        }}
                        onChange={(event) =>
                          select(event.target.checked ? filtered.map((node) => node.id) : [])
                        }
                      />
                      {t('Название')}
                    </label>
                  </th>
                  <th>{t('Размер')}</th>
                  <th>{t('Добавлено')}</th>
                  <th>
                    <span className="visually-hidden">{t('Действия')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((node) => (
                  <tr
                    key={node.id}
                    className={
                      selection.ids.includes(node.id) && selection.scope === selectionScope
                        ? 'selected'
                        : undefined
                    }
                  >
                    <td>
                      <div className="file-name">
                        <input
                          type="checkbox"
                          aria-label={t('Выбрать {0}', [node.name])}
                          checked={selected.some((item) => item.id === node.id)}
                          onChange={(event) =>
                            select(
                              event.target.checked
                                ? [...selected.map((item) => item.id), node.id]
                                : selected
                                    .filter((item) => item.id !== node.id)
                                    .map((item) => item.id),
                            )
                          }
                        />
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
                            title={t('Открыть предпросмотр {0}', [node.name])}
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
                        {node.kind === 'folder' && (
                          <span className="folder-type">{t('Папка')}</span>
                        )}
                      </div>
                      {listing &&
                        (() => {
                          const path = (node as SearchNode).path;
                          const parent = path.at(-1);
                          const label = path
                            .map((folder) =>
                              folder.id === session.user.home_id ? t('Мои файлы') : folder.name,
                            )
                            .join(' / ');
                          return parent ? (
                            <button
                              className="search-location"
                              aria-label={t('Открыть папку для {0}', [node.name])}
                              title={label}
                              onClick={() => go(parent.id)}
                            >
                              {label}
                            </button>
                          ) : (
                            <span className="search-location">{t('Доступная папка')}</span>
                          );
                        })()}
                    </td>
                    <td>{node.kind === 'folder' ? '—' : bytes(node.size)}</td>
                    <td>{date(node.created_at)}</td>
                    <td>
                      <div className="row-actions">
                        {favoriteButton(node)}
                        {node.kind === 'file' && (
                          <a
                            className="icon-button"
                            href={`/api/v1/nodes/${idPath(node.id)}/content`}
                            title={t('Скачать {0}', [node.name])}
                            aria-label={t('Скачать {0}', [node.name])}
                          >
                            <Icon name="download" />
                          </a>
                        )}
                        {(node.effective_role ?? 0) >= 3 && (
                          <IconButton
                            icon="link"
                            label={t('Поделиться {0}', [node.name])}
                            onClick={() => setDialog({ kind: 'share', node })}
                          />
                        )}
                        {(node.effective_role ?? 0) >= 2 && (
                          <details className="action-menu">
                            <summary aria-label={t('Действия {0}', [node.name])}>
                              <Icon name="more" />
                            </summary>
                            <div className="menu-items">
                              {node.parent_id && (
                                <>
                                  <button onClick={() => setDialog({ kind: 'rename', node })}>
                                    <Icon name="edit" />
                                    {t('Переименовать')}
                                  </button>
                                  <button onClick={() => setDialog({ kind: 'move', node })}>
                                    <Icon name="move" />
                                    {t('Переместить')}
                                  </button>
                                </>
                              )}
                              {node.kind === 'folder' && (node.effective_role ?? 0) >= 3 && (
                                <button onClick={() => setDialog({ kind: 'access', node })}>
                                  <Icon name="shield" />
                                  {t('Права доступа')}
                                </button>
                              )}
                              {node.kind === 'file' && (
                                <button onClick={() => setDialog({ kind: 'replace', node })}>
                                  <Icon name="upload" />
                                  {t('Заменить файлом')}
                                </button>
                              )}
                              {node.id !== session.user.home_id && (
                                <button
                                  className="text-danger"
                                  onClick={() => setDialog({ kind: 'delete', node })}
                                >
                                  <Icon name="trash" />
                                  {t('В корзину')}
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
          count={listing ? (results.value?.items.length ?? 0) : (rows.value?.length ?? 0)}
          hasMore={listing ? (results.value?.has_more ?? false) : rows.value?.length === 100}
          onChange={setOffset}
        />
        {editor && (
          <div className="drop-hint">
            <Icon name="upload" />
            <span>
              {manager ? t('Перетащите файлы или папку') : t('Перетащите файлы')}{' '}
              {t('в эту область · загрузку можно приостановить')}
            </span>
          </div>
        )}
        {dragging && (
          <div className="drop-overlay">
            <Icon name="upload" />
            <strong>{t('Отпустите файлы для загрузки')}</strong>
          </div>
        )}
      </section>
      <div className="page-note">
        <Icon name="lock" />
        <span>
          {t(
            'Файлы доступны по вашим правам. При загрузке папки создаётся общая ссылка на 7 дней.',
          )}
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
        title={t('Переместить в корзину?')}
        description={t(
          '«{0}» можно восстановить в течение 7 дней. Публичные ссылки на удалённое содержимое будут отозваны.',
          [dialog.node.name],
        )}
        label={t('В корзину')}
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
      ? t('Новая папка')
      : dialog.kind === 'rename'
        ? t('Переименовать')
        : dialog.kind === 'replace'
          ? t('Заменить файл')
          : t('Переместить');
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
            {t('Название')}
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
                {t(
                  'После успешной загрузки старый файл попадёт в корзину, его прямые публичные ссылки будут отозваны. Новый файл получит прежнее имя.',
                )}
              </p>
            </div>
            <label>
              {t('Новый файл')}
              <input type="file" required onChange={(e) => setFile(e.target.files?.[0])} />
            </label>
          </>
        )}
        <ErrorBox message={task.error} />
        <div className="modal-footer">
          <Button onClick={onClose} disabled={task.busy}>
            {t('Отмена')}
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
              ? t('Подождите…')
              : dialog.kind === 'replace'
                ? t('Загрузить замену')
                : t('Сохранить')}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
