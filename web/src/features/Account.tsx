import { useState } from 'react';
import { api, idPath } from '../api';
import { useResource, useTask } from '../hooks';
import { useApp } from '../context';
import { bytes, date } from '../utils';
import type { Device, DeviceRequest, FileNode, Share } from '../types';
import { Button, Confirm, ErrorBox, Modal, PageTitle, State } from '../components/UI';
import { Icon } from '../components/Icon';
import { FolderPicker, Pager } from '../components/FolderPicker';

export function Trash() {
  const { notify } = useApp();
  const [offset, setOffset] = useState(0);
  const data = useResource<FileNode[]>(`/api/v1/trash?limit=100&offset=${offset}`);
  const [selected, setSelected] = useState<{ node: FileNode; action: 'restore' | 'purge' }>();
  return (
    <>
      <PageTitle
        eyebrow="УПРАВЛЕНИЕ ФАЙЛАМИ"
        title="Корзина"
        description="Удалённые файлы хранятся 7 дней. После восстановления публичные ссылки остаются отозванными."
      >
        <Button icon="refresh" onClick={data.refresh}>
          Обновить
        </Button>
      </PageTitle>
      <section className="content-card">
        <State
          loading={data.loading}
          error={data.error}
          empty={!data.value?.length}
          icon="trash"
          title="Корзина пуста"
          description="Здесь будут файлы и папки, которые ещё можно восстановить."
          retry={data.refresh}
        />
        <div className="item-list">
          {data.value?.map((node) => (
            <div className="item-row" key={node.id}>
              <span className={`file-symbol ${node.kind === 'folder' ? 'folder' : 'document'}`}>
                <Icon name={node.kind} />
              </span>
              <div className="item-copy">
                <strong>{node.name}</strong>
                <small>
                  Удалён {date(node.deleted_at)} ·{' '}
                  {node.kind === 'folder' ? 'Папка' : bytes(node.size)}
                </small>
              </div>
              <div className="row-actions">
                <Button icon="refresh" onClick={() => setSelected({ node, action: 'restore' })}>
                  Восстановить
                </Button>
                <Button variant="ghost" onClick={() => setSelected({ node, action: 'purge' })}>
                  Удалить навсегда
                </Button>
              </div>
            </div>
          ))}
        </div>
        <Pager offset={offset} count={data.value?.length ?? 0} onChange={setOffset} />
      </section>
      {selected && (
        <TrashDialog
          node={selected.node}
          action={selected.action}
          onClose={() => setSelected(undefined)}
          onSaved={() => {
            data.refresh();
            notify(selected.action === 'restore' ? 'Восстановлено' : 'Удалено навсегда');
          }}
        />
      )}
    </>
  );
}
function TrashDialog({
  node,
  action,
  onClose,
  onSaved,
}: {
  node: FileNode;
  action: 'restore' | 'purge';
  onClose: () => void;
  onSaved: () => void;
}) {
  const [custom, setCustom] = useState(false);
  const [target, setTarget] = useState('');
  const folder = useResource<FileNode>(target ? `/api/v1/nodes/${idPath(target)}` : null);
  const task = useTask();
  const submit = () =>
    task.run(async () => {
      await api(
        `/api/v1/trash/${idPath(node.id)}${action === 'restore' ? '/restore' : ''}`,
        action === 'restore' ? 'POST' : 'DELETE',
        action === 'restore' ? { parent_id: custom ? target : '' } : undefined,
      );
      onSaved();
      onClose();
    });
  if (action === 'purge')
    return (
      <Confirm
        title="Удалить навсегда?"
        description={`«${node.name}» будет безвозвратно удалён${node.kind === 'folder' ? ' вместе со всем содержимым, в том числе удалённым ранее' : ''}.`}
        label="Удалить навсегда"
        danger
        busy={task.busy}
        error={task.error}
        onClose={onClose}
        onConfirm={() => void submit()}
      />
    );
  return (
    <Modal title="Восстановить" subtitle={node.name} onClose={onClose} busy={task.busy}>
      <label className="checkbox-label">
        <input type="checkbox" checked={custom} onChange={(e) => setCustom(e.target.checked)} />
        Выбрать другую папку
      </label>
      {custom ? (
        <FolderPicker value={target} onChange={setTarget} />
      ) : (
        <p className="modal-copy">
          Файл вернётся в прежнюю папку. При совпадении имени сервер добавит порядковый номер.
        </p>
      )}
      <ErrorBox message={task.error} />
      <div className="modal-footer">
        <Button onClick={onClose} disabled={task.busy}>
          Отмена
        </Button>
        <Button
          variant="primary"
          disabled={task.busy || (custom && (!target || (folder.value?.effective_role ?? 0) < 2))}
          onClick={() => void submit()}
        >
          Восстановить
        </Button>
      </div>
    </Modal>
  );
}

