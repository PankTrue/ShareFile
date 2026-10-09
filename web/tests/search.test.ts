import { describe, expect, it } from 'vitest';
import { emptySearch, searchParams } from '../src/search';

describe('global search filters', () => {
  it('encodes literal Unicode queries and fractional sizes in bytes', () => {
    const params = new URLSearchParams(
      searchParams({
        ...emptySearch,
        query: '  Отчёт %_ & +  ',
        type: 'document',
        minSize: '0.5',
        maxSize: '1',
        sizeUnit: '1024',
      }),
    );
    expect(params.get('query')).toBe('Отчёт %_ & +');
    expect(params.get('type')).toBe('document');
    expect(params.get('min_size')).toBe('512');
    expect(params.get('max_size')).toBe('1024');
  });

  it('includes both selected local calendar days', () => {
    const params = new URLSearchParams(
      searchParams({ ...emptySearch, after: '2026-10-09', before: '2026-10-09' }),
    );
    expect(Number(params.get('created_after'))).toBe(
      new Date('2026-10-09T00:00:00').getTime() / 1000,
    );
    expect(Number(params.get('created_before'))).toBe(
      new Date('2026-10-10T00:00:00').getTime() / 1000,
    );
  });

  it('preserves zero size and rejects invalid or unsafe ranges', () => {
    expect(
      new URLSearchParams(searchParams({ ...emptySearch, maxSize: '0' })).get('max_size'),
    ).toBe('0');
    for (const invalid of [
      { minSize: '-1' },
      { minSize: '-0.1', sizeUnit: '1' },
      { maxSize: 'Infinity' },
      { maxSize: '9007199254740992', sizeUnit: '1' },
      { minSize: '2', maxSize: '1' },
      { after: '2026-10-10', before: '2026-10-09' },
      { after: 'invalid' },
      { type: 'folder', maxSize: '0' },
      { query: 'я'.repeat(121) },
    ])
      expect(() => searchParams({ ...emptySearch, ...invalid })).toThrow();
  });
});
