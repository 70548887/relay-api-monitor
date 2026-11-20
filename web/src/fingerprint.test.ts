import { expect, it } from 'vitest';
import { intervalText } from './fingerprint';

it('names a detection interval in the largest unit that divides it', () => {
  expect(intervalText(3600)).toBe('1 小时');
  expect(intervalText(86400)).toBe('24 小时');
  expect(intervalText(129600)).toBe('36 小时');
  expect(intervalText(172800)).toBe('2 天');
  expect(intervalText(2592000)).toBe('30 天');
  expect(intervalText(5400)).toBe('90 分钟');
});