export function SharesPage() {
  const { notify } = useApp();
  const [offset, setOffset] = useState(0);
  const data = useResource<Share[]>(`/api/v1/shares?limit=100&offset=${offset}`);
  const [selected, setSelected] = useState<Share>();
  const task = useTask();
  return (
    <>
      <PageTitle
        eyebrow="ДЕЛИТЕСЬ ФАЙЛАМИ"
        title="Публичные ссылки"
        description="Все ссылки, созданные вашей учётной записью. Создать новую можно в разделе файлов."
      >
        <Button icon="refresh" onClick={data.refresh}>
          Обновить
        </Button>
      </PageTitle>
      <section className="content-card">
        <State
          loading={data.loading}
          error={data.error}
          empty={!data.value?.length}
          icon="link"
          title="Публичных ссылок пока нет"
          description="Откройте файл или папку и нажмите «Поделиться»."
          retry={data.refresh}
        />
        <div className="item-list">
          {data.value?.map((link) => (
            <ShareRow key={link.id} link={link} onRevoke={() => setSelected(link)} />
          ))}
        </div>
        <Pager offset={offset} count={data.value?.length ?? 0} onChange={setOffset} />
      </section>
      <div className="page-note">
        <Icon name="info" />
        <span>
          Адрес ссылки показывается только при создании. Сохранённую ссылку можно отозвать, а при
          потере адреса — создать новую.
        </span>
      </div>
      {selected && (
        <Confirm
          title="Отозвать публичную ссылку?"
          description="Люди с этой ссылкой потеряют доступ. Отзыв нельзя отменить."
          label="Отозвать"
          danger
          onClose={() => setSelected(undefined)}
          busy={task.busy}
          error={task.error}
          onConfirm={() =>
            void task.run(async () => {
              await api(`/api/v1/shares/${idPath(selected.id)}`, 'DELETE');
              setSelected(undefined);
              data.refresh();
              notify('Ссылка отозвана');
            })
          }
        />
      )}
    </>
  );
}
function ShareRow({ link, onRevoke }: { link: Share; onRevoke: () => void }) {
  const node = useResource<FileNode>(link.revoked ? null : `/api/v1/nodes/${idPath(link.node_id)}`);
  const inactive = link.revoked || Boolean(link.expires_at && link.expires_at <= Date.now() / 1000);
  return (
    <div className="item-row">
      <span className={`file-symbol ${inactive ? 'neutral' : 'document'}`}>
        <Icon name={link.password_protected ? 'lock' : 'link'} />
      </span>
      <div className="item-copy">
        <strong>
          {node.value?.name ?? (node.loading ? 'Загружаем название…' : 'Ссылка на файл или папку')}
        </strong>
        <small>
          Создана {date(link.created_at)} ·{' '}
          {link.expires_at ? `до ${date(link.expires_at)}` : 'без срока'}
          {link.password_protected && ' · с паролем'}
        </small>
      </div>
      <span className={`badge ${inactive ? 'neutral' : 'green'}`}>
        {link.revoked ? 'Отозвана' : inactive ? 'Истекла' : 'Активна'}
      </span>
      {!link.revoked && <Button onClick={onRevoke}>Отозвать</Button>}
    </div>
  );
}

