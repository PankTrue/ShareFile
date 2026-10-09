import { createContext, useContext, useState, useSyncExternalStore } from 'react';
import { UploadManager } from '../uploads';
import { bytes } from '../utils';
import { errorMessage } from '../api';
import { Icon } from './Icon';
import { Button, ErrorBox, IconButton } from './UI';
import { copy } from '../utils';
import { directoryFromFiles } from '../directories';

export const UploadContext = createContext<UploadManager | null>(null);
export function useUploads() {
  const manager = useContext(UploadContext);
  if (!manager) throw new Error('Missing upload manager');
  return manager;
}
export function Uploads({ expanded, onToggle }: { expanded: boolean; onToggle: () => void }) {
  const manager = useUploads();
  const jobs = useSyncExternalStore(manager.subscribe, manager.getSnapshot);
  const [error, setError] = useState('');
  const [copiedID, setCopiedID] = useState('');
  const active = jobs.filter((job) => job.status !== 'complete').length;
  async function action(work: () => Promise<void>) {
    setError('');
    try {
      await work();
    } catch (error) {
      setError(errorMessage(error));
    }
  }
  if (!jobs.length) return null;
  return (
    <aside className={`upload-panel ${expanded ? 'expanded' : ''}`} aria-label="Очередь загрузок">
      <button className="upload-panel-heading" onClick={onToggle} aria-expanded={expanded}>
        <span>
          <Icon name="upload" />
          <strong>Загрузки</strong>
          <span className="count">{active || jobs.length}</span>
        </span>
        <Icon name={expanded ? 'close' : 'chevron'} />
      </button>
      {expanded && (
        <div className="upload-list">
          <ErrorBox message={error} />
          {jobs.map((job) => (
            <div className="upload-job" key={job.id}>
              <div className="upload-job-top">
                <Icon
                  name={job.status === 'complete' ? 'check' : job.directory ? 'folder' : 'file'}
                />
                <div>
                  <strong title={job.name}>{job.name}</strong>
                  <small>
                    {job.folderName} · {bytes(job.offset)} из {bytes(job.size)}
                  </small>
                </div>
                {['queued', 'uploading'].includes(job.status) ? (
                  <IconButton
                    icon="pause"
                    label={`Приостановить ${job.name}`}
                    onClick={() => manager.pause(job.id)}
                  />
                ) : ['paused', 'error'].includes(job.status) ||
                  (job.status === 'reselect' &&
                    job.directory?.files.every((entry) => entry.job.status === 'complete')) ? (
                  <IconButton
                    icon="play"
                    label={`Продолжить ${job.name}`}
                    onClick={() => manager.resume(job.id)}
                  />
                ) : null}
                {job.status !== 'sharing' && (
                  <IconButton
                    icon="close"
                    label={
                      job.status === 'complete'
                        ? `Убрать ${job.name} из списка`
                        : `Отменить ${job.name}`
                    }
                    onClick={() => void action(() => manager.remove(job.id))}
                  />
                )}
              </div>
              <progress
                value={job.status === 'complete' ? Math.max(1, job.size) : job.offset}
                max={Math.max(1, job.size)}
                aria-label={`Прогресс ${job.name}`}
              />
              <small className={job.error ? 'text-danger' : 'muted'}>
                {job.status === 'complete'
                  ? job.directory
                    ? 'Папка загружена · Ссылка на 7 дней'
                    : 'Сохранён в хранилище'
                  : job.status === 'uploading'
                    ? 'Загружается…'
                    : job.status === 'sharing'
                      ? 'Создаётся ссылка на папку…'
                      : job.status === 'queued'
                        ? 'В очереди'
                        : job.status === 'reselect'
                          ? job.directory
                            ? 'Выберите исходную папку, чтобы продолжить'
                            : 'Выберите исходный файл, чтобы продолжить'
                          : job.status === 'paused'
                            ? 'Приостановлено'
                            : job.error}
              </small>
              {job.link && (
                <div className="generated-link">
                  <input
                    readOnly
                    value={job.link}
                    onFocus={(e) => e.target.select()}
                    aria-label={`Ссылка на папку ${job.name}`}
                  />
                  <Button
                    icon="link"
                    onClick={() =>
                      void action(async () => {
                        await copy(job.link!);
                        setCopiedID(job.id);
                      })
                    }
                  >
                    {copiedID === job.id ? 'Ссылка скопирована' : 'Скопировать ссылку'}
                  </Button>
                </div>
              )}
              {job.directory && job.fileID && (
                <a className="uploaded-folder-link" href={`#/files/${job.fileID}`}>
                  Открыть загруженную папку
                </a>
              )}
              {job.directory && job.status === 'reselect' && job.directory.files.length > 0 && (
                <label className="button">
                  Выбрать исходную папку
                  <input
                    type="file"
                    multiple
                    className="visually-hidden"
                    aria-label={`Продолжить загрузку папки ${job.name}`}
                    ref={(node) => {
                      if (node) node.webkitdirectory = true;
                    }}
                    onChange={(e) => {
                      const files = Array.from(e.target.files ?? []);
                      e.target.value = '';
                      void action(async () =>
                        manager.reselectDirectory(job.id, directoryFromFiles(files)),
                      );
                    }}
                  />
                </label>
              )}
              {job.directory && job.status === 'reselect' && (
                <p className="hint">
                  Можно перетащить ту же папку в её прежнюю папку назначения. Пустую папку
                  продолжите кнопкой ▶.
                </p>
              )}
              {job.status === 'reselect' && !job.directory && (
                <label className="button secondary reselect">
                  Выбрать файл
                  <input
                    type="file"
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (file) void action(() => manager.reselect(job.id, file));
                      e.target.value = '';
                    }}
                  />
                </label>
              )}
              {job.status === 'error' && job.error?.includes('не найден') && (
                <p className="hint">Загрузка могла истечь. Отмените её и загрузите файл заново.</p>
              )}
            </div>
          ))}
        </div>
      )}
      {!expanded && (
        <div className="upload-panel-summary">
          {active ? `${active} в очереди · можно продолжить после паузы` : 'Все файлы сохранены'}
        </div>
      )}
    </aside>
  );
}
