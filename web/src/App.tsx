import { useCallback, useEffect, useState } from 'react';
import { api, APIError, errorMessage, setCSRF } from './api';
import type { Session, UIInfo } from './types';
import { AppContext } from './context';
import { useTask } from './hooks';
import { Icon } from './components/Icon';
import type { IconName } from './components/Icon';
import { Button, ErrorBox, IconButton } from './components/UI';
import { UploadContext, Uploads } from './components/Uploads';
import { UploadManager } from './uploads';
import { Files } from './features/Files';
import { Devices, SharesPage, Trash } from './features/Account';
import { Groups, SettingsPage, Users } from './features/Admin';

export function App() {
  const [session, setSession] = useState<Session | null>();
  const [info, setInfo] = useState<UIInfo>();
  const [error, setError] = useState('');
  const [revision, setRevision] = useState(0);
  const [toast, setToast] = useState('');
  const notify = useCallback((message: string) => setToast(message), []);
  const authenticate = useCallback((session: Session) => {
    setCSRF(session.csrf_token);
    setSession(session);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setError('');
    setSession(undefined);
    void Promise.all([
      api<UIInfo>('/api/v1/ui', 'GET', undefined, controller.signal),
      api<Session>('/api/v1/me', 'GET', undefined, controller.signal).catch((error) => {
        if (error instanceof APIError && error.status === 401) return null;
        throw error;
      }),
    ])
      .then(([info, session]) => {
        if (controller.signal.aborted) return;
        setInfo(info);
        if (session) authenticate(session);
        else setSession(null);
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          setError(errorMessage(error));
          setSession(null);
        }
      });
    const unauthorized = () => {
      setSession(null);
      setCSRF('');
      setToast('Сессия завершилась. Войдите снова.');
    };
    window.addEventListener('sharefile:unauthorized', unauthorized);
    return () => {
      controller.abort();
      window.removeEventListener('sharefile:unauthorized', unauthorized);
    };
  }, [revision, authenticate]);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(''), 6000);
    return () => clearTimeout(timer);
  }, [toast]);
  return (
    <>
      {session === undefined ? (
        <div className="boot-screen">
          <Brand />
          <span className="spinner" />
          <p>Подключаемся к хранилищу…</p>
        </div>
      ) : session ? (
        <AppContext value={{ session, notify }}>
          <Workspace
            key={`${session.user.id}:${session.admin_listener}`}
            session={session}
            onLogout={() => {
              setCSRF('');
              setSession(null);
            }}
          />
        </AppContext>
      ) : (
        <Login
          info={info}
          error={error}
          retry={() => setRevision((v) => v + 1)}
          onLogin={authenticate}
        />
      )}
      {toast && (
        <div className="toast" role="status">
          <Icon name="check" />
          <span>{toast}</span>
          <IconButton icon="close" label="Закрыть уведомление" onClick={() => setToast('')} />
        </div>
      )}
    </>
  );
}
function Brand() {
  return (
    <div className="brand">
      <span className="brand-symbol">
        <Icon name="logo" />
      </span>
      <span>
        Share<span className="brand-light">File</span>
      </span>
    </div>
  );
}
function Login({
  info,
  error,
  retry,
  onLogin,
}: {
  info?: UIInfo;
  error: string;
  retry: () => void;
  onLogin: (session: Session) => void;
}) {
  const [username, setUsername] = useState(''),
    [password, setPassword] = useState('');
  const task = useTask();
  return (
    <div className="login-page">
      <div className="login-art">
        <Brand />
        <div className="login-art-content">
          <div className="art-orbit orbit-one" />
          <div className="art-orbit orbit-two" />
          <div className="art-file file-one">
            <Icon name="folder" />
            <span>Ваши файлы</span>
          </div>
          <div className="art-file file-two">
            <Icon name="link" />
            <span>Простой обмен</span>
          </div>
          <div className="art-file file-three">
            <Icon name="shield" />
            <span>Ваши правила</span>
          </div>
          <div className="art-center">
            <Icon name="logo" />
          </div>
        </div>
        <div className="login-art-copy">
          <div className="eyebrow">ВАШ ДИСК. ВАШЕ ПРОСТРАНСТВО.</div>
          <h2>
            Всё важное —<br />в одном месте.
          </h2>
          <p>
            Храните файлы дома. Делитесь ими
            <br />с теми, кому доверяете.
          </p>
        </div>
        <div className="login-art-footer">
          <Icon name="lock" />
          Личное хранилище на вашем устройстве
        </div>
      </div>
      <main className="login-main">
        <div className="mobile-brand">
          <Brand />
        </div>
        <div className="login-form">
          <span className="login-mark">
            <Icon name={info?.admin_listener ? 'shield' : 'folder'} />
          </span>
          <div className="eyebrow">
            {info?.admin_listener ? 'ПРИВАТНЫЙ ДОСТУП' : 'ДОБРО ПОЖАЛОВАТЬ'}
          </div>
          <h1>{info?.admin_listener ? 'Управление хранилищем' : 'Войти в ShareFile'}</h1>
          <p>
            {info?.admin_listener
              ? 'Войдите с учётной записью администратора.'
              : 'Используйте учётную запись, созданную администратором.'}
          </p>
          <ErrorBox message={error} />
          {error && (
            <Button icon="refresh" onClick={retry}>
              Проверить подключение
            </Button>
          )}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void task.run(async () => {
                try {
                  const result = await api<Session>('/api/v1/auth/login', 'POST', {
                    username,
                    password,
                  });
                  setPassword('');
                  onLogin(result);
                } catch (error) {
                  if (error instanceof APIError && error.status === 401)
                    throw new Error('Неверное имя пользователя или пароль.', { cause: error });
                  throw error;
                }
              });
            }}
          >
            <label>
              Имя пользователя
              <input
                autoFocus
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
                placeholder="Ваш логин"
              />
            </label>
            <label>
              Пароль
              <input
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                placeholder="Введите пароль"
              />
            </label>
            <ErrorBox message={task.error} />
            <Button type="submit" variant="primary" disabled={task.busy}>
              {task.busy ? 'Входим…' : 'Войти в хранилище'}
              <Icon name="chevron" />
            </Button>
          </form>
          <div className="login-help">
            <Icon name="info" />
            <span>
              Нет доступа или забыли пароль?
              <br />
              Обратитесь к администратору хранилища.
            </span>
          </div>
        </div>
        <footer>
          ShareFile <span>Личное пространство для ваших файлов</span>
        </footer>
      </main>
    </div>
  );
}

