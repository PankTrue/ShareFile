import { useState } from 'react';
import { api, APIError, idPath } from '../api';
import { useResource, useTask } from '../hooks';
import { useApp } from '../context';
import type { FileNode, Group, Settings, User } from '../types';
import { validName, validPassword } from '../utils';
import { ServerSettings } from './Settings';
import { Button, Confirm, ErrorBox, Modal, PageTitle, State } from '../components/UI';
import { Icon } from '../components/Icon';
import { FolderPicker } from '../components/FolderPicker';

export function Users() {
  const { notify, session } = useApp();
  const data = useResource<User[]>('/api/v1/admin/users');
  const [dialog, setDialog] = useState<
    { kind: 'new' } | { kind: 'password' | 'status'; user: User }
  >();
  return (
    <>
      <PageTitle
        eyebrow="АДМИНИСТРИРОВАНИЕ"
        title="Пользователи"
        description="Учётные записи, личные папки и доступ к хранилищу."
      >
        <Button variant="primary" icon="plus" onClick={() => setDialog({ kind: 'new' })}>
          Добавить пользователя
        </Button>
      </PageTitle>
      <section className="content-card">
        <State
          loading={data.loading}
          error={data.error}
          empty={!data.value?.length}
          icon="users"
          retry={data.refresh}
        />
        <div className="item-list">
          {data.value?.map((user) => (
            <div className="item-row" key={user.id}>
              <span className="avatar">{user.username.slice(0, 2).toUpperCase()}</span>
              <div className="item-copy">
                <strong>
                  {user.username}
                  {user.id === session.user.id && <span className="muted"> · вы</span>}
                </strong>
                <small>
                  {user.admin ? 'Администратор' : 'Пользователь'} · личная папка создана
                </small>
              </div>
              <span className={`badge ${user.active ? 'green' : 'neutral'}`}>
                {user.active ? 'Активен' : 'Отключён'}
              </span>
              <Button onClick={() => setDialog({ kind: 'password', user })}>Сменить пароль</Button>
              <Button variant="ghost" onClick={() => setDialog({ kind: 'status', user })}>
                {user.active ? 'Отключить' : 'Включить'}
              </Button>
            </div>
          ))}
        </div>
      </section>
      {dialog && (
        <UserDialog
          key={dialog.kind === 'new' ? 'new' : `${dialog.kind}:${dialog.user.id}`}
          dialog={dialog}
          onClose={() => setDialog(undefined)}
          onSaved={() => {
            data.refresh();
            notify(dialog.kind === 'new' ? 'Пользователь создан' : 'Учётная запись обновлена');
          }}
        />
      )}
    </>
  );
}
function UserDialog({
  dialog,
  onClose,
  onSaved,
}: {
  dialog: { kind: 'new' } | { kind: 'password' | 'status'; user: User };
  onClose: () => void;
  onSaved: () => void;
}) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [admin, setAdmin] = useState(false);
  const task = useTask();
  if (dialog.kind === 'status')
    return (
      <Confirm
        title={dialog.user.active ? 'Отключить пользователя?' : 'Включить пользователя?'}
        description={
          dialog.user.active
            ? `Файлы «${dialog.user.username}» сохранятся. Сессии, устройства и публичные ссылки пользователя будут отозваны. Последнего активного администратора отключить нельзя.`
            : `Пользователь «${dialog.user.username}» сможет войти. Старые публичные ссылки и подключения не возобновятся.`
        }
        label={dialog.user.active ? 'Отключить' : 'Включить'}
        danger={dialog.user.active}
        onClose={onClose}
        busy={task.busy}
        error={task.error}
        onConfirm={() =>
          void task.run(async () => {
            try {
              await api(`/api/v1/admin/users/${idPath(dialog.user.id)}`, 'PATCH', {
                active: !dialog.user.active,
              });
            } catch (error) {
              if (error instanceof APIError && error.status === 409)
                throw new Error('Нельзя отключить последнего активного администратора.', {
                  cause: error,
                });
              throw error;
            }
            onSaved();
            onClose();
          })
        }
      />
    );
  return (
    <Modal
      title={dialog.kind === 'new' ? 'Новый пользователь' : 'Сменить пароль'}
      subtitle={dialog.kind !== 'new' ? dialog.user.username : undefined}
      onClose={onClose}
      busy={task.busy}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void task.run(async () => {
            if (!validPassword(password))
              throw new Error('Пароль должен занимать от 12 до 256 байт.');
            if (dialog.kind === 'new')
              await api('/api/v1/admin/users', 'POST', { username, password, admin });
            else await api(`/api/v1/admin/users/${idPath(dialog.user.id)}`, 'PATCH', { password });
            setPassword('');
            onSaved();
            onClose();
          });
        }}
      >
        {dialog.kind === 'new' && (
          <label>
            Имя пользователя
            <input
              autoFocus
              autoComplete="off"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              required
              placeholder="Например, alex"
            />
          </label>
        )}
        <label>
          Пароль
          <input
            type="password"
            autoComplete="new-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Не менее 12 символов"
          />
        </label>
        {dialog.kind === 'new' ? (
          <>
            <label className="checkbox-label">
              <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} />
              Администратор
            </label>
            <p className="hint">
              Администратор управляет всеми файлами и учётными записями. Личная папка и папка
              Uploads создадутся автоматически.
            </p>
          </>
        ) : (
          <p className="hint">
            Все активные сессии и подключения пользователя будут отозваны. Если это ваша запись,
            потребуется войти заново.
          </p>
        )}
        <ErrorBox message={task.error} />
        <div className="modal-footer">
          <Button onClick={onClose} disabled={task.busy}>
            Отмена
          </Button>
          <Button type="submit" variant="primary" disabled={task.busy}>
            Сохранить
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function Groups() {
  const data = useResource<Group[]>('/api/v1/admin/groups');
  const [selected, setSelected] = useState<Group>();
  const [creating, setCreating] = useState(false);
  return (
    <>
      <PageTitle
        eyebrow="АДМИНИСТРИРОВАНИЕ"
        title="Группы"
        description="Объединяйте пользователей и назначайте общие права на папки."
      >
        <Button variant="primary" icon="plus" onClick={() => setCreating(true)}>
          Создать группу
        </Button>
      </PageTitle>
      <section className="content-card">
        <State
          loading={data.loading}
          error={data.error}
          empty={!data.value?.length}
          icon="users"
          title="Групп пока нет"
          description="Создайте группу для семьи, команды или общего проекта."
          retry={data.refresh}
        />
        <div className="item-list">
          {data.value?.map((group) => (
            <div className="item-row" key={group.id}>
              <span className="file-symbol folder">
                <Icon name="users" />
              </span>
              <div className="item-copy">
                <strong>{group.name}</strong>
                <small>Права настраиваются в разделе файлов</small>
              </div>
              <Button onClick={() => setSelected(group)}>Участники</Button>
            </div>
          ))}
        </div>
      </section>
      {creating && <CreateGroup onClose={() => setCreating(false)} onSaved={data.refresh} />}
      {selected && <Members group={selected} onClose={() => setSelected(undefined)} />}
    </>
  );
}
function CreateGroup({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const { notify } = useApp();
  const [name, setName] = useState('');
  const task = useTask();
  return (
    <Modal title="Новая группа" onClose={onClose} busy={task.busy}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void task.run(async () => {
            if (!validName(name)) throw new Error('Укажите допустимое имя группы.');
            await api('/api/v1/admin/groups', 'POST', { name });
            onSaved();
            onClose();
            notify('Группа создана');
          });
        }}
      >
        <label>
          Название группы
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} required />
        </label>
        <ErrorBox message={task.error} />
        <div className="modal-footer">
          <Button onClick={onClose} disabled={task.busy}>
            Отмена
          </Button>
          <Button type="submit" variant="primary" disabled={task.busy}>
            Создать
          </Button>
        </div>
      </form>
    </Modal>
  );
}
function Members({ group, onClose }: { group: Group; onClose: () => void }) {
  const { notify } = useApp();
  const users = useResource<User[]>('/api/v1/admin/users');
  const members = useResource<string[]>(`/api/v1/admin/groups/${idPath(group.id)}/members`);
  const task = useTask();
  return (
    <Modal title="Участники группы" subtitle={group.name} onClose={onClose} busy={task.busy}>
      <State
        loading={users.loading || members.loading}
        error={users.error || members.error}
        retry={() => {
          users.refresh();
          members.refresh();
        }}
      />
      <div className="compact-list">
        {users.value &&
          members.value &&
          users.value.map((user) => (
            <label className="member-row" key={user.id}>
              <input
                type="checkbox"
                checked={members.value!.includes(user.id)}
                disabled={task.busy}
                onChange={(e) => {
                  const add = e.target.checked;
                  const previous = members.value!;
                  members.mutate(
                    add ? [...previous, user.id] : previous.filter((id) => id !== user.id),
                  );
                  void task.run(async () => {
                    try {
                      await api(
                        `/api/v1/admin/groups/${idPath(group.id)}/members/${idPath(user.id)}`,
                        add ? 'PUT' : 'DELETE',
                      );
                    } catch (error) {
                      members.mutate(previous);
                      throw error;
                    }
                    notify('Состав группы обновлён');
                  });
                }}
              />
              <strong>{user.username}</strong>
              <span className="muted">{user.active ? '' : 'Отключён'}</span>
            </label>
          ))}
      </div>
      <ErrorBox message={task.error} />
      <p className="hint">
        Удаление участника может навсегда отозвать созданные им публичные ссылки, если он потеряет
        право управления.
      </p>
      <div className="modal-footer">
        <Button onClick={onClose} disabled={task.busy}>
          Готово
        </Button>
      </div>
    </Modal>
  );
}

