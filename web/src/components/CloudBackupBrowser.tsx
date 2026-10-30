import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Empty, Input, Modal, Space, Table, Tag } from 'antd';
import { CloudDownloadOutlined, ReloadOutlined } from '@ant-design/icons';
import { backupAPI } from '../backupApi';
import type { CloudCandidate } from '../backupApi';
import { captureSessionGuard } from '../api';
import { time } from '../lib';

interface Props {
  open: boolean;
  storageConfigured: boolean;
  disabled?: boolean;
  onClose: () => void;
  onConfigure: () => void;
  onImported: () => Promise<void> | void;
}

function size(bytes: number) { return bytes >= 1024 ** 3 ? `${(bytes / 1024 ** 3).toFixed(2)} GiB` : bytes >= 1024 ** 2 ? `${(bytes / 1024 ** 2).toFixed(1)} MiB` : bytes >= 1024 ? `${(bytes / 1024).toFixed(1)} KiB` : `${bytes} B`; }

export function CloudBackupBrowser({ open, storageConfigured, disabled, onClose, onConfigure, onImported }: Props) {
  const [rows, setRows] = useState<CloudCandidate[]>([]);
  const [continuation, setContinuation] = useState<string>();
  const [truncated, setTruncated] = useState(false);
  const [scanned, setScanned] = useState(false);
  const [busy, setBusy] = useState<'scan' | 'import' | ''>('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [selected, setSelected] = useState<CloudCandidate>();
  const [identity, setIdentity] = useState('');
  const [confirming, setConfirming] = useState(false);
  const [password, setPassword] = useState('');
  const [passwordError, setPasswordError] = useState('');
  const active = useRef(false);
  const generation = useRef(0);
  const scanAbort = useRef<AbortController>();

  useEffect(() => {
    generation.current++;
    if (!open) {
      scanAbort.current?.abort();
      setRows([]); setContinuation(undefined); setTruncated(false); setScanned(false);
      setSelected(undefined); setIdentity(''); setPassword(''); setPasswordError('');
      setConfirming(false); setError(''); setNotice(''); setBusy(''); active.current = false;
    }
  }, [open]);
  useEffect(() => () => { generation.current++; scanAbort.current?.abort(); }, []);

  async function scan(more: boolean) {
    if (active.current || !storageConfigured || disabled || more && !continuation) return;
    active.current = true; setBusy('scan'); setError(''); setNotice('');
    const current = generation.current; const validSession = captureSessionGuard();
    const controller = new AbortController(); scanAbort.current = controller;
    try {
      const result = await backupAPI.cloudScan(more ? continuation : undefined, controller.signal);
      if (generation.current !== current || !validSession()) return;
      setRows(existing => {
        const byID = new Map((more ? existing : []).map(row => [row.id, row]));
        result.items.forEach(row => byID.set(row.id, row));
        return [...byID.values()];
      });
      setContinuation(result.continuation_token); setTruncated(result.truncated); setScanned(true);
      if (!more) { setSelected(undefined); setIdentity(''); }
    } catch (failure) {
      if (!controller.signal.aborted && generation.current === current && validSession()) setError((failure as Error).message);
    } finally {
      if (generation.current === current) { setBusy(''); active.current = false; }
    }
  }

  function select(row: CloudCandidate) { if (active.current || row.imported) return; setSelected(row); setIdentity(''); setPassword(''); setPasswordError(''); setError(''); setNotice(''); }
  function requestImport() { if (!selected || active.current || disabled) return; setPassword(''); setPasswordError(''); setConfirming(true); }
  async function importSelected() {
    if (!selected || !password || active.current || disabled) return;
    active.current = true; setBusy('import'); setPasswordError(''); setError('');
    const current = generation.current; const validSession = captureSessionGuard();
    let sent = false;
    try {
      await backupAPI.reauth(password); setPassword('');
      if (generation.current !== current || !validSession()) return;
      sent = true;
      const result = await backupAPI.cloudImport(selected.id, identity);
      if (generation.current !== current || !validSession()) return;
      setRows(existing => existing.map(row => row.id === selected.id ? { ...row, imported: true, status: 'imported', message: '已校验并导入受保护记录' } : row));
      setSelected(undefined); setConfirming(false);
      setNotice(`已导入 ${result.file_name || '云端备份'}。记录已保护；需要恢复时请在备份记录中另行执行预检。`);
      await onImported();
    } catch (failure) {
      if (generation.current !== current || !validSession()) return;
      if (sent) { setConfirming(false); setError((failure as Error).message); }
      else setPasswordError((failure as Error).message);
    } finally {
      if (generation.current === current) {
        if (sent) setIdentity('');
        setBusy(''); active.current = false;
      }
    }
  }

  return <>
    <Modal title="云端备份目录" open={open} width={980} onCancel={() => { if (busy !== 'import') onClose(); }} footer={<Button onClick={onClose} disabled={busy === 'import'}>关闭</Button>} maskClosable={busy !== 'import'} closable={busy !== 'import'} destroyOnClose>
      <div className="backup-cloud-browser">
        <Alert showIcon type="info" message="扫描已配置存储中的备份" description="云端对象先列为待核对。导入时使用对应恢复私钥完整校验，只新增受保护的备份记录；业务数据恢复需要另行执行。未知对象不会自动删除。" />
        {!storageConfigured && <Alert type="warning" showIcon message="请先保存 S3 存储配置" action={<Button onClick={() => { onClose(); onConfigure(); }}>配置云端存储</Button>} />}
        <div className="backup-cloud-toolbar"><Space wrap><Button type="primary" icon={<ReloadOutlined spin={busy === 'scan'} />} disabled={!storageConfigured || !!busy || disabled} loading={busy === 'scan' && !continuation} onClick={() => void scan(false)}>扫描云端备份</Button>{truncated && <Button disabled={!continuation || !!busy || disabled} loading={busy === 'scan' && !!continuation} onClick={() => void scan(true)}>继续扫描</Button>}</Space><span>已列出 {rows.length} 份{truncated ? ' · 尚有更多对象' : scanned ? ' · 本次扫描完成' : ''}</span></div>
        {error && <Alert type="error" showIcon message="云端操作未完成" description={error} />}
        {notice && <Alert type="success" showIcon message={notice} />}
        <Table<CloudCandidate> rowKey="id" dataSource={rows} pagination={false} loading={busy === 'scan'} scroll={{ x: 700, y: 320 }} locale={{ emptyText: <Empty description={scanned ? '当前前缀没有可导入的备份文件' : '点击扫描读取已配置存储中的备份'} /> }} columns={[
          { title: '文件', dataIndex: 'file_name', render: value => <span className="backup-cloud-file">{value}</span> },
          { title: '大小', dataIndex: 'size_bytes', width: 100, render: value => size(value) },
          { title: '云端时间 · 北京时间', dataIndex: 'last_modified', width: 180, render: value => time(value) },
          { title: '状态', width: 150, render: (_, row) => <><Tag color={row.imported ? 'success' : 'default'}>{row.imported ? '已导入' : '待核对'}</Tag>{row.message && <div className="backup-row-note">{row.message}</div>}</> },
          { title: '操作', width: 105, render: (_, row) => <Button size="small" icon={<CloudDownloadOutlined />} disabled={row.imported || !!busy || disabled} onClick={() => select(row)}>选择导入</Button> },
        ]} />
        {selected && <section className="backup-cloud-selection"><h3>校验并导入所选备份</h3><strong>{selected.file_name}</strong><p>仅凭文件名不能确认来源。请提供此加密备份对应的恢复私钥，私钥仅用于本次请求。</p><label htmlFor="backup-cloud-identity">云端备份恢复私钥</label><Input.Password id="backup-cloud-identity" value={identity} onChange={event => setIdentity(event.target.value)} disabled={busy === 'import'} autoComplete="new-password" placeholder="AGE-SECRET-KEY-…" /><div className="backup-form-actions"><Button onClick={() => { setSelected(undefined); setIdentity(''); }} disabled={!!busy}>取消选择</Button><Button type="primary" disabled={!!busy || disabled || !identity.trim()} onClick={requestImport}>验证密码并导入</Button></div></section>}
        {busy === 'import' && <Alert type="info" showIcon message="正在下载、校验并导入，请保留此页面" description="大备份可能需要较长时间。页面不会自动重试，也不会再次发送导入请求。" />}
      </div>
    </Modal>
    <Modal title="确认导入云端备份" open={open && confirming} onCancel={() => { if (!busy) { setConfirming(false); setPassword(''); setPasswordError(''); } }} onOk={() => void importSelected()} okText="验证并导入" cancelText="取消" confirmLoading={busy === 'import'} okButtonProps={{ disabled: !password || busy === 'scan' }} maskClosable={!busy} closable={!busy} destroyOnClose>
      <p>验证管理员密码后，下载并校验所选备份。此操作会创建受保护的备份记录。</p>{passwordError && <Alert type="error" showIcon message={passwordError} />}<label htmlFor="backup-cloud-password">管理员密码</label><Input.Password id="backup-cloud-password" value={password} onChange={event => setPassword(event.target.value)} disabled={busy === 'import'} autoComplete="current-password" />
    </Modal>
  </>;
}