const navigation: { page: string; title: string; icon: IconName; admin?: boolean }[] = [
  { page: 'files', title: 'Мои файлы', icon: 'folder' },
  { page: 'spaces', title: 'Доступные папки', icon: 'users' },
  { page: 'shares', title: 'Публичные ссылки', icon: 'link' },
  { page: 'trash', title: 'Корзина', icon: 'trash' },
  { page: 'devices', title: 'Устройства', icon: 'device' },
  { page: 'users', title: 'Пользователи', icon: 'users', admin: true },
  { page: 'groups', title: 'Группы', icon: 'shield', admin: true },
  { page: 'settings', title: 'Настройки', icon: 'settings', admin: true },
];
function getRoute() {
  const [page = 'files', folder] = window.location.hash.replace(/^#\/?/, '').split('/');
  return {
    page: navigation.some((item) => item.page === page) ? page : 'files',
    folder: folder && /^[A-Za-z0-9_-]{43}$/.test(folder) ? folder : undefined,
  };
}
function Workspace({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const [route, setRoute] = useState(getRoute);
  const [mobileMenu, setMobileMenu] = useState(false);
  const [queue, setQueue] = useState(true);
  const [uploadRevision, setUploadRevision] = useState(0);
  const [pairing, setPairing] = useState(() => {
    const id =
      window.location.pathname === '/device/connect'
        ? new URLSearchParams(window.location.search).get('request')
        : null;
    return id && /^[A-Za-z0-9_-]{43}$/.test(id) ? id : null;
  });
  const [uploads] = useState(
    () =>
      new UploadManager(session.user.id, () => {
        setUploadRevision((value) => value + 1);
        window.dispatchEvent(new Event('sharefile:uploaded'));
      }),
  );
  const task = useTask();
  const admin = session.user.admin && session.admin_listener;
  const page = pairing
    ? 'devices'
    : !admin && ['users', 'groups', 'settings'].includes(route.page)
      ? 'files'
      : route.page;
  useEffect(() => {
    const handler = () => {
      setRoute(getRoute());
      setMobileMenu(false);
    };
    window.addEventListener('hashchange', handler);
    return () => window.removeEventListener('hashchange', handler);
  }, []);
  useEffect(() => {
    const unload = (event: BeforeUnloadEvent) => {
      if (uploads.getSnapshot().some((job) => ['uploading', 'queued'].includes(job.status))) {
        event.preventDefault();
        event.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', unload);
    return () => {
      uploads.pauseAll();
      window.removeEventListener('beforeunload', unload);
    };
  }, [uploads]);
  function navigate(page: string, folder?: string) {
    window.location.hash = `/${page}${folder ? `/${folder}` : ''}`;
    setMobileMenu(false);
  }
  function finishPairing() {
    setPairing(null);
    history.replaceState({}, '', '/#/devices');
    setRoute({ page: 'devices', folder: undefined });
  }
  const folderID = route.folder ?? session.user.home_id;
  useEffect(() => {
    const closeMenus = (event: Event) => {
      const current = event.target instanceof Element ? event.target.closest('.action-menu') : null;
      document.querySelectorAll<HTMLDetailsElement>('.action-menu[open]').forEach((menu) => {
        if (menu !== current || event.type === 'keydown') menu.open = false;
      });
    };
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeMenus(event);
    };
    document.addEventListener('click', closeMenus);
    document.addEventListener('keydown', key);
    return () => {
      document.removeEventListener('click', closeMenus);
      document.removeEventListener('keydown', key);
    };
  }, []);
  return (
    <UploadContext value={uploads}>
      <div className="workspace">
        <aside className={`sidebar ${mobileMenu ? 'open' : ''}`}>
          <div className="sidebar-brand">
            <Brand />
            <IconButton
              icon="close"
              label="Закрыть меню"
              className="mobile-only"
              onClick={() => setMobileMenu(false)}
            />
          </div>
          <div className="sidebar-label">ХРАНИЛИЩЕ</div>
          <nav aria-label="Основное меню">
            {navigation
              .filter((item) => !item.admin)
              .map((item) => (
                <button
                  key={item.page}
                  className={`nav-item ${page === item.page ? 'active' : ''}`}
                  onClick={() => navigate(item.page)}
                >
                  <Icon name={item.icon} />
                  <span>{item.title}</span>
                  {page === item.page && <span className="nav-active-dot" />}
                </button>
              ))}
          </nav>
          {admin && (
            <>
              <div className="sidebar-label admin-label">АДМИНИСТРИРОВАНИЕ</div>
              <nav aria-label="Администрирование">
                {navigation
                  .filter((item) => item.admin)
                  .map((item) => (
                    <button
                      key={item.page}
                      className={`nav-item ${page === item.page ? 'active' : ''}`}
                      onClick={() => navigate(item.page)}
                    >
                      <Icon name={item.icon} />
                      <span>{item.title}</span>
                    </button>
                  ))}
              </nav>
            </>
          )}
          <div className="sidebar-bottom">
            <div className="storage-info">
              <span className="status-dot" />
              <div>
                <strong>Личное хранилище</strong>
                <small>
                  {session.admin_listener
                    ? 'Доступ в локальной сети / VPN'
                    : 'Файлы на вашем диске'}
                </small>
              </div>
            </div>
            <div className="sidebar-user">
              <span className="avatar">{session.user.username.slice(0, 2).toUpperCase()}</span>
              <div>
                <strong>{session.user.username}</strong>
                <small>{session.user.admin ? 'Администратор' : 'Пользователь'}</small>
              </div>
              <IconButton
                icon="logout"
                label="Выйти"
                disabled={task.busy}
                onClick={() =>
                  void task.run(async () => {
                    uploads.pauseAll();
                    await api('/api/v1/auth/logout', 'POST');
                    onLogout();
                  })
                }
              />
            </div>
          </div>
        </aside>
        {mobileMenu && (
          <button
            className="sidebar-overlay"
            aria-label="Закрыть меню"
            onClick={() => setMobileMenu(false)}
          />
        )}
        <div className="main-column">
          <header className="topbar">
            <div>
              <IconButton
                icon="menu"
                label="Открыть меню"
                className="mobile-only"
                onClick={() => setMobileMenu(true)}
              />
              <span className="topbar-title">
                {navigation.find((item) => item.page === page)?.title}
              </span>
            </div>
            <div className="topbar-meta">
              {admin && (
                <span className="private-badge">
                  <Icon name="shield" />
                  Приватная админка
                </span>
              )}
              <span className="topbar-user">
                <Icon name="shield" />
                {session.user.username}
              </span>
            </div>
          </header>
          <main className="main-content">
            <ErrorBox message={task.error} />
            {page === 'files' && (
              <Files
                key={folderID}
                folderID={folderID}
                onFolder={(id) => (id ? navigate('files', id) : navigate('spaces'))}
                uploadRevision={uploadRevision}
                onQueue={() => setQueue(true)}
              />
            )}
            {page === 'spaces' && (
              <Files
                folderID=""
                spaces
                onFolder={(id) => (id ? navigate('files', id) : navigate('spaces'))}
                uploadRevision={uploadRevision}
                onQueue={() => setQueue(true)}
              />
            )}
            {page === 'shares' && <SharesPage />}
            {page === 'trash' && <Trash />}
            {page === 'devices' && <Devices requestID={pairing} onDone={finishPairing} />}
            {admin && page === 'users' && <Users />}
            {admin && page === 'groups' && <Groups />}
            {admin && page === 'settings' && <SettingsPage />}
          </main>
          <footer className="workspace-footer">
            <span>ShareFile</span>
            <span>Ваши файлы. Под вашим контролем.</span>
          </footer>
        </div>
        <Uploads expanded={queue} onToggle={() => setQueue((value) => !value)} />
      </div>
    </UploadContext>
  );
}
