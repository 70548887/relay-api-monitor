import { expect, it } from 'vitest';
import { shortTime } from './lib';

it('writes a near time as month, day, hour and minute in Shanghai time', () => {
  expect(shortTime('2026-11-21T03:12:59Z')).toBe('11/21 11:12');
  expect(shortTime('2026-11-20T16:05:00Z')).toBe('11/21 00:05');
  expect(shortTime('2027-01-01T15:59:00Z')).toBe('01/01 23:59');
});
