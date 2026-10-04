import { useEffect, useId, useRef } from 'react';
import type { ReactNode } from 'react';
import { Icon } from './Icon';
import type { IconName } from './Icon';

export function Button({
  children,
  icon,
  variant = 'secondary',
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  icon?: IconName;
  variant?: 'primary' | 'secondary' | 'danger' | 'ghost';
}) {
  return (
    <button type="button" {...props} className={`button ${variant} ${props.className ?? ''}`}>
      {icon && <Icon name={icon} />}
      {children}
    </button>
  );
}
export function IconButton({
  icon,
  label,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { icon: IconName; label: string }) {
  return (
    <button
      type="button"
      {...props}
      className={`icon-button ${props.className ?? ''}`}
      title={label}
      aria-label={label}
    >
      <Icon name={icon} />
    </button>
  );
}
export function Modal({
  title,
  subtitle,
  children,
  onClose,
  busy = false,
  wide = false,
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
  onClose: () => void;
  busy?: boolean;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleID = useId();
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className={`modal ${wide ? 'wide' : ''}`}
      aria-labelledby={titleID}
      onCancel={(e) => {
        e.preventDefault();
        if (!busy) onClose();
      }}
    >
      <div className="modal-heading">
        <div>
          <h2 id={titleID}>{title}</h2>
          {subtitle && <p>{subtitle}</p>}
        </div>
        <IconButton icon="close" label="Закрыть" onClick={onClose} disabled={busy} />
      </div>
      {children}
    </dialog>
  );
}
export function ErrorBox({ message }: { message?: string }) {
  return message ? (
    <div className="error-box" role="alert">
      <Icon name="info" />
      <span>{message}</span>
    </div>
  ) : null;
}
export function State({
  loading,
  error,
  empty,
  icon = 'folder',
  title = 'Здесь пока пусто',
  description,
  action,
  retry,
}: {
  loading?: boolean;
  error?: string;
  empty?: boolean;
  icon?: IconName;
  title?: string;
  description?: string;
  action?: ReactNode;
  retry?: () => void;
}) {
  if (loading)
    return (
      <div className="empty-state" role="status">
        <span className="spinner" />
        <p>Загружаем…</p>
      </div>
    );
  if (error)
    return (
      <div className="empty-state">
        <ErrorBox message={error} />
        {retry && (
          <Button icon="refresh" onClick={retry}>
            Попробовать снова
          </Button>
        )}
      </div>
    );
  if (empty)
    return (
      <div className="empty-state">
        <div className="empty-icon">
          <Icon name={icon} />
        </div>
        <h3>{title}</h3>
        {description && <p>{description}</p>}
        {action}
      </div>
    );
  return null;
}
export function PageTitle({
  eyebrow,
  title,
  description,
  children,
}: {
  eyebrow?: string;
  title: string;
  description?: string;
  children?: ReactNode;
}) {
  return (
    <div className="page-title">
      <div>
        {eyebrow && <div className="eyebrow">{eyebrow}</div>}
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      <div className="page-actions">{children}</div>
    </div>
  );
}
export function Confirm({
  title,
  description,
  label,
  onClose,
  onConfirm,
  busy,
  error,
  danger = false,
}: {
  title: string;
  description: string;
  label: string;
  onClose: () => void;
  onConfirm: () => void;
  busy: boolean;
  error: string;
  danger?: boolean;
}) {
  return (
    <Modal title={title} onClose={onClose} busy={busy}>
      <p className="modal-copy">{description}</p>
      <ErrorBox message={error} />
      <div className="modal-footer">
        <Button onClick={onClose} disabled={busy}>
          Отмена
        </Button>
        <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} disabled={busy}>
          {busy ? 'Подождите…' : label}
        </Button>
      </div>
    </Modal>
  );
}
