import { t } from '../i18n';
import { useState } from 'react';
import { api, idPath } from '../api';
import { useResource, useTask } from '../hooks';
import { useApp } from '../context';
import type { FileNode, Grant, Principal, Share } from '../types';
import { copy, date, roles, validPassword } from '../utils';
import { Button, ErrorBox, Modal, State } from '../components/UI';
import { Icon } from '../components/Icon';
import { Pager } from '../components/FolderPicker';

export function ShareDialog({ node, onClose }: { node: FileNode; onClose: () => void }) {
  const { notify } = useApp();
  const [offset, setOffset] = useState(0);
  const links = useResource<Share[]>(
    `/api/v1/shares?node_id=${idPath(node.id)}&limit=100&offset=${offset}`,
  );
  const [password, setPassword] = useState('');
  const [expiry, setExpiry] = useState('7');
  const [url, setURL] = useState('');
  const task = useTask();
  return (
    <Modal title={t('Публичный доступ')} subtitle={node.name} onClose={onClose} busy={task.busy}>
      <div className="info-box">
        <Icon name="link" />
        <p>
          {node.kind === 'folder'
            ? t(
                'Ссылка откроет папку и вложенные папки, включая новые файлы. Фото и текст можно просмотреть в браузере, остальные файлы — скачать.',
              )
            : t('Файл можно будет скачать без учётной записи.')}{' '}
          {t('Ссылку можно отозвать в любое время.')}
        </p>
      </div>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void task.run(async () => {
            if (password && !validPassword(password))
              throw new Error('Пароль ссылки должен занимать от 12 до 256 байт.');
            const result = await api<{ share: Share; url: string }>('/api/v1/shares', 'POST', {
              node_id: node.id,
              password,
              ...(expiry === 'forever'
                ? { never_expires: true }
                : { expires_at: Math.floor(Date.now() / 1000) + Number(expiry) * 86400 }),
            });
            setURL(result.url);
            setPassword('');
            links.refresh();
            notify('Публичная ссылка создана');
          });
        }}
      >
        <div className="form-grid">
          <label>
            {t('Срок действия')}
            <select value={expiry} onChange={(e) => setExpiry(e.target.value)}>
              <option value="1">{t('1 день')}</option>
              <option value="7">{t('7 дней')}</option>
              <option value="30">{t('30 дней')}</option>
              <option value="forever">{t('Без срока')}</option>
            </select>
          </label>
          <label>
            {t('Пароль')} <span className="muted">{t('необязательно')}</span>
            <input
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder={t('От 12 символов')}
            />
          </label>
        </div>
        <ErrorBox message={task.error} />
        <Button type="submit" variant="primary" icon="link" disabled={task.busy}>
          {t('Создать ссылку')}
        </Button>
      </form>
      {url && (
        <div className="generated-link">
          <label>
            {t('Сохраните ссылку — она показывается только сейчас')}
            <input
              readOnly
              value={url}
              onFocus={(e) => e.target.select()}
              aria-label={t('Созданная публичная ссылка')}
            />
          </label>
          <Button
            icon="link"
            onClick={() =>
              void task.run(async () => {
                await copy(url);
                notify('Ссылка скопирована');
              })
            }
          >
            {t('Скопировать')}
          </Button>
        </div>
      )}
      <h3 className="section-title">
        {t('Ссылки на этот')} {node.kind === 'folder' ? t('каталог') : t('файл')}
      </h3>
      <State
        loading={links.loading}
        error={links.error}
        empty={!links.value?.length}
        icon="link"
        title={t('Ссылок пока нет')}
        retry={links.refresh}
      />
      <div className="compact-list">
        {links.value?.map((link) => (
          <div key={link.id}>
            <span className={`small-symbol ${link.revoked ? '' : 'blue'}`}>
              <Icon name={link.password_protected ? 'lock' : 'link'} />
            </span>
            <div>
              <strong>
                {link.revoked
                  ? t('Отозвана')
                  : link.expires_at && link.expires_at <= Date.now() / 1000
                    ? t('Срок истёк')
                    : t('Публичная ссылка')}
              </strong>
              <small>
                {t('Создана')} {date(link.created_at)} ·{' '}
                {link.expires_at ? t('до {0}', [date(link.expires_at)]) : t('без срока')}
              </small>
            </div>
            {!link.revoked && (
              <Button
                variant="ghost"
                disabled={task.busy}
                onClick={() =>
                  void task.run(async () => {
                    await api(`/api/v1/shares/${idPath(link.id)}`, 'DELETE');
                    links.refresh();
                    notify('Ссылка отозвана');
                  })
                }
              >
                {t('Отозвать')}
              </Button>
            )}
          </div>
        ))}
      </div>
      <Pager offset={offset} count={links.value?.length ?? 0} onChange={setOffset} />
      <p className="hint">
        {t('Адрес существующей ссылки восстановить нельзя. При необходимости создайте новую.')}
      </p>
    </Modal>
  );
}

