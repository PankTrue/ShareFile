import { afterEach, describe, expect, it } from 'vitest';
import { APIError } from '../src/api';
import { getLanguage, message, readLanguage, setLanguage, t } from '../src/i18n';
import translations from '../src/translations.json';
import { bytes, date } from '../src/utils';
import { transferDetails } from '../src/progress';

afterEach(() => setLanguage('en'));

describe('Web UI languages', () => {
  it('defaults to English independently of the operating system language', () => {
    expect(readLanguage()).toBe('en');
    expect(getLanguage()).toBe('en');
    expect(t('Мои файлы')).toBe('My files');
  });
  it('translates saved messages and errors at display time', () => {
    const notice = message('Перемещено: {0}', [3]);
    const error = new APIError(403);
    expect(t(notice)).toBe('Moved: 3');
    expect(t(error.message)).toBe('You do not have permission to perform this action.');
    setLanguage('ru');
    expect(t(notice)).toBe('Перемещено: 3');
    expect(t(error.message)).toBe('Недостаточно прав для этого действия.');
  });
  it('preserves dynamic filenames and paths without translating inserted content', () => {
    expect(t('Выбрать {0}', ['Папка {1} $&'])).toBe('Select Папка {1} $&');
    const detail = 'Настройки не применены: Директория должна существовать: /disk/Русский';
    expect(t(detail)).toBe('Settings were not applied: The directory must exist: /disk/Русский');
    setLanguage('ru');
    expect(t(detail)).toBe(detail);
  });
  it('localizes units, dates, and transfer details', () => {
    expect(bytes(1536)).toBe('1.5 KB');
    expect(date()).toBe('No expiry');
    expect(transferDetails(1024, 61)).toBe('1 KB/s · Remaining ≈ 2 min');
    setLanguage('ru');
    expect(bytes(1536)).toBe('1,5 КБ');
    expect(date()).toBe('Без срока');
    expect(transferDetails(1024, 61)).toBe('1 КБ/с · Осталось ≈ 2 мин');
  });
  it('keeps every translation complete with matching interpolation fields', () => {
    for (const [source, english] of Object.entries(translations)) {
      expect(english.trim(), source).not.toBe('');
      expect(english.match(/\{\d+\}/g) ?? [], source).toEqual(source.match(/\{\d+\}/g) ?? []);
    }
  });
});
