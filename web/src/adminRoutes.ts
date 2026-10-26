const adminBase = 'https://relay-lab.invalid';

/** Keep login return paths inside the administration area. */
export function safeAdminNext(value: string | null | undefined): string {
  if (!value || !value.startsWith('/') || value.startsWith('//') || /[\\\u0000-\u001f\u007f]/.test(value)) return '/admin';
  try {
    const target = new URL(value, adminBase);
    if (target.origin !== adminBase || !/^\/admin(?:\/[a-z0-9-]+)*\/?$/.test(target.pathname)) return '/admin';
    if (/^\/admin\/(?:login|setup)\/?$/.test(target.pathname)) return '/admin';
    return `${target.pathname}${target.search}${target.hash}`;
  } catch {
    return '/admin';
  }
}

export function adminLoginPath(next: string): string {
  return `/admin/login?next=${encodeURIComponent(safeAdminNext(next))}`;
}
