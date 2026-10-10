import { getLanguage, setLanguage, t } from '../i18n';

export function LanguageSwitch() {
  return (
    <label className="language-switch">
      <span>{t('Язык')}</span>
      <select
        aria-label={t('Язык')}
        value={getLanguage()}
        onChange={(event) => setLanguage(event.target.value === 'ru' ? 'ru' : 'en')}
      >
        <option value="en" lang="en">
          English
        </option>
        <option value="ru" lang="ru">
          Русский
        </option>
      </select>
    </label>
  );
}
