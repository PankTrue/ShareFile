import { t } from '../i18n';
import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { api } from '../api';
import { useResource, useTask } from '../hooks';
import { useApp } from '../context';
import type { Configuration, ServerConfig, Volume } from '../types';
import { bytes } from '../utils';
import { Button, Confirm, ErrorBox, State } from '../components/UI';

export function ServerSettings() {
  const data = useResource<Configuration>('/api/v1/admin/config');
  const volumes = useResource<Volume[]>('/api/v1/admin/volumes');
  return (
    <>
      <State loading={data.loading} error={data.error} retry={data.refresh} />
      {data.value && (
        <ConfigurationForm
          key={data.value.revision}
          snapshot={data.value}
          volumes={volumes.value ?? []}
          volumesError={volumes.error}
          refreshVolumes={volumes.refresh}
          onSaved={data.mutate}
        />
      )}
    </>
  );
}

const configLabels: Record<keyof ServerConfig, string> = {
  data_dir: 'Директория хранения',
  volume_id: 'Метка хранилища',
  public_addr: 'Адрес публичного сервера',
  public_url: 'Публичный URL',
  admin_addr: 'Адрес административного сервера',
  admin_url: 'URL администрирования',
  admin_cert: 'Файл сертификата TLS',
  admin_key: 'Файл приватного ключа TLS',
  web_dir: 'Каталог Web UI',
  import_dirs: 'Источники импорта',
  reserve_bytes: 'Резерв свободного места, байт',
  upload_slots: 'Одновременные загрузки',
  download_slots: 'Одновременные скачивания',
  chunk_bytes: 'Размер части загрузки, байт',
  log_file: 'Файл журнала',
  insecure_local: 'HTTP для локальной разработки',
};

