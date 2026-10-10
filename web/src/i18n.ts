import { useEffect, useSyncExternalStore } from 'react';
import translations from './translations.json';

export type Language = 'en' | 'ru';
export type Message = string | { key: string; values: readonly (string | number)[] };
const storageKey = 'sharefile.language';
const cookieName = 'sf_language';
const catalogue: Readonly<Record<string, string>> = translations;
const listeners = new Set<() => void>();

export function readLanguage(): Language {
  if (typeof document === 'undefined') return 'en';
  const cookie = document.cookie.match(/(?:^|;\s*)sf_language=(en|ru)(?:;|$)/)?.[1];
  if (cookie === 'en' || cookie === 'ru') return cookie;
  try {
    return localStorage.getItem(storageKey) === 'ru' ? 'ru' : 'en';
  } catch {
    return 'en';
  }
}
let language = readLanguage();
export function getLanguage(): Language {
  return language;
}
export function message(key: string, values: readonly (string | number)[]): Message {
  return { key, values };
}

// Russian source text is the translation key and the Russian catalogue.
// Translate only interface messages, never filenames, user input, or preview content.
export function t(input: Message, values: readonly (string | number)[] = []): string {
  const key = typeof input === 'string' ? input : input.key;
  const parameters = typeof input === 'string' ? values : input.values;
  const text =
    language === 'ru'
      ? (serverDiagnostics[key] ?? translateRussianDetail(key))
      : (catalogue[key] ?? translateDetail(key));
  return text.replace(/\{(\d+)\}/g, (placeholder, index: string) =>
    parameters[Number(index)] === undefined ? placeholder : String(parameters[Number(index)]),
  );
}
// Configuration errors include server paths and nested diagnostics. Preserve those values.
function translateRussianDetail(text: string): string {
  for (const prefix of detailPrefixes) {
    if (text.startsWith(prefix)) return prefix + t(text.slice(prefix.length));
  }
  return text;
}
function translateDetail(text: string): string {
  const address = text.match(/^Не удалось открыть адрес (.*): (.*)$/);
  if (address) return t('Не удалось открыть адрес {0}: {1}', [address[1], address[2]]);
  for (const prefix of detailPrefixes) {
    if (text.startsWith(prefix)) return catalogue[prefix] + t(text.slice(prefix.length));
  }
  return text;
}
const detailPrefixes = [
  'Настройки не применены: ',
  'Проверьте параметры конфигурации: ',
  'Директория должна существовать: ',
  'Новая директория хранения должна быть пустой: ',
  'Проверьте сертификат TLS и приватный ключ: ',
  'Нет доступа на запись к директории: ',
];
const serverDiagnostics: Readonly<Record<string, string>> = {
  'absolute data_dir and volume_id are required':
    'Укажите абсолютный путь data_dir и метку volume_id',
  'listeners must have different addresses': 'Серверы должны использовать разные адреса',
  'transfer slots must be 1..1024, chunk_bytes 1..1073741824, reserve_bytes 0..9007199254740991':
    'Число передач: 1..1024, chunk_bytes: 1..1073741824, reserve_bytes: 0..9007199254740991',
  'origin URL required': 'Укажите URL без дополнительных компонентов пути',
  'HTTPS required outside loopback development':
    'За пределами локальной разработки требуется HTTPS',
  'listener port must be 1..65535': 'Порт сервера должен быть в диапазоне 1..65535',
  'listeners must bind an explicit loopback or private LAN/VPN address':
    'Серверы должны использовать конкретный локальный адрес или адрес локальной сети/VPN',
  'insecure_local requires loopback listeners':
    'insecure_local требует адресов локальной разработки',
  'admin TLS certificate and key required':
    'Укажите сертификат и приватный ключ TLS административного сервера',
  'absolute import directories required': 'Укажите абсолютные пути директорий импорта',
  'absolute log, web and TLS paths required':
    'Укажите абсолютные пути журнала, Web UI и файлов TLS',
};
function updateLanguage(next: Language) {
  if (language === next) return;
  language = next;
  listeners.forEach((listener) => listener());
}
export function setLanguage(next: Language) {
  if (typeof document !== 'undefined') {
    // This cookie contains only a preference and also localizes server-rendered shared pages.
    document.cookie = `${cookieName}=${next}; Path=/; Max-Age=31536000; SameSite=Lax${location.protocol === 'https:' ? '; Secure' : ''}`;
    try {
      localStorage.setItem(storageKey, next);
    } catch {
      // Language switching still works when browser storage is unavailable.
    }
  }
  updateLanguage(next);
}
function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
export function useLanguage() {
  const current = useSyncExternalStore(subscribe, getLanguage, () => 'en' as const);
  useEffect(() => {
    document.documentElement.lang = current;
    document.title = t('ShareFile — ваше хранилище');
  }, [current]);
  useEffect(() => {
    function sync(event: StorageEvent) {
      if (event.key === storageKey && (event.newValue === 'en' || event.newValue === 'ru'))
        updateLanguage(event.newValue);
    }
    window.addEventListener('storage', sync);
    return () => window.removeEventListener('storage', sync);
  }, []);
  return current;
}
