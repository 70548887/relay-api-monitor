import { useSyncExternalStore } from 'react';

type Tracking = { id: string; token: string; expiresAt: number };
const entries = new Map<string, Tracking>();
const listeners = new Set<() => void>();
const notify = () => listeners.forEach(listener => listener());

// Recovery credentials survive an administrator logout only in this tab's
// memory. They are scoped to one read-only operation and never persisted.
export function trackRestore(id: string, token: string, expiresAt = Date.now() + 60 * 60 * 1000) {
  entries.set(id, { id, token, expiresAt });
  notify();
}
export function forgetRestore(id: string) { entries.delete(id); notify(); }
export function useRestoreTracking(id: string) {
  return useSyncExternalStore(listener => { listeners.add(listener); return () => listeners.delete(listener); }, () => entries.get(id));
}
