let csrf = '';
export function setCSRF(token: string) {
  csrf = token;
}

const messages: Record<number, string> = {
  400: 'Проверьте введённые данные. Имя не должно содержать слеши и быть длиннее 240 байт.',
  401: 'Сессия завершилась. Войдите снова.',
  403: 'Недостаточно прав для этого действия.',
  404: 'Объект не найден или больше недоступен.',
  409: 'Операция конфликтует с текущим состоянием. Проверьте имя и выбранную папку.',
  412: 'Версия протокола загрузки не поддерживается.',
  413: 'Размер части файла превышает ограничение сервера.',
  429: 'Сервер занят. Попробуйте ещё раз через несколько секунд.',
  507: 'На диске недостаточно свободного места.',
  503: 'Хранилище временно недоступно. Проверьте подключение диска.',
};

export class APIError extends Error {
  constructor(public status: number) {
    super(messages[status] ?? 'Не удалось выполнить запрос. Попробуйте ещё раз.');
  }
}

export async function request(path: string, init: RequestInit = {}): Promise<Response> {
  // All credentials and CSRF stay on this origin, including private-listener uploads.
  if (!path.startsWith('/api/v1/') || path.startsWith('//'))
    throw new Error('Недопустимый адрес запроса.');
  const headers = new Headers(init.headers);
  if (!['GET', 'HEAD', 'OPTIONS'].includes(init.method ?? 'GET') && csrf)
    headers.set('X-CSRF-Token', csrf);
  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'same-origin',
    redirect: 'error',
  });
  if (!response.ok) {
    if (response.status === 401 && path !== '/api/v1/auth/login' && path !== '/api/v1/me')
      window.dispatchEvent(new Event('sharefile:unauthorized'));
    const error = new APIError(response.status);
    if (path.startsWith('/api/v1/admin/config') && [400, 409].includes(response.status)) {
      const detail = (await response.json().catch(() => null)) as { error?: string } | null;
      if (detail?.error) error.message = detail.error;
    }
    throw error;
  }
  return response;
}

export async function api<T>(
  path: string,
  method = 'GET',
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await request(path, {
    method,
    signal,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export function errorMessage(error: unknown) {
  if (error instanceof TypeError)
    return 'Нет связи с сервером. Проверьте подключение и повторите попытку.';
  return error instanceof Error ? error.message : 'Не удалось выполнить операцию.';
}
export function idPath(id: string) {
  return encodeURIComponent(id);
}
