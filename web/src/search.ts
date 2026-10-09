export type SearchFilters = {
  query: string;
  type: string;
  after: string;
  before: string;
  minSize: string;
  maxSize: string;
  sizeUnit: string;
};

export const emptySearch: SearchFilters = {
  query: '',
  type: '',
  after: '',
  before: '',
  minSize: '',
  maxSize: '',
  sizeUnit: '1048576',
};

// Date inputs refer to local calendar days. The upper API bound is exclusive,
// so advance by one calendar day (also works across daylight saving changes).
export function searchParams(filters: SearchFilters): string {
  const params = new URLSearchParams();
  const query = filters.query.trim();
  if (new TextEncoder().encode(query).length > 240)
    throw new Error('Поисковый запрос должен быть не длиннее 240 байт.');
  params.set('query', query);
  if (filters.type) params.set('type', filters.type);
  if (filters.after && filters.before && filters.after > filters.before)
    throw new Error('Начальная дата должна быть не позже конечной.');
  for (const [key, value] of [
    ['created_after', filters.after],
    ['created_before', filters.before],
  ]) {
    if (!value) continue;
    const day = new Date(`${value}T00:00:00`);
    if (!Number.isFinite(day.getTime())) throw new Error('Укажите корректную дату.');
    if (key === 'created_before') day.setDate(day.getDate() + 1);
    const seconds = Math.floor(day.getTime() / 1000);
    if (seconds < 0) throw new Error('Дата должна быть не раньше 1970 года.');
    params.set(key, String(seconds));
  }
  const sizes: number[] = [];
  for (const [key, value] of [
    ['min_size', filters.minSize],
    ['max_size', filters.maxSize],
  ]) {
    if (!value) continue;
    const raw = Number(value) * Number(filters.sizeUnit);
    const size = key === 'min_size' ? Math.ceil(raw) : Math.floor(raw);
    if (!Number.isFinite(raw) || raw < 0 || !Number.isSafeInteger(size))
      throw new Error('Размер должен быть неотрицательным числом до 9 007 199 254 740 991 байт.');
    params.set(key, String(size));
    sizes.push(size);
  }
  if (sizes.length === 2 && sizes[0] > sizes[1])
    throw new Error('Минимальный размер должен быть не больше максимального.');
  if (filters.type === 'folder' && (filters.minSize || filters.maxSize))
    throw new Error('Фильтр размера применяется только к файлам.');
  return params.toString();
}
