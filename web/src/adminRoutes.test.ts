import { describe, expect, it } from 'vitest';
import { adminLoginPath, safeAdminNext } from './adminRoutes';

describe('administrator login destinations', () => {
  it('preserves an administration path, query, and fragment', () => {
    expect(safeAdminNext('/admin/updates?channel=stable#history')).toBe('/admin/updates?channel=stable#history');
    expect(adminLoginPath('/admin?view=recent#queue')).toBe('/admin/login?next=%2Fadmin%3Fview%3Drecent%23queue');
  });

  it.each([
    undefined, '', 'https://example.com/admin', '//example.com/admin', '/test', '/administrator',
    '/admin/login', '/admin/login?next=/admin', '/admin/setup/', '/admin/../test',
    '/admin/%2e%2e/test', '/admin/%2f%2fexample.com', '/admin\\example.com', '/admin\n/settings',
  ])('uses the overview for an invalid return path: %s', value => {
    expect(safeAdminNext(value)).toBe('/admin');
  });
});
