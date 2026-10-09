import { useEffect, useRef, useState } from 'react';
import { idPath } from '../api';
import { useResource } from '../hooks';
import type { FileNode } from '../types';
import { bytes } from '../utils';
import { Button, Modal, State } from './UI';
import { Icon } from './Icon';

type Preview = {
  kind: 'image' | 'text' | 'pdf' | 'audio' | 'video' | 'unsupported';
  media_type: string;
  text: string;
  message: string;
  truncated: boolean;
};

export function FilePreview({
  files,
  initialIndex,
  onClose,
}: {
  files: FileNode[];
  initialIndex: number;
  onClose: () => void;
}) {
  const [index, setIndex] = useState(initialIndex);
  const node = files[index];
  useEffect(() => {
    function navigate(event: KeyboardEvent) {
      if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      // Let native players and editable controls keep their own arrow shortcuts.
      if (
        event.target instanceof Element &&
        event.target.closest('audio, video, input, textarea, select, [contenteditable], pre')
      )
        return;
      if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
        event.preventDefault();
        setIndex((current) =>
          Math.max(0, Math.min(files.length - 1, current + (event.key === 'ArrowRight' ? 1 : -1))),
        );
      }
    }
    document.addEventListener('keydown', navigate);
    return () => document.removeEventListener('keydown', navigate);
  }, [files.length]);
  return (
    <Modal
      title={node.name}
      subtitle={`${bytes(node.size)} · Предпросмотр без публичной ссылки`}
      onClose={onClose}
      className="file-preview"
    >
      <PreviewContent key={node.id} node={node} />
      <div className="preview-toolbar">
        <div className="preview-navigation" aria-label="Галерея файлов">
          <Button
            icon="previous"
            disabled={index === 0}
            onClick={() => setIndex(index - 1)}
            aria-label="Предыдущий файл"
          />
          <span role="status" aria-live="polite">
            {index + 1} из {files.length}
          </span>
          <Button
            icon="chevron"
            disabled={index === files.length - 1}
            onClick={() => setIndex(index + 1)}
            aria-label="Следующий файл"
          />
        </div>
        <a className="button secondary" href={`/api/v1/nodes/${idPath(node.id)}/content`}>
          <Icon name="download" /> Скачать файл
        </a>
      </div>
      <p className="preview-hint">← → — файлы на этой странице · Esc — закрыть</p>
    </Modal>
  );
}

function PreviewContent({ node }: { node: FileNode }) {
  const preview = useResource<Preview>(`/api/v1/nodes/${idPath(node.id)}/preview`);
  const [failed, setFailed] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const value = preview.value;
  const src = `/api/v1/nodes/${idPath(node.id)}/preview/content`;
  function retry() {
    setFailed(false);
    setLoaded(false);
    preview.refresh();
  }
  if (preview.loading || preview.error)
    return <State loading={preview.loading} error={preview.error} retry={retry} />;
  if (!value) return null;
  if (failed)
    return (
      <State
        error="Не удалось открыть файл. Возможно, браузер не поддерживает этот формат или кодек, либо доступ к файлу изменился. Попробуйте снова или скачайте файл."
        retry={retry}
      />
    );
  if (value.kind === 'unsupported')
    return <State empty icon="file" title="Предпросмотр недоступен" description={value.message} />;
  if (value.kind === 'text')
    return (
      <>
        {value.truncated && (
          <p className="preview-notice" role="status">
            Показаны первые 256 КиБ. Скачайте файл для просмотра целиком.
          </p>
        )}
        <pre className="preview-text" tabIndex={0} aria-label={`Текст ${node.name}`}>
          {value.text || 'Файл пуст.'}
        </pre>
      </>
    );
  return (
    <div className={`preview-stage preview-${value.kind}`} aria-busy={!loaded}>
      {!loaded && (
        <div className="preview-loading" role="status">
          <span className="spinner" /> Открываем файл…
        </div>
      )}
      {value.kind === 'image' && (
        <img
          src={src}
          alt={node.name}
          onLoad={() => setLoaded(true)}
          onError={() => setFailed(true)}
        />
      )}
      {value.kind === 'pdf' && (
        <>
          <iframe
            src={src}
            title={`PDF ${node.name}`}
            onLoad={(event) => {
              if (event.currentTarget.contentDocument?.contentType === 'application/json')
                setFailed(true);
              else setLoaded(true);
            }}
            onError={() => setFailed(true)}
          />
          <p className="preview-hint">
            Если браузер не показывает PDF, воспользуйтесь кнопкой «Скачать файл».
          </p>
        </>
      )}
      {(value.kind === 'audio' || value.kind === 'video') && (
        <NativePlayer
          kind={value.kind}
          src={src}
          name={node.name}
          onLoaded={() => setLoaded(true)}
          onError={() => setFailed(true)}
        />
      )}
    </div>
  );
}

function NativePlayer({
  kind,
  src,
  name,
  onLoaded,
  onError,
}: {
  kind: 'audio' | 'video';
  src: string;
  name: string;
  onLoaded: () => void;
  onError: () => void;
}) {
  const audio = useRef<HTMLAudioElement>(null);
  const video = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const player = audio.current ?? video.current;
    return () => {
      if (player) {
        player.pause();
        player.removeAttribute('src');
        player.load();
      }
    };
  }, [src]);
  const props = { controls: true, preload: 'metadata', src, onLoadedMetadata: onLoaded, onError };
  return kind === 'audio' ? (
    <audio ref={audio} {...props} aria-label={`Аудио ${name}`} />
  ) : (
    <video ref={video} {...props} playsInline aria-label={`Видео ${name}`} />
  );
}
