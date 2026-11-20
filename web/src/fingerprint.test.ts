import { expect, it } from 'vitest';
import { agoText, intervalText, verdictShort } from './fingerprint';

it('names a detection interval in the largest unit that divides it', () => {
  expect(intervalText(3600)).toBe('1 小时');
  expect(intervalText(86400)).toBe('24 小时');
  expect(intervalText(129600)).toBe('36 小时');
  expect(intervalText(172800)).toBe('2 天');
  expect(intervalText(2592000)).toBe('30 天');
  expect(intervalText(5400)).toBe('90 分钟');
});

it('says how long ago a time was in whole minutes, hours or days', () => {
  const now = Date.parse('2026-11-20T12:00:00Z');
  const before = (seconds: number) => agoText(new Date(now - seconds * 1000).toISOString(), now);
  expect(before(0)).toBe('不到 1 分钟前');
  expect(before(59)).toBe('不到 1 分钟前');
  expect(before(60)).toBe('1 分钟前');
  expect(before(119)).toBe('1 分钟前');
  expect(before(3599)).toBe('59 分钟前');
  expect(before(3600)).toBe('1 小时前');
  expect(before(86399)).toBe('23 小时前');
  expect(before(86400)).toBe('1 天前');
  expect(before(3 * 86400 + 5)).toBe('3 天前');
  // A clock that runs behind the server must not show a time in the future.
  expect(before(-30)).toBe('不到 1 分钟前');
});

it('shortens only the verdict whose wording is long', () => {
  expect(verdictShort('family')).toBe('同家族');
  expect(verdictShort('match')).toBe('一致');
  expect(verdictShort('mismatch')).toBe('不一致');
  expect(verdictShort('unlisted')).toBe('未收录');
  expect(verdictShort('undetermined')).toBe('无法判定');
});