export function Devices({ requestID, onDone }: { requestID: string | null; onDone: () => void }) {
  const { notify } = useApp();
  const data = useResource<Device[]>('/api/v1/devices');
  const pairing = useResource<DeviceRequest>(
    requestID ? `/api/v1/device-requests/${idPath(requestID)}` : null,
  );
  const [selected, setSelected] = useState<Device>();
  const task = useTask();
  return (
    <>
      <PageTitle
        eyebrow="ВАША УЧЁТНАЯ ЗАПИСЬ"
        title="Устройства"
        description="Приложения, которым вы разрешили доступ к хранилищу."
      >
        <Button icon="refresh" onClick={data.refresh}>
          Обновить
        </Button>
      </PageTitle>
      {requestID && (
        <section className="pairing-card">
          <span className="file-symbol document">
            <Icon name="device" />
          </span>
          <div>
            <h2>Подключение устройства</h2>
            <State loading={pairing.loading} error={pairing.error} retry={pairing.refresh} />
            {pairing.value && (
              <>
                <p>
                  Разрешить доступ приложению <strong>«{pairing.value.name}»</strong>?
                </p>
                <p className="hint">
                  Подтверждайте только запрос, который вы начали сами. Приложение получит доступ с
                  правами вашей учётной записи. Запрос действует до{' '}
                  {new Date(pairing.value.expires_at * 1000).toLocaleTimeString('ru')}.
                </p>
                <ErrorBox message={task.error} />
                <div className="page-actions">
                  <Button onClick={onDone} disabled={task.busy}>
                    Не подключать
                  </Button>
                  <Button
                    variant="primary"
                    icon="check"
                    disabled={task.busy}
                    onClick={() =>
                      void task.run(async () => {
                        await api(`/api/v1/device-requests/${idPath(requestID)}/approve`, 'POST');
                        onDone();
                        data.refresh();
                        notify('Подключение разрешено. Вернитесь в приложение.');
                      })
                    }
                  >
                    Разрешить доступ
                  </Button>
                </div>
              </>
            )}
            {pairing.error && <Button onClick={onDone}>Закрыть запрос</Button>}
          </div>
        </section>
      )}
      <section className="content-card">
        <State
          loading={data.loading}
          error={data.error}
          empty={!data.value?.length}
          icon="device"
          title="Устройств пока нет"
          description="Когда вы подключите приложение, оно появится здесь."
          retry={data.refresh}
        />
        <div className="item-list">
          {data.value?.map((device) => (
            <div className="item-row" key={device.id}>
              <span className="file-symbol document">
                <Icon name="device" />
              </span>
              <div className="item-copy">
                <strong>{device.name}</strong>
                <small>
                  Подключено {date(device.created_at)} · до {date(device.expires_at)}
                </small>
              </div>
              <span className={`badge ${device.revoked ? 'neutral' : 'green'}`}>
                {device.revoked
                  ? 'Отозвано'
                  : device.expires_at <= Date.now() / 1000
                    ? 'Истекло'
                    : 'Подключено'}
              </span>
              {!device.revoked && (
                <Button onClick={() => setSelected(device)}>Отозвать доступ</Button>
              )}
            </div>
          ))}
        </div>
      </section>
      {selected && (
        <Confirm
          title="Отключить устройство?"
          description={`Приложение «${selected.name}» потеряет доступ. Для повторного подключения потребуется новое подтверждение.`}
          label="Отключить"
          danger
          busy={task.busy}
          error={task.error}
          onClose={() => setSelected(undefined)}
          onConfirm={() =>
            void task.run(async () => {
              await api(`/api/v1/devices/${idPath(selected.id)}`, 'DELETE');
              setSelected(undefined);
              data.refresh();
              notify('Доступ устройства отозван');
            })
          }
        />
      )}
    </>
  );
}
