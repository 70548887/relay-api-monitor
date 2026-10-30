import { APIError, api, setSession } from './api';

export interface BackupStorage {
  id: string; revision: number; endpoint: string; region: string; bucket: string; prefix: string;
  force_path_style: boolean; access_key_mask?: string; secret_configured: boolean; created_at?: string;
  health?: { checked_at?: string; status: 'untested' | 'ok' | 'failed'; message: string };
}
export interface BackupFilter { state?: string; trigger?: string; protection?: '' | 'ordinary' | 'monthly' | 'protected'; from?: string; to?: string; }
export interface RetentionPreview { count: number; local_copies: number; remote_copies: number; total_bytes: number; items: Pick<BackupRun, 'id' | 'file_name' | 'size_bytes' | 'local_state' | 'remote_state' | 'started_at'>[]; }
export interface BackupStorageInput {
  endpoint: string; region: string; bucket: string; prefix: string; force_path_style: boolean;
  access_key_id?: string; secret_access_key?: string;
}
export interface BackupSchedule {
  enabled: boolean; cron: string; timezone: string; mode: 'local' | 'local_s3'; retain_days: number; retain_count: number;
  monthly_enabled: boolean; monthly_days: number[]; monthly_month_end: boolean; monthly_retain_count: number;
  recipient: string; recipient_id: string; recipient_verified: boolean; next_run_at?: string; reconciliation_required: boolean;
}
export interface BackupRun {
  id: string; request_id: string; state: string; trigger: string; scheduled_for?: string; created_at: string;
  started_at: string; finished_at?: string; heartbeat_at: string; version: string; database_schema: number;
  recipient_id: string; file_name: string; sha256?: string; size_bytes: number; transferred_bytes: number;
  local_state: string; remote_state: string; storage_id?: string; protected: boolean; monthly_archive: boolean;
  archive_dates?: string[]; archive_retain_count: number; expires_at?: string; error_code?: string; message?: string;
  cloud_imported?: boolean;
}
export interface CloudCandidate {
  id: string; file_name: string; size_bytes: number; last_modified?: string; imported: boolean;
  manifest_available?: boolean; status: 'pending' | 'imported'; message?: string;
}
export interface CloudScanResult { items: CloudCandidate[]; continuation_token?: string; truncated: boolean; }
export interface BackupSnapshot {
  schedule: BackupSchedule; storage?: BackupStorage; active?: BackupRun | null; last_success?: BackupRun | null;
  records?: BackupRun[]; next_runs?: string[]; backup_dir?: string; native_restore_supported?: boolean; deployment_kind?: string; capabilities?: string[];
  restore_operation?: RestoreOperation;
  reconciliation_warnings?: string[];
}
export interface RestorePreflight {
  id: string; backup_id?: string; manifest: { format_version?: number; database_schema?: number; app_version?: string; version?: string; created_at?: string };
  expires_at: string; native_restore_supported: boolean; deployment_kind: string; docker_instructions: string[];
}
export interface RestoreOperation {
  id?: string; operation_id?: string; state: string; message?: string; error_code?: string; progress?: number; finished_at?: string;
  started_at?: string; updated_at?: string;
}
export const defaultBackupSchedule: BackupSchedule = {
  enabled: false, cron: '0 2 * * *', timezone: 'Asia/Shanghai', mode: 'local', retain_days: 14, retain_count: 10,
  monthly_enabled: false, monthly_days: [1], monthly_month_end: false, monthly_retain_count: 0,
  recipient: '', recipient_id: '', recipient_verified: false, reconciliation_required: false,
};
const base = '/admin/backups';
export const backupAPI = {
  status: (signal?:AbortSignal) => api<BackupSnapshot>(`${base}/status`,{signal}),
  records: (page: number, filter: BackupFilter = {}, signal?: AbortSignal) => { const query = new URLSearchParams({limit:'20', offset:String((page-1)*20)}); Object.entries(filter).forEach(([name,value]) => { if(value) query.set(name,value); }); return api<BackupRun[]>(`${base}/records?${query}`, {signal}); },
  record: (id: string,signal?:AbortSignal) => api<BackupRun>(`${base}/records/${encodeURIComponent(id)}`,{signal}),
  create: (requestID: string) => api<BackupRun>(`${base}/records`, { method: 'POST', body: JSON.stringify({ request_id: requestID }) }),
  storage: () => api<BackupStorage>(`${base}/storage`),
  saveStorage: (input: BackupStorageInput) => api<BackupStorage>(`${base}/storage`, { method: 'PUT', body: JSON.stringify(input) }),
  testStorage: (input: BackupStorageInput) => api<void>(`${base}/storage/test`, { method: 'POST', body: JSON.stringify(input) }),
  schedule: () => api<BackupSchedule>(`${base}/schedule`),
  saveSchedule: (input: BackupSchedule) => api<BackupSchedule>(`${base}/schedule`, { method: 'PUT', body: JSON.stringify(input) }),
  preview: (input: BackupSchedule) => api<{ next_runs: string[] }>(`${base}/schedule/preview`, { method: 'POST', body: JSON.stringify(input) }),
  retentionPreview: (input: BackupSchedule) => api<RetentionPreview>(`${base}/schedule/retention-preview`, {method:'POST',body:JSON.stringify(input)}),
  generateKey: () => api<{ identity: string; recipient: string }>(`${base}/keys/generate`, { method: 'POST' }),
  setRecipient: (recipient: string) => api<void>(`${base}/keys/recipient`, { method: 'PUT', body: JSON.stringify({ recipient }) }),
  verifyIdentity: (identity: string) => api<void>(`${base}/keys/verify`, { method: 'POST', body: JSON.stringify({ identity }) }),
  reauth: (password: string) => api<void>(`${base}/reauth`, { method: 'POST', body: JSON.stringify({ password }) }),
  retry: (id: string) => api<BackupRun>(`${base}/records/${encodeURIComponent(id)}/retry-upload`, { method: 'POST' }),
  protect: (id: string, protectedValue: boolean) => api<void>(`${base}/records/${encodeURIComponent(id)}/protection`, { method: 'PATCH', body: JSON.stringify({ protected: protectedValue }) }),
  remove: (id: string) => api<void>(`${base}/records/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  reconcile: () => api<void>(`${base}/reconcile`, { method: 'POST' }),
  acknowledgeMissing: (id: string) => api<void>(`${base}/records/${encodeURIComponent(id)}/acknowledge-missing`, { method: 'POST', body: JSON.stringify({ confirm: '确认副本遗失' }) }),
  cloudScan: (continuationToken?: string, signal?: AbortSignal) => api<CloudScanResult>(`${base}/cloud/scan`, { method: 'POST', body: JSON.stringify(continuationToken ? { continuation_token: continuationToken } : {}), signal }),
  cloudImport: (candidateID: string, identity: string) => api<BackupRun>(`${base}/cloud/import`, { method: 'POST', body: JSON.stringify({ candidate_id: candidateID, identity }) }),
  apply: (id: string) => api<{ operation_id: string; status_token?: string }>(`${base}/restore/apply`, { method: 'POST', body: JSON.stringify({ preflight_id: id }) }),
  operation: (id: string, token?: string) => api<RestoreOperation>(`${base}/operations/${encodeURIComponent(id)}`, token ? { headers: { 'X-Restore-Status-Token': token } } : {}),
  async preflight(form: FormData, csrfToken: string) {
    const response = await fetch(`/api/v1${base}/restore/preflight`, { method: 'POST', body: form, credentials: 'same-origin', headers: { 'X-CSRF-Token': csrfToken } });
    if (!response.ok) {
      const body = await response.json().catch(() => null);
      if (response.status === 401) setSession(null);
      throw new APIError(response.status, body?.error?.code || 'backup_preflight_failed', body?.error?.message || `恢复预检失败 (${response.status})`);
    }
    return response.json() as Promise<RestorePreflight>;
  },
  downloadURL: (id: string) => `/api/v1${base}/records/${encodeURIComponent(id)}/download`,
};
