import { t } from '../i18n';
import { useState } from 'react';
import { useResource } from '../hooks';
import { idPath } from '../api';
import type { FileNode } from '../types';
import { Button, State } from './UI';
import { Icon } from './Icon';

export function Pager({
  offset,
  count,
  hasMore = count === 100,
  onChange,
}: {
  offset: number;
  count: number;
  hasMore?: boolean;
  onChange: (offset: number) => void;
}) {
  if (offset === 0 && !hasMore) return null;
  return (
    <div className="pager">
      <span>
        {t('Страница')} {offset / 100 + 1}
      </span>
      <Button disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - 100))}>
        {t('Назад')}
      </Button>
      <Button disabled={!hasMore} onClick={() => onChange(offset + 100)}>
        {t('Далее')}
      </Button>
    </div>
  );
}
export function FolderPicker({
  value,
  onChange,
  exclude,
}: {
  value: string;
  onChange: (id: string) => void;
  exclude?: string | string[];
}) {
  const [offset, setOffset] = useState(0);
  const nodes = useResource<FileNode[]>(
    `/api/v1/folders?parent_id=${idPath(value)}&limit=100&offset=${offset}`,
  );
  const folder = useResource<FileNode>(value ? `/api/v1/nodes/${idPath(value)}` : null);
  const folders =
    nodes.value?.filter(
      (node) =>
        node.kind === 'folder' &&
        !(Array.isArray(exclude) ? exclude.includes(node.id) : node.id === exclude),
    ) ?? [];
  function go(id: string) {
    setOffset(0);
    onChange(id);
  }
  return (
    <div className="folder-picker">
      <div className="picker-heading">
        <Icon name="folder" />
        <strong>{value ? (folder.value?.name ?? t('Папка')) : t('Доступные папки')}</strong>
        <Button variant="ghost" onClick={() => go('')}>
          {t('В начало')}
        </Button>
      </div>
      <State loading={nodes.loading} error={nodes.error || folder.error} retry={nodes.refresh} />
      {!nodes.loading && !nodes.error && (
        <div className="picker-list">
          {folders.map((node) => (
            <button type="button" key={node.id} onClick={() => go(node.id)}>
              <Icon name="folder" />
              <span>{node.name}</span>
              <span className="muted">
                {(node.effective_role ?? 0) < 2 ? t('Только чтение') : ''}
              </span>
              <Icon name="chevron" />
            </button>
          ))}
          {!folders.length && <p className="hint">{t('Вложенных папок на этой странице нет.')}</p>}
        </div>
      )}
      <Pager offset={offset} count={nodes.value?.length ?? 0} onChange={setOffset} />
      <p className="hint">
        {value && (folder.value?.effective_role ?? 0) >= 2
          ? t('Выбрана папка «{0}».', [folder.value?.name ?? ''])
          : t('Откройте папку с правом редактирования.')}
      </p>
      <input type="hidden" value={value && (folder.value?.effective_role ?? 0) >= 2 ? value : ''} />
    </div>
  );
}
