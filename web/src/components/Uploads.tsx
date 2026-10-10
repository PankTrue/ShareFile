import { t } from '../i18n';
import { createContext, useContext, useEffect, useState, useSyncExternalStore } from 'react';
import { UploadManager } from '../uploads';
import { bytes } from '../utils';
import { errorMessage } from '../api';
import { Icon } from './Icon';
import { Button, ErrorBox, IconButton } from './UI';
import { copy } from '../utils';
import { directoryFromFiles } from '../directories';
import { progressSummary, selectionJobs, transferDetails } from '../progress';

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
  const uploading = jobs.some((job) => job.status === 'uploading');
  const [, tick] = useState(0);
  useEffect(() => {
    if (!uploading) return;
    const timer = window.setInterval(() => tick((value) => value + 1), 1000);
    return () => window.clearInterval(timer);
  }, [uploading]);
  const selection = selectionJobs(jobs);
  const rate = selection.reduce((sum, job) => sum + manager.rate(job.id), 0);
  const summary = progressSummary(selection, rate);
  const status = selection.every((job) => job.status === 'complete')
    ? t('Загрузка завершена')
    : selection.some((job) => ['error', 'reselect', 'uncertain'].includes(job.status))
      ? t('Требуется действие · Оставшееся время —')
      : selection.some((job) => job.status === 'paused')
        ? t('Приостановлено · Оставшееся время —')
        : summary.offset === summary.size
          ? t('Данные переданы · Завершаем загрузку…')
          : uploading
            ? transferDetails(rate, summary.seconds)
            : t('В очереди · Оставшееся время —');
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
    <aside
      className={`upload-panel ${expanded ? 'expanded' : ''}`}
      aria-label={t('Очередь загрузок')}
    >
      <button className="upload-panel-heading" onClick={onToggle} aria-expanded={expanded}>
        <span>
          <Icon name="upload" />
          <strong>{t('Загрузки')}</strong>
          <span className="count">{active || jobs.length}</span>
        </span>
        <Icon name={expanded ? 'close' : 'chevron'} />
      </button>
      <div className="upload-panel-summary" aria-label={t('Общий прогресс подборки')}>
        <strong>
          {t('Общий прогресс ·')} {summary.percent}%
        </strong>
        <span>
          {bytes(summary.offset)} {t('из')} {bytes(summary.size)} {t('· Готово')}{' '}
          {summary.completed} {t('из')} {selection.length}
        </span>
        <progress value={summary.percent} max={100} aria-label={t('Общий прогресс загрузки')} />
        <span>{status}</span>
      </div>
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
                    {job.folderName} · {bytes(job.offset)} {t('из')} {bytes(job.size)}
                  </small>
                </div>
                {['queued', 'uploading'].includes(job.status) ? (
                  <IconButton
                    icon="pause"
                    label={t('Приостановить {0}', [job.name])}
                    onClick={() => manager.pause(job.id)}
                  />
                ) : ['paused', 'error'].includes(job.status) ||
                  (job.status === 'reselect' &&
                    job.directory?.files.every((entry) => entry.job.status === 'complete')) ? (
                  <IconButton
                    icon="play"
                    label={t('Продолжить {0}', [job.name])}
                    onClick={() => manager.resume(job.id)}
                  />
                ) : null}
                {job.status !== 'sharing' && (
                  <IconButton
                    icon="close"
                    label={
                      job.status === 'complete'
                        ? t('Убрать {0} из списка', [job.name])
                        : t('Отменить {0}', [job.name])
                    }
                    onClick={() => void action(() => manager.remove(job.id))}
                  />
                )}
              </div>
              <progress
                value={job.status === 'complete' ? Math.max(1, job.size) : job.offset}
                max={Math.max(1, job.size)}
                aria-label={t('Прогресс {0}', [job.name])}
              />
              <small className={job.error ? 'text-danger' : 'muted'}>
                {job.status === 'complete'
                  ? job.directory
                    ? t('Папка загружена · Ссылка на 7 дней')
                    : t('Сохранён в хранилище')
                  : job.status === 'uploading'
                    ? t('Загружается…')
                    : job.status === 'sharing'
                      ? t('Создаётся ссылка на папку…')
                      : job.status === 'queued'
                        ? t('В очереди')
                        : job.status === 'reselect'
                          ? job.directory
                            ? t('Выберите исходную папку, чтобы продолжить')
                            : t('Выберите исходный файл, чтобы продолжить')
                          : job.status === 'paused'
                            ? t('Приостановлено')
                            : t(job.error ?? '')}
              </small>
              {job.status === 'uploading' && job.offset < job.size && (
                <small>
                  {transferDetails(
                    manager.rate(job.id),
                    manager.rate(job.id) > 0
                      ? Math.ceil((job.size - job.offset) / manager.rate(job.id))
                      : null,
                  )}
                </small>
              )}
              {job.link && (
                <div className="generated-link">
                  <input
                    readOnly
                    value={job.link}
                    onFocus={(e) => e.target.select()}
                    aria-label={t('Ссылка на папку {0}', [job.name])}
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
                    {copiedID === job.id ? t('Ссылка скопирована') : t('Скопировать ссылку')}
                  </Button>
                </div>
              )}
              {job.directory && job.fileID && (
                <a className="uploaded-folder-link" href={`#/files/${job.fileID}`}>
                  {t('Открыть загруженную папку')}
                </a>
              )}
              {job.directory && job.status === 'reselect' && job.directory.files.length > 0 && (
                <label className="button">
                  {t('Выбрать исходную папку')}
                  <input
                    type="file"
                    multiple
                    className="visually-hidden"
                    aria-label={t('Продолжить загрузку папки {0}', [job.name])}
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
                  {t(
                    'Можно перетащить ту же папку в её прежнюю папку назначения. Пустую папку продолжите кнопкой ▶.',
                  )}
                </p>
              )}
              {job.status === 'reselect' && !job.directory && (
                <label className="button secondary reselect">
                  {t('Выбрать файл')}
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
                <p className="hint">
                  {t('Загрузка могла истечь. Отмените её и загрузите файл заново.')}
                </p>
              )}
            </div>
          ))}
        </div>
      )}
    </aside>
  );
}