export function SettingsPage() {
  const data = useResource<Settings>('/api/v1/admin/settings');
  return (
    <>
      <PageTitle
        eyebrow="АДМИНИСТРИРОВАНИЕ"
        title="Настройки"
        description="Диск, директория хранения, адреса доступа и параметры сервера."
      />
      <ServerSettings />
      <State loading={data.loading} error={data.error} retry={data.refresh} />
      {data.value && <SettingsForm settings={data.value} onSaved={data.refresh} />}
    </>
  );
}
function SettingsForm({ settings, onSaved }: { settings: Settings; onSaved: () => void }) {
  const { notify } = useApp();
  const [hours, setHours] = useState(settings.upload_ttl_seconds / 3600);
  const [importing, setImporting] = useState(false);
  const task = useTask();
  return (
    <>
      <section className="settings-card">
        <div className="settings-heading">
          <span className="file-symbol document">
            <Icon name="clock" />
          </span>
          <div>
            <h2>Незавершённые загрузки</h2>
            <p>Освобождайте место от забытых файлов.</p>
          </div>
        </div>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void task.run(async () => {
              await api('/api/v1/admin/settings', 'PATCH', {
                upload_ttl_seconds: Math.round(hours * 3600),
              });
              onSaved();
              notify('Настройки сохранены');
            });
          }}
        >
          <label>
            Срок хранения, часов
            <input
              type="number"
              min={1}
              max={8760}
              step={1}
              required
              value={hours}
              onChange={(e) => setHours(Number(e.target.value))}
            />
          </label>
          <p className="hint">
            Отсчитывается от последней переданной части. Новый срок применяется к новым загрузкам и
            при продолжении существующих.
          </p>
          <ErrorBox message={task.error} />
          <Button type="submit" variant="primary" disabled={task.busy}>
            Сохранить настройки
          </Button>
        </form>
      </section>
      <section className="settings-card">
        <div className="settings-heading">
          <span className="file-symbol document">
            <Icon name="upload" />
          </span>
          <div>
            <h2>Импорт с диска</h2>
            <p>Скопируйте отдельный файл из настроенного источника в хранилище.</p>
          </div>
        </div>
        <Button icon="plus" disabled={!settings.import_roots} onClick={() => setImporting(true)}>
          Импортировать файл
        </Button>
        {!settings.import_roots && (
          <p className="hint">
            Добавьте директории в разделе «Источники импорта» выше и примените настройки.
          </p>
        )}
      </section>
      {importing && (
        <ImportDialog roots={settings.import_roots} onClose={() => setImporting(false)} />
      )}
    </>
  );
}
function ImportDialog({ roots, onClose }: { roots: number; onClose: () => void }) {
  const { notify, session } = useApp();
  const [root, setRoot] = useState(0),
    [path, setPath] = useState(''),
    [name, setName] = useState(''),
    [folder, setFolder] = useState(session.user.home_id);
  const target = useResource<FileNode>(folder ? `/api/v1/nodes/${idPath(folder)}` : null);
  const task = useTask();
  return (
    <Modal title="Импортировать файл" onClose={onClose} busy={task.busy}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void task.run(async () => {
            const filename = name || path.split('/').at(-1) || '';
            if (!validName(filename)) throw new Error('Укажите допустимое имя файла.');
            await api('/api/v1/admin/import', 'POST', {
              root,
              path,
              name: filename,
              folder_id: folder,
            });
            onClose();
            notify('Файл скопирован в хранилище');
          });
        }}
      >
        <div className="form-grid">
          <label>
            Источник
            <select value={root} onChange={(e) => setRoot(Number(e.target.value))}>
              {Array.from({ length: roots }, (_, index) => (
                <option value={index} key={index}>
                  Источник {index + 1}
                </option>
              ))}
            </select>
          </label>
          <label>
            Имя в хранилище
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Как у исходного файла"
            />
          </label>
        </div>
        <label>
          Путь относительно источника
          <input
            required
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="documents/report.pdf"
          />
        </label>
        <p className="hint">
          Нумерация источников соответствует порядку import_dirs. Исходный файл сохранится. Папки и
          символические ссылки импортировать нельзя.
        </p>
        <FolderPicker value={folder} onChange={setFolder} />
        <ErrorBox message={task.error} />
        <div className="modal-footer">
          <Button onClick={onClose} disabled={task.busy}>
            Отмена
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={task.busy || !folder || (target.value?.effective_role ?? 0) < 2}
          >
            Скопировать файл
          </Button>
        </div>
      </form>
    </Modal>
  );
}
