export function bytes(size: number) {
  if (!Number.isFinite(size) || size < 0) return '—';
  if (size < 1024) return `${size} Б`;
  const units = ['КБ', 'МБ', 'ГБ', 'ТБ'];
  let value = size / 1024,
    index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index++;
  }
  return `${new Intl.NumberFormat('ru', { maximumFractionDigits: 1 }).format(value)} ${units[index]}`;
}
export function date(time?: number) {
  return time
    ? new Intl.DateTimeFormat('ru', { day: 'numeric', month: 'short', year: 'numeric' }).format(
        time * 1000,
      )
    : 'Без срока';
}
export const roles = ['Нет доступа', 'Читатель', 'Редактор', 'Управляющий'];
export function validName(name: string) {
  return (
    name.trim() === name &&
    name.length > 0 &&
    new TextEncoder().encode(name).length <= 240 &&
    !/\p{Cc}|[/\\]/u.test(name) &&
    name !== '.' &&
    name !== '..'
  );
}
export function validPassword(password: string) {
  const size = new TextEncoder().encode(password).length;
  return size >= 12 && size <= 256;
}
export async function copy(text: string) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  // Clipboard API requires HTTPS; selection remains usable on local HTTP development.
  const input = document.createElement('textarea');
  input.value = text;
  input.className = 'clipboard-fallback';
  document.body.appendChild(input);
  input.select();
  const ok = document.execCommand('copy');
  input.remove();
  if (!ok) throw new Error('Выделите ссылку и скопируйте её вручную.');
}
