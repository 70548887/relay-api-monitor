import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Tag } from 'antd';
import type { FormInstance } from 'antd';
import { backupAPI, StorageTestError } from '../backupApi';
import type { BackupStorage, BackupStorageInput, BackupStorageTestResult } from '../backupApi';
import { captureSessionGuard, useSession } from '../api';

export const STORAGE_TEST_TIMEOUT_MS = 75000;
type TestState = { phase: 'running' | 'ok' | 'failed' | 'unknown' | 'invalid'; elapsedMS: number; scope: string; editVersion: number; message: string; result?: BackupStorageTestResult; code?: string };
interface Props { form: FormInstance<BackupStorageInput>; storage?: BackupStorage; editVersion?: number; disabled?: boolean; onRunningChange: (running: boolean) => void; onSettled: () => Promise<void> | void; }

const phaseNames: Record<string,string> = { validating: '配置校验', dns: '域名解析', connecting: '建立连接', writing: '写入测试对象', reading: '读取测试对象', verifying: '内容校验', cleaning: '清理测试对象', configuration: '配置校验', config: '配置校验', connect: '连接', write: '写入测试对象', read: '读取测试对象', verify: '内容校验', delete: '清理测试对象', cleanup: '清理测试对象', completed: '全部验证完成' };
const seconds = (value: number) => `${Math.max(0,value / 1000).toFixed(1)} 秒`;
function scopeOf(input: BackupStorageInput, storage?: BackupStorage) {
  if (!storage) return '本次表单草稿（尚未保存）';
  const endpoint = (value?: string) => (value || '').trim().replace(/\/+$/, '');
  const prefix = (value?: string) => (value || '').replace(/^\/+|\/+$/g, '') || 'backups';
  const changed = endpoint(input.endpoint) !== endpoint(storage.endpoint) || (input.region || 'auto') !== (storage.region || 'auto') || input.bucket !== storage.bucket || prefix(input.prefix) !== prefix(storage.prefix) || !!input.force_path_style !== !!storage.force_path_style;
  return changed || input.access_key_id || input.secret_access_key ? '本次表单草稿（测试不会保存配置）' : '已保存配置（凭据留空，使用服务端保存的密钥）';
}