function ConfigurationForm({
  snapshot,
  volumes,
  volumesError,
  refreshVolumes,
  onSaved,
}: {
  snapshot: Configuration;
  volumes: Volume[];
  volumesError: string;
  refreshVolumes: () => void;
  onSaved: (value: Configuration) => void;
}) {
  const { notify } = useApp();
  const initial = snapshot.pending ?? snapshot.active;
  const [draft, setDraft] = useState<ServerConfig>(initial);
  const [confirm, setConfirm] = useState(false);
  const [applying, setApplying] = useState(snapshot.applying);
  const [applyError, setApplyError] = useState('');
  const task = useTask();
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const busy = task.busy || applying;
  const changes = (Object.keys(configLabels) as (keyof ServerConfig)[]).filter(
    (key) => JSON.stringify(snapshot.active[key]) !== JSON.stringify(initial[key]),
  );
  const storageChanged = draft.data_dir !== snapshot.active.data_dir;
  const changedAddress = draft.admin_url.replace(/\/$/, '') !== window.location.origin;

  useEffect(() => {
    if (!applying) return;
    let stopped = false;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const state = await api<Configuration>(
          '/api/v1/admin/config',
          'GET',
          undefined,
          controller.signal,
        );
        if (stopped) return;
        if (!state.applying && state.last_error) {
          setApplyError(state.last_error);
          setApplying(false);
          return;
        }
        if (!state.applying && !state.pending) {
          if (!changedAddress && draft.web_dir) window.location.reload();
          return;
        }
      } catch {
        // HTTP is unavailable while copying storage and restarting listeners.
      }
      if (!stopped) timer = setTimeout(() => void poll(), 2000);
    };
    timer = setTimeout(() => void poll(), 1500);
    return () => {
      stopped = true;
      clearTimeout(timer);
      controller.abort();
    };
  }, [applying, changedAddress, draft.web_dir]);

  function text(key: keyof ServerConfig, hint?: string, required = false) {
    return (
      <label>
        {t(configLabels[key])}
        <input
          aria-label={t(configLabels[key])}
          value={String(draft[key] ?? '')}
          required={required}
          autoComplete="off"
          onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
        />
        {hint && <span className="hint">{hint}</span>}
      </label>
    );
  }
  function number(
    key: 'reserve_bytes' | 'chunk_bytes' | 'upload_slots' | 'download_slots',
    min: number,
    max: number,
  ) {
    return (
      <label>
        {t(configLabels[key])}
        <input
          aria-label={t(configLabels[key])}
          type="number"
          min={min}
          max={max}
          step={1}
          required
          value={draft[key]}
          onChange={(e) => setDraft({ ...draft, [key]: e.target.valueAsNumber })}
        />
        {(key === 'reserve_bytes' || key === 'chunk_bytes') && (
          <span className="hint">{bytes(draft[key])}</span>
        )}
      </label>
    );
  }
  const mount = volumes
    .filter(
      (v) =>
        draft.data_dir === v.path || draft.data_dir.startsWith(v.path.replace(/\/$/, '') + '/'),
    )
    .sort((a, b) => b.path.length - a.path.length)[0];
  return (
    <form
      className="server-settings"
      onSubmit={(e) => {
        e.preventDefault();
        void task.run(async () => {
          for (const key of [
            'reserve_bytes',
            'chunk_bytes',
            'upload_slots',
            'download_slots',
          ] as const)
            if (!Number.isSafeInteger(draft[key]))
              throw new Error('Числовые параметры должны быть целыми.');
          const config = {
            ...draft,
            import_dirs: draft.import_dirs.map((p) => p.trim()).filter(Boolean),
          };
          const saved = await api<Configuration>('/api/v1/admin/config', 'PUT', {
            config,
            revision: snapshot.revision,
          });
          onSaved(saved);
          notify('Черновик настроек сохранён');
        });
      }}
    >
      <fieldset disabled={busy}>
        <SettingsSection
          title={t('Хранилище')}
          description={t('Выберите подключённый диск или укажите абсолютный путь на сервере.')}
        >
          <label>
            {t('Подключённый диск')}
            <select
              aria-label={t('Подключённый диск')}
              value={mount?.path ?? ''}
              onChange={(e) => {
                if (!e.target.value) return;
                setDraft({ ...draft, data_dir: e.target.value.replace(/\/$/, '') + '/sharefile' });
              }}
            >
              <option value="">{t('Указать директорию вручную')}</option>
              {volumes.map((v) => (
                <option key={v.path} value={v.path} disabled={v.read_only}>
                  {t(v.device)} · {v.path}
                  {v.current ? t(' · используется') : ''}
                  {v.read_only ? t(' · только чтение') : ''}
                </option>
              ))}
            </select>
          </label>
          {mount && (
            <p className="hint">
              {t('Свободно')} {bytes(mount.free_bytes)} {t('из')} {bytes(mount.total_bytes)}
              {mount.filesystem ? ` · ${mount.filesystem}` : ''}
            </p>
          )}
          <Button icon="refresh" onClick={refreshVolumes}>
            {t('Обновить диски')}
          </Button>
          <ErrorBox message={volumesError} />
          <div className="form-grid">
            {text(
              'data_dir',
              t(
                'Родительская директория должна существовать. Новая директория хранения — пустая или ещё не созданная.',
              ),
              true,
            )}
            {text(
              'volume_id',
              t(
                'Проверяет, что подключено нужное хранилище. Новую метку можно задать при переносе в другую директорию.',
              ),
              true,
            )}
            {number('reserve_bytes', 0, Number.MAX_SAFE_INTEGER)}
          </div>
          <p className="hint">
            {t('Сейчас используется:')} {snapshot.active.data_dir}
          </p>
          {storageChanged && (
            <div className="info-box">
              {t(
                'При применении файлы, база, аккаунты и незавершённые загрузки будут скопированы в новую директорию. Исходная копия останется на старом диске. Дождитесь завершения переноса перед отключением диска.',
              )}
            </div>
          )}
        </SettingsSection>
        <SettingsSection
          title={t('Передача файлов')}
          description={t('Количество одновременных передач и размер одной части загрузки.')}
        >
          <div className="form-grid">
            {number('upload_slots', 1, 1024)}
            {number('download_slots', 1, 1024)}
            {number('chunk_bytes', 1, 1073741824)}
          </div>
        </SettingsSection>
        <SettingsSection
          title={t('Адреса доступа')}
          description={t(
            'Публичный URL используется для ссылок. Административный сервер доступен в локальной сети или VPN.',
          )}
        >
          <div className="form-grid">
            {text('public_addr', t('IP:порт, например 127.0.0.1:8080'), true)}
            {text('public_url', t('HTTPS-адрес, по которому получатели открывают ссылки.'), true)}
            {text('admin_addr', t('IP:порт, например 192.168.1.1:8081'), true)}
            {text('admin_url', t('HTTPS-адрес интерфейса администрирования.'), true)}
          </div>
        </SettingsSection>
        <SettingsSection
          title={t('TLS и локальная разработка')}
          description={t('Укажите пути к существующим файлам сертификата и ключа на сервере.')}
        >
          <div className="form-grid">
            {text('admin_cert', undefined, !draft.insecure_local)}
            {text('admin_key', undefined, !draft.insecure_local)}
          </div>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={draft.insecure_local}
              onChange={(e) => setDraft({ ...draft, insecure_local: e.target.checked })}
            />
            {t('HTTP для локальной разработки')}
          </label>
          <p className="hint">
            {t(
              'HTTP разрешён только для адресов 127.0.0.1 / ::1 и localhost. В локальной сети нужен HTTPS.',
            )}
          </p>
        </SettingsSection>
        <SettingsSection
          title={t('Источники импорта')}
          description={t('Абсолютные пути к существующим директориям, по одному на строку.')}
        >
          <label>
            {t('Директории импорта')}
            <textarea
              aria-label={t('Директории импорта')}
              rows={4}
              value={draft.import_dirs.join('\n')}
              onChange={(e) => setDraft({ ...draft, import_dirs: e.target.value.split('\n') })}
            />
          </label>
          <p className="hint">
            {t(
              'Очистите поле, чтобы отключить импорт. Файлы копируются в хранилище по отдельному действию.',
            )}
          </p>
        </SettingsSection>
        <SettingsSection
          title={t('Web UI и журнал')}
          description={t('Пути на сервере. Пустой путь журнала направляет логи в stderr.')}
        >
          <div className="form-grid">
            {text(
              'web_dir',
              t('Каталог собранного интерфейса с index.html. Пустой путь отключает Web UI.'),
            )}
            {text('log_file', t('Например /opt/var/log/sharefile/server.jsonl'))}
          </div>
        </SettingsSection>
        <section className="settings-card settings-save">
          <h2>{t('Сохранение и применение')}</h2>
          <p className="hint">
            {t(
              'Черновик сохраняется на сервере. При применении сервер завершит текущие запросы и перезапустит слушатели. Передачи могут потребовать продолжения.',
            )}
          </p>
          {snapshot.pending && (
            <p className="hint">
              {t('Изменения:')}{' '}
              {changes.map((k) => t(configLabels[k])).join(', ') ||
                t('параметры совпадают с текущими')}
              .
            </p>
          )}
          <ErrorBox message={task.error || applyError || snapshot.last_error} />
          <div className="page-actions">
            <Button type="submit" variant="primary" disabled={!dirty}>
              {t('Сохранить черновик')}
            </Button>
            {snapshot.pending && (
              <>
                <Button icon="refresh" disabled={dirty} onClick={() => setConfirm(true)}>
                  {t('Применить и перезапустить')}
                </Button>
                <Button
                  onClick={() =>
                    void task.run(async () => {
                      await api('/api/v1/admin/config', 'DELETE', { revision: snapshot.revision });
                      onSaved(await api<Configuration>('/api/v1/admin/config'));
                      notify('Черновик удалён');
                    })
                  }
                >
                  {t('Удалить черновик')}
                </Button>
              </>
            )}
          </div>
        </section>
      </fieldset>
      {applying && (
        <section className="settings-card" role="status">
          <h2>{t('Применяем настройки…')}</h2>
          <p className="hint">
            {storageChanged
              ? t('Копируем хранилище. Время зависит от объёма данных и скорости дисков.')
              : t('Перезапускаем сервер.')}{' '}
            {t('Доступ появится после завершения.')}
          </p>
          {changedAddress ? (
            <a className="button primary" href={draft.admin_url + '/#/settings'}>
              {t('Открыть новый адрес')}
            </a>
          ) : (
            <p className="hint">{t('Страница обновится автоматически после запуска.')}</p>
          )}
        </section>
      )}
      {confirm && (
        <Confirm
          title={t('Применить настройки сервера?')}
          description={t('{0}Доступ временно приостановится.{1}{2}', [
            storageChanged
              ? t('Хранилище будет скопировано в ') +
                draft.data_dir +
                t('. Исходные данные сохранятся. ')
              : '',
            changedAddress ? t(' Затем откройте ') + draft.admin_url + '.' : '',
            !draft.web_dir ? t(' Каталог Web UI пуст: интерфейс отключится.') : '',
          ])}
          label={t('Применить')}
          busy={task.busy}
          error={task.error}
          onClose={() => setConfirm(false)}
          onConfirm={() =>
            void task.run(async () => {
              await api('/api/v1/admin/config/apply', 'POST', { revision: snapshot.revision });
              setConfirm(false);
              setApplyError('');
              setApplying(true);
            })
          }
        />
      )}
    </form>
  );
}

function SettingsSection({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <section className="settings-card">
      <div className="settings-heading">
        <div>
          <h2>{title}</h2>
          <p>{description}</p>
        </div>
      </div>
      {children}
    </section>
  );
}