export function AccessDialog({ node, onClose }: { node: FileNode; onClose: () => void }) {
  const { notify } = useApp();
  const grants = useResource<Grant[]>(`/api/v1/nodes/${idPath(node.id)}/grants`);
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [offset, setOffset] = useState(0);
  const principals = useResource<Principal[]>(
    `/api/v1/nodes/${idPath(node.id)}/principals?query=${encodeURIComponent(search)}&limit=100&offset=${offset}`,
  );
  const [selected, setSelected] = useState('');
  const [role, setRole] = useState(1);
  const task = useTask();
  const selectedPrincipal = principals.value?.find((p) => `${p.type}:${p.id}` === selected);
  async function save(grant: Grant) {
    await api(`/api/v1/nodes/${idPath(node.id)}/grants`, 'PUT', {
      principal_type: grant.principal_type,
      principal_id: grant.principal_id,
      role: grant.role,
    });
    grants.refresh();
    notify('Права доступа обновлены');
  }
  return (
    <Modal title={t('Доступ к папке')} subtitle={node.name} onClose={onClose} busy={task.busy}>
      <div className="info-box">
        <Icon name="shield" />
        <p>
          {t(
            'Права наследуются вложенными папками и файлами. Из прав пользователя, его групп и родительских папок действует самое широкое. Владелец личной папки сохраняет полный доступ.',
          )}
        </p>
      </div>
      <h3 className="section-title">{t('Права, назначенные в этой папке')}</h3>
      <State
        loading={grants.loading}
        error={grants.error}
        empty={!grants.value?.length}
        icon="users"
        title={t('Прямых разрешений нет')}
        description={t('Могут действовать права родительской папки или владельца.')}
        retry={grants.refresh}
      />
      <div className="compact-list">
        {grants.value?.map((grant) => (
          <div key={`${grant.principal_type}:${grant.principal_id}`}>
            <span className="small-symbol">
              <Icon name={grant.principal_type === 'group' ? 'users' : 'shield'} />
            </span>
            <div>
              <strong>{grant.principal_name ?? grant.principal_id}</strong>
              <small>
                {grant.principal_type === 'group' ? t('Группа') : t('Пользователь')} ·{' '}
                {t(roles[grant.role])}
              </small>
            </div>
            <select
              aria-label={t('Роль {0}', [grant.principal_name ?? grant.principal_id])}
              value={grant.role}
              disabled={task.busy}
              onChange={(e) =>
                void task.run(() => save({ ...grant, role: Number(e.target.value) }))
              }
            >
              {roles.map((role, i) => (
                <option key={i} value={i}>
                  {i === 0 ? t('Убрать разрешение') : t(role)}
                </option>
              ))}
            </select>
          </div>
        ))}
      </div>
      <h3 className="section-title">{t('Добавить пользователя или группу')}</h3>
      <form
        className="search-form"
        onSubmit={(e) => {
          e.preventDefault();
          setSearch(query);
          setOffset(0);
          setSelected('');
        }}
      >
        <input
          aria-label={t('Поиск пользователя или группы')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('Имя пользователя или группы')}
          maxLength={128}
        />
        <Button type="submit" icon="search">
          {t('Найти')}
        </Button>
      </form>
      <State loading={principals.loading} error={principals.error} retry={principals.refresh} />
      <div className="form-grid">
        <label>
          {t('Кому')}
          <select
            aria-label={t('Кому')}
            value={selected}
            onChange={(e) => setSelected(e.target.value)}
          >
            <option value="">{t('Выберите из списка')}</option>
            {principals.value?.map((p) => (
              <option key={`${p.type}:${p.id}`} value={`${p.type}:${p.id}`}>
                {p.name}
                {p.type === 'group' ? t(' · группа') : ''}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('Роль')}
          <select
            aria-label={t('Роль нового участника')}
            value={role}
            onChange={(e) => setRole(Number(e.target.value))}
          >
            {roles.slice(1).map((label, i) => (
              <option key={i} value={i + 1}>
                {t(label)}
              </option>
            ))}
          </select>
        </label>
      </div>
      <Pager
        offset={offset}
        count={principals.value?.length ?? 0}
        onChange={(value) => {
          setOffset(value);
          setSelected('');
        }}
      />
      <p className="hint">
        {t(
          'Читатель скачивает. Редактор добавляет, перемещает и удаляет файлы. Управляющий также выдаёт права и создаёт публичные ссылки.',
        )}
      </p>
      <ErrorBox message={task.error} />
      <div className="modal-footer">
        <Button onClick={onClose} disabled={task.busy}>
          {t('Готово')}
        </Button>
        <Button
          variant="primary"
          icon="plus"
          disabled={task.busy || !selectedPrincipal}
          onClick={() =>
            void task.run(async () => {
              if (selectedPrincipal)
                await save({
                  principal_type: selectedPrincipal.type,
                  principal_id: selectedPrincipal.id,
                  role,
                });
            })
          }
        >
          {t('Добавить доступ')}
        </Button>
      </div>
    </Modal>
  );
}