export function StorageConnectionTest({ form, storage, editVersion = 0, disabled, onRunningChange, onSettled }: Props) {
  const session = useSession();
  const [state,setState] = useState<TestState>();
  const [healthWarning,setHealthWarning] = useState('');
  const active = useRef(false); const mounted = useRef(false); const generation = useRef(0); const startedAt = useRef(0);
  const controller = useRef<AbortController>();
  const callbacks = useRef({ onRunningChange, onSettled }); callbacks.current = { onRunningChange, onSettled };
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; generation.current++; controller.current?.abort(); if(active.current){active.current=false;callbacks.current.onRunningChange(false);} }; }, []);
  useEffect(() => {
    if (state?.phase !== 'running') return;
    const timer = window.setInterval(() => setState(previous => previous?.phase === 'running' ? { ...previous, elapsedMS: performance.now() - startedAt.current } : previous), 100);
    return () => window.clearInterval(timer);
  }, [state?.phase]);

  async function test() {
    if (active.current || disabled) return;
    active.current = true; const current = ++generation.current; const validSession = captureSessionGuard();
    callbacks.current.onRunningChange(true); setHealthWarning(''); startedAt.current = performance.now();
    const valid = () => mounted.current && generation.current === current && validSession();
    let sent = false; let timeout = false; let timer: ReturnType<typeof setTimeout> | undefined;
    let scope = '本次表单配置';
    setState({ phase: 'running', elapsedMS: 0, scope, editVersion, message: '连接与写入、读取、删除验证进行中，请等待最终结果。' });
    try {
      let input: BackupStorageInput;
      try { input = await form.validateFields(); }
      catch (failure) {
        const fields = (failure as { errorFields?: { name: (string | number)[]; errors?: string[] }[] }).errorFields;
        const first = fields?.[0];
        if (valid()) {
          setState({ phase: 'invalid', elapsedMS: performance.now() - startedAt.current, scope, editVersion, message: first?.errors?.[0] || '请检查端点、区域和存储桶等必填配置；本次未发送连接请求。', code: 'local_validation_failed' });
          if(first) form.getFieldInstance(first.name)?.focus?.();
        }
        return;
      }
      scope = scopeOf(input,storage);
      if (!valid()) return;
      setState(previous => previous ? { ...previous, scope } : previous);
      const request = new AbortController(); controller.current = request;
      timer = setTimeout(() => { timeout = true; request.abort(); }, STORAGE_TEST_TIMEOUT_MS);
      sent = true;
      const result = await backupAPI.testStorage(input,{ signal: request.signal, csrfToken: session?.csrf_token });
      if(valid())setState({ phase: result.status === 'ok' ? 'ok' : 'failed', elapsedMS: performance.now() - startedAt.current, scope, editVersion, message: result.message, result, code: result.error_code });
    } catch (failure) {
      if (!valid()) return;
      if(timeout) setState({ phase:'unknown',elapsedMS:performance.now()-startedAt.current,scope,editVersion,message:'客户端等待已超过 75 秒，已停止等待。服务端结果未知，测试对象清理可能仍在进行；请稍后刷新健康状态核对，勿立即重复测试。',code:'client_timeout' });
      else if((failure as Error).name==='AbortError') setState({phase:'unknown',elapsedMS:performance.now()-startedAt.current,scope,editVersion,message:'本次等待已取消，服务端结果尚未确认。',code:'client_canceled'});
      else { const result=failure instanceof StorageTestError?failure.result:undefined;setState({phase:'failed',elapsedMS:performance.now()-startedAt.current,scope,editVersion,message:(failure as Error).message||'连接测试未完成，请稍后核对。',result,code:failure instanceof StorageTestError?failure.code:undefined}); }
    } finally {
      if(timer)clearTimeout(timer);
      if (mounted.current && generation.current === current) {
        active.current=false;controller.current=undefined;callbacks.current.onRunningChange(false);
        if(sent&&validSession())void Promise.resolve().then(()=>callbacks.current.onSettled()).catch(()=>{if(valid())setHealthWarning('测试结果已保留，但存储健康状态刷新失败，可稍后点击页面刷新。');});
      }
    }
  }

  const running = state?.phase === 'running';
  const edited = !!state && state.editVersion !== editVersion;
  const bound = state?.result?.test_scope === 'saved' && !!state.result.storage_id && typeof state.result.storage_revision === 'number';
  const priorRevision = bound && (state!.result!.storage_id !== storage?.id || state!.result!.storage_revision !== storage?.revision);
  const association = priorRevision || edited ? 'previous' : state?.result?.test_scope === 'draft' ? 'draft' : bound ? 'current' : 'unknown';
  const terminal = state?.phase === 'ok' || state?.phase === 'failed';
  const outcome = state?.phase === 'ok' ? '通过' : '失败';
  const title = running ? '连接测试进行中' : state?.phase === 'invalid' ? '配置需要完善，尚未发送请求' : state?.phase === 'unknown' ? '连接测试结果待核对' : priorRevision ? `此前配置测试${outcome}，此结果不代表当前配置` : edited ? `此前配置测试${outcome}，当前表单尚未验证` : association === 'draft' ? `草稿连接测试${outcome}` : association === 'unknown' ? `连接测试${outcome}，配置归属待核对` : `连接测试${outcome}`;
  const scopeLabel = running || !terminal ? state?.scope : bound ? `测试对象：已保存配置 · 修订 ${state!.result!.storage_revision}` : state?.result?.test_scope === 'draft' ? '测试对象：未保存草稿；此结果不代表已保存配置已通过验证。' : '服务端未返回测试配置归属；请以当前配置健康状态为准，重新测试可确认。';
  const alertType = running ? 'info' : state?.phase === 'invalid' || state?.phase === 'unknown' || terminal && association !== 'current' ? 'warning' : state?.phase === 'ok' ? 'success' : 'error';
  return <div className="backup-storage-test">
    <Button disabled={disabled || running} loading={running} onClick={() => void test()}>测试连接</Button>
    {state && <div className="backup-storage-test-feedback" aria-live="polite" aria-atomic="true">
      <Alert showIcon type={alertType} message={title} description={<>
        <p>{state.message}</p><p className="backup-storage-test-context">{scopeLabel}</p>
        {edited && <p className="backup-row-note">表单已修改；本次测试使用提交时的配置，此结果不代表当前填写内容。</p>}
        {priorRevision && <p className="backup-row-note">已保存配置已变化；上方健康状态对应当前修订，下方保留此前测试诊断。</p>}
        <div className="backup-storage-test-metrics"><span>{running?'客户端已等待':'客户端耗时'}：{seconds(state.elapsedMS)}</span>{state.result && <><span>服务端阶段：{phaseNames[state.result.stage]||state.result.stage||'未提供'}</span><span>服务端耗时：{typeof state.result.duration_ms==='number'?seconds(state.result.duration_ms):'未提供'}</span>{!!state.result.http_status&&<span>上游 HTTP：{state.result.http_status}</span>}</>}{state.code&&<Tag>{state.code}</Tag>}</div>
        {state.result?.request_id&&<p className="backup-row-note">诊断编号：<code>{state.result.request_id}</code></p>}
        {!!state.result?.cleanup_error_code&&<p className="backup-storage-test-cleanup">测试对象清理未完成：{state.result.cleanup_message||'请检查删除权限并核对残留对象。'} <code>{state.result.cleanup_error_code}</code></p>}
        {running&&<p className="backup-row-note">具体服务端阶段将在请求结束后返回；页面不会自动重试。</p>}
      </>} />
      {healthWarning&&<p className="backup-storage-test-refresh">{healthWarning}</p>}
    </div>}
  </div>;
}
