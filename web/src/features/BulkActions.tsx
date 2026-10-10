import { useState } from 'react';
import { api, idPath } from '../api';
import { useApp } from '../context';
import { useResource, useTask } from '../hooks';
import type { FileNode } from '../types';
import { validName } from '../utils';
import { Button, Confirm, ErrorBox, Modal } from '../components/UI';
import { FolderPicker } from '../components/FolderPicker';
import { Icon } from '../components/Icon';

type Action = 'move' | 'delete' | 'collect';

async function batch<T>(body: {
  action: Action;
  node_ids: string[];
  parent_id?: string;
  name?: string;
}): Promise<T> {
  try {
    return await api<T>('/api/v1/nodes/batch', 'POST', body);
  } catch (error) {
    if (error instanceof TypeError)
      throw new Error(
        'Не удалось получить ответ сервера. Операция могла выполниться. Закройте окно и обновите файлы, прежде чем повторять действие.',
        { cause: error },
      );
    throw error;
  }
}

export function BulkActions({
  nodes,
  parent,
  onClear,
  onSaved,
  onCollected,
}: {
  nodes: FileNode[];
  parent: string;
  onClear: () => void;
  onSaved: () => void;
  onCollected: (folder: FileNode) => void;
}) {
  const { session } = useApp();
  const [action, setAction] = useState<Action>();
  const movable = nodes.every((node) => node.parent_id && (node.effective_role ?? 0) >= 2);
  const deletable = nodes.every(
    (node) => node.id !== session.user.home_id && (node.effective_role ?? 0) >= 2,
  );
  const query = new URLSearchParams();
  nodes.forEach((node) => query.append('id', node.id));
  return (
    <div className="bulk-actions" role="region" aria-label="Действия с выбранными файлами">
      <strong role="status">Выбрано: {nodes.length}</strong>
      <div className="bulk-buttons">
        <Button icon="move" disabled={!movable} onClick={() => setAction('move')}>
          Переместить выбранное
        </Button>
        <Button icon="trash" disabled={!deletable} onClick={() => setAction('delete')}>
          В корзину выбранное
        </Button>
        <a className="button secondary" href={`/api/v1/nodes/archive?${query}`}>
          <Icon name="download" />
          Скачать выбранное (ZIP)
        </a>
        <Button icon="folder" disabled={!movable} onClick={() => setAction('collect')}>
          Собрать для отправки
        </Button>
        <Button variant="ghost" onClick={onClear}>
          Снять выделение
        </Button>
      </div>
      {!movable && (
        <p className="hint">
          Для перемещения нужны права редактора на всё выбранное содержимое. Корневые папки
          перемещать нельзя.
        </p>
      )}
      {action && (
        <BulkDialog
          action={action}
          nodes={nodes}
          parent={parent || session.user.home_id}
          onClose={() => setAction(undefined)}
          onSaved={onSaved}
          onCollected={onCollected}
        />
      )}
    </div>
  );
}

function BulkDialog({
  action,
  nodes,
  parent,
  onClose,
  onSaved,
  onCollected,
}: {
  action: Action;
  nodes: FileNode[];
  parent: string;
  onClose: () => void;
  onSaved: () => void;
  onCollected: (folder: FileNode) => void;
}) {
  const { notify } = useApp();
  const task = useTask();
  const [target, setTarget] = useState(parent);
  const [name, setName] = useState('Для отправки');
  const targetNode = useResource<FileNode>(target ? `/api/v1/nodes/${idPath(target)}` : null);
  const nodeIDs = nodes.map((node) => node.id);
  const summary =
    nodes
      .slice(0, 3)
      .map((node) => `«${node.name}»`)
      .join(', ') + (nodes.length > 3 ? '…' : '');
  if (action === 'delete')
    return (
      <Confirm
        title={`Переместить в корзину выбранное (${nodes.length})?`}
        description={`${summary}. Всё выбранное содержимое можно восстановить в течение 7 дней. Публичные ссылки на удалённое содержимое будут отозваны.`}
        label="В корзину"
        danger
        busy={task.busy}
        error={task.error}
        onClose={onClose}
        onConfirm={() =>
          void task.run(async () => {
            await batch({ action, node_ids: nodeIDs });
            notify(`Перемещено в корзину: ${nodes.length}`);
            onClose();
            onSaved();
          })
        }
      />
    );
  return (
    <Modal
      title={action === 'collect' ? 'Собрать в папку для отправки' : 'Переместить выбранное'}
      subtitle={`${nodes.length}: ${summary}`}
      onClose={onClose}
      busy={task.busy}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void task.run(async () => {
            if (action === 'collect' && !validName(name))
              throw new Error('Укажите допустимое имя папки длиной до 240 байт.');
            const folder = await batch<FileNode>({
              action,
              node_ids: nodeIDs,
              parent_id: target,
              ...(action === 'collect' ? { name } : {}),
            });
            notify(
              action === 'collect' ? 'Папка для отправки собрана' : `Перемещено: ${nodes.length}`,
            );
            onClose();
            onSaved();
            if (action === 'collect') onCollected(folder);
          });
        }}
      >
        {action === 'collect' && (
          <>
            <label>
              Название папки
              <input
                autoFocus
                required
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
            <p className="modal-copy">
              Выбранные файлы и папки будут перемещены в новую папку. После этого можно создать одну
              ссылку для отправки. При совпадении имён добавится числовой суффикс.
            </p>
          </>
        )}
        {action === 'move' && (
          <p className="modal-copy">
            Откройте папку назначения. Если в ней уже есть такое имя, вся операция отменится.
          </p>
        )}
        <FolderPicker value={target} onChange={setTarget} exclude={nodeIDs} />
        {action === 'collect' && (targetNode.value?.effective_role ?? 0) < 3 && (
          <p className="hint">Для отправки по ссылке выберите папку с правами управляющего.</p>
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
              !target ||
              targetNode.loading ||
              Boolean(targetNode.error) ||
              (targetNode.value?.effective_role ?? 0) < (action === 'collect' ? 3 : 2)
            }
          >
            {task.busy
              ? 'Подождите…'
              : action === 'collect'
                ? 'Собрать и поделиться'
                : 'Переместить'}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
