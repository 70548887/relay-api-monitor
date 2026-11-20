import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, App, Button, Form, Input, InputNumber, Radio } from 'antd';
import { BranchesOutlined, CheckCircleOutlined, CheckOutlined, ClockCircleOutlined, ControlOutlined, DatabaseOutlined, InfoCircleOutlined, KeyOutlined, SafetyCertificateOutlined, ScanOutlined, TeamOutlined } from '@ant-design/icons';
import { api, captureSessionGuard, post, put, setSession } from '../api';
import { adminLoginPath } from '../adminRoutes';
import { PageHeading, QueryState } from '../components/Common';
import type { Settings } from '../types';
import '../settings.css';

type SettingsTab = 'schedule' | 'requests' | 'data' | 'visitors' | 'fingerprint' | 'security';
type NumericSetting = Exclude<keyof Settings, 'artifact_history_scope' | 'fingerprint_bank_dir'>;
type SettingField = { name: NumericSetting; label: string; unit: string; help: string; min: number; max: number };
type SettingSection = { title: string; description: string; fields?: SettingField[]; history?: boolean; bank?: boolean };
type SettingPanel = { key: SettingsTab; label: string; icon: ReactNode; description: string; note: string; sections: SettingSection[] };
type PasswordValues = { old_password: string; new_password: string; confirm: string };

const panels: SettingPanel[] = [
  {
    key: 'schedule', label: '任务调度', icon: <ClockCircleOutlined aria-hidden="true"/>,
    description: '配置并发上限与执行间隔，平衡观测效率和资源占用。',
    note: '执行间隔越短，任务触发越频繁。请结合平台额度与实际负载调整。',
    sections: [
      { title: '并发控制', description: '管理同时执行的任务数', fields: [
        { name: 'probe_concurrency', label: '健康检测并发', unit: '个', help: '全局同时执行的检测任务', min: 1, max: 32 },
        { name: 'artifact_concurrency', label: '作品生成并发', unit: '个', help: '全局同时执行的创作任务', min: 1, max: 16 },
        { name: 'channel_concurrency', label: '单分组并发', unit: '个', help: '每个分组的同时执行上限', min: 1, max: 16 },
      ] },
      { title: '执行频率', description: '设置自动任务的间隔', fields: [
        { name: 'probe_interval_seconds', label: '默认检测间隔', unit: '秒', help: '', min: 30, max: 86400 },
        { name: 'artifact_interval_seconds', label: '默认创作间隔', unit: '秒', help: '', min: 60, max: 604800 },
      ] },
    ],
  },
  {
    key: 'requests', label: '请求策略', icon: <BranchesOutlined aria-hidden="true"/>,
    description: '为不同请求设置等待时限，统一识别响应缓慢的模型。',
    note: '超时用于结束等待，缓慢阈值用于判定响应速度。两者可以独立设置。',
    sections: [
      { title: '超时设置', description: '各类请求的最大等待时间', fields: [
        { name: 'probe_timeout_seconds', label: '检测超时', unit: '秒', help: '健康检测请求的等待上限', min: 5, max: 600 },
        { name: 'artifact_timeout_seconds', label: '创作超时', unit: '秒', help: '作品生成请求的等待上限', min: 5, max: 600 },
        { name: 'discovery_timeout_seconds', label: '模型发现超时', unit: '秒', help: '读取模型列表的等待上限', min: 1, max: 120 },
      ] },
      { title: '响应判定', description: '设置缓慢响应的分界值', fields: [
        { name: 'slow_threshold_ms', label: '缓慢阈值', unit: '毫秒', help: '响应时间超过此值时标记为缓慢', min: 100, max: 600000 },
      ] },
    ],
  },
  {
    key: 'data', label: '数据与历史', icon: <DatabaseOutlined aria-hidden="true"/>,
    description: '管理记录保留周期，以及创作历史的聚合范围。',
    note: '数据保留周期与创作历史聚合范围分别管理，按实际查阅需要配置。',
    sections: [
      { title: '数据保留', description: '按记录类型设置保留周期', fields: [
        { name: 'retention_days', label: '检测数据保留', unit: '天', help: '健康检测记录的保留时长', min: 1, max: 365 },
        { name: 'online_test_retention_days', label: '私有测试记录保留', unit: '天', help: '私有在线测试记录的保留时长', min: 1, max: 365 },
      ] },
      { title: '创作历史', description: '选择最近 8 次记录的范围', history: true },
    ],
  },
  {
    key: 'visitors', label: '访客额度', icon: <TeamOutlined aria-hidden="true"/>,
    description: '管理公开测试入口的并发与访问频率。',
    note: '访客限制作用于公开测试入口，按每个 IP 分别计算请求频率。',
    sections: [
      { title: '并发上限', description: '控制公开入口的并行请求', fields: [
        { name: 'visitor_concurrency', label: '访客总并发', unit: '个', help: '全部访客共享的并发上限', min: 1, max: 64 },
        { name: 'visitor_ip_concurrency', label: '每 IP 并发', unit: '个', help: '单个 IP 的同时请求上限', min: 1, max: 8 },
      ] },
      { title: '频率限制', description: '按 IP 限制每分钟调用量', fields: [
        { name: 'visitor_models_per_minute', label: '读取模型', unit: '次 / 分钟', help: '每个 IP 每分钟读取模型的上限', min: 1, max: 120 },
        { name: 'visitor_generations_per_minute', label: '生成作品', unit: '次 / 分钟', help: '每个 IP 每分钟生成作品的上限', min: 1, max: 120 },
      ] },
    ],
  },
  {
    key: 'fingerprint', label: '指纹匹对', icon: <ScanOutlined aria-hidden="true"/>,
    description: '管理指纹检测的并发与定时间隔、自助检测的额度，以及参考库的来源。',
    note: '定时检测默认关闭，在后台「指纹匹对」里按模型打开。修改间隔后，已打开定时的模型从保存时起按新间隔重新计时。',
    sections: [
      { title: '平台检测', description: '后台发起与定时的检测', fields: [
        { name: 'fingerprint_concurrency', label: '指纹检测并发', unit: '个', help: '全局同时执行的指纹检测任务', min: 1, max: 8 },
        { name: 'fingerprint_interval_seconds', label: '定时检测间隔', unit: '秒', help: '', min: 3600, max: 2592000 },
      ] },
      { title: '自助检测', description: '限制访客与会员的用量', fields: [
        { name: 'fingerprint_manual_per_minute', label: '手动粘贴比对', unit: '次 / 分钟', help: '每个 IP 每分钟比对的上限', min: 1, max: 120 },
        { name: 'fingerprint_api_per_hour', label: '通过 API 检测', unit: '轮 / 小时', help: '每个会员每小时检测的上限', min: 1, max: 60 },
      ] },
      { title: '参考库', description: '指纹数据的来源', bank: true },
    ],
  },
  {
    key: 'security', label: '账号安全', icon: <SafetyCertificateOutlined aria-hidden="true"/>,
    description: '修改当前管理员账号的登录密码。',
    note: '密码更新后，请使用新密码重新登录。', sections: [],
  },
];
const securityIndex = panels.length - 1;
const intervalActions: Partial<Record<NumericSetting, string>> = { probe_interval_seconds: '检测', artifact_interval_seconds: '生成', fingerprint_interval_seconds: '检测' };
// The server decides what is absolute on its own system; this only refuses a path that is absolute on none.
const absolutePath = /^(\/|[A-Za-z]:[\\/]|\\\\)/;

function checkBankDir(_: unknown, value?: string) {
  const dir = (value || '').trim();
  if (dir && !absolutePath.test(dir)) return Promise.reject(new Error('请填写绝对路径，例如 /data/fingerprint-bank 或 D:\\fingerprint-bank'));
  if (new TextEncoder().encode(dir).length > 1024) return Promise.reject(new Error('路径最多为 1024 字节'));
  return Promise.resolve();
}

function normalizeSettings(settings: Settings): Settings {
  return { ...settings, artifact_history_scope: settings.artifact_history_scope || 'group' };
}

function changedSettings(saved: Settings | undefined, draft: Partial<Settings>) {
  return saved ? (Object.keys(saved) as (keyof Settings)[]).filter(key => saved[key] !== draft[key]).length : 0;
}

function intervalHelp(value: number | undefined, action: string) {
  if (!value || value < 1) return '请输入有效的执行间隔';
  const amount = value % 3600 === 0 ? `${value / 3600} 小时` : value % 60 === 0 ? `${value / 60} 分钟` : `${value.toLocaleString('zh-CN')} 秒`;
  return `每 ${amount}${action}一次`;
}

function PanelHeading({ panel, index }: { panel: SettingPanel; index: number }) {
  return <div className="settings-panel-heading"><div><h2>{panel.label}</h2><p>{panel.description}</p></div><span className="settings-panel-index" aria-hidden="true">0{index + 1} / 0{panels.length}</span></div>;
}

function PanelNote({ children }: { children: ReactNode }) {
  return <div className="settings-panel-note"><InfoCircleOutlined aria-hidden="true"/><span>{children}</span></div>;
}

export function SettingsPage() {
  const { message } = App.useApp();
  const client = useQueryClient();
  const navigate = useNavigate();
  const query = useQuery({ queryKey: ['admin-settings'], queryFn: ({ signal }) => api<Settings>('/admin/settings', { signal }) });
  const [form] = Form.useForm<Settings>();
  const [password] = Form.useForm<PasswordValues>();
  const [saved, setSaved] = useState<Settings>();
  const [draft, setDraft] = useState<Partial<Settings>>({});
  const [activeTab, setActiveTab] = useState<SettingsTab>('schedule');
  const [serverUpdated, setServerUpdated] = useState(false);
  const baseline = useRef<Settings>();
  const mounted = useRef(false);
  const tabButtons = useRef<(HTMLButtonElement | null)[]>([]);
  const changeCount = changedSettings(saved, draft);

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  useEffect(() => {
    if (!query.data) return;
    const next = normalizeSettings(query.data);
    const previous = baseline.current;
    const current: Partial<Settings> = form.getFieldsValue(true);
    const dirty = changedSettings(previous, current) > 0;
    let merged: Partial<Settings> = { ...next };
    if (previous) {
      // Keep deliberate edits, but refresh untouched fields and the reset baseline.
      for (const key of Object.keys(previous) as (keyof Settings)[]) {
        if (current[key] !== previous[key]) merged = { ...merged, [key]: current[key] };
      }
      if (changedSettings(previous, next)) setServerUpdated(dirty);
    }
    baseline.current = next;
    setSaved(next);
    setDraft(merged);
    form.setFieldsValue(merged);
  }, [query.data, form]);

  useEffect(() => {
    if (!changeCount) return;
    const warnBeforeLeaving = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', warnBeforeLeaving);
    return () => window.removeEventListener('beforeunload', warnBeforeLeaving);
  }, [changeCount]);

  function selectTab(tab: SettingsTab) {
    setActiveTab(tab);
  }

  function handleTabKey(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next = event.key === 'ArrowRight' ? (index + 1) % panels.length : event.key === 'ArrowLeft' ? (index + panels.length - 1) % panels.length : event.key === 'Home' ? 0 : event.key === 'End' ? panels.length - 1 : undefined;
    if (next === undefined) return;
    event.preventDefault();
    selectTab(panels[next].key);
    tabButtons.current[next]?.focus();
  }

  const save = useMutation({
    mutationFn: async (values: Settings) => {
      const isCurrentSession = captureSessionGuard();
      // A GET started before this write must not replace the confirmed save result.
      await client.cancelQueries({ queryKey: ['admin-settings'], exact: true });
      if (!isCurrentSession()) return { settings: values, isCurrentSession };
      const response = await put<Settings>('/admin/settings', values);
      return { settings: normalizeSettings(response || values), isCurrentSession };
    },
    onSuccess: ({ settings: next, isCurrentSession }) => {
      if (!isCurrentSession()) return;
      if (mounted.current) {
        baseline.current = next;
        form.setFieldsValue(next);
        setSaved(next);
        setDraft(next);
        setServerUpdated(false);
      }
      client.setQueryData(['admin-settings'], next);
      void client.invalidateQueries({ queryKey: ['admin-intelligence-status'] });
      void client.invalidateQueries({ queryKey: ['admin-fingerprint'] });
      void client.invalidateQueries({ queryKey: ['fingerprint-overview'] });
      if (mounted.current) void message.success('系统设置已保存');
    },
  });

  const changePassword = useMutation({
    mutationFn: async (values: PasswordValues) => {
      const isCurrentSession = captureSessionGuard();
      await post('/auth/password', { old_password: values.old_password, new_password: values.new_password });
      return isCurrentSession;
    },
    onSuccess: isCurrentSession => {
      if (!isCurrentSession()) return;
      if (mounted.current) password.resetFields();
      setSession(null);
      void message.success('密码已更新，请重新登录');
      navigate(adminLoginPath('/admin/settings'), { replace: true });
    },
  });

  const writePending = save.isPending || changePassword.isPending;

  function resetChanges() {
    const latest = client.getQueryData<Settings>(['admin-settings']) || baseline.current;
    if (!latest) return;
    const next = normalizeSettings(latest);
    baseline.current = next;
    form.resetFields();
    form.setFieldsValue(next);
    setSaved(next);
    setDraft(next);
    setServerUpdated(false);
    save.reset();
  }

  function revealInvalidField({ errorFields }: { errorFields: { name: (string | number)[] }[] }) {
    const first = errorFields[0]?.name[0];
    const panel = panels.find(item => item.sections.some(section => section.fields?.some(field => field.name === first) || section.history && first === 'artifact_history_scope' || section.bank && first === 'fingerprint_bank_dir'));
    if (panel) selectTab(panel.key);
    if (first !== undefined) window.requestAnimationFrame(() => form.getFieldInstance(first)?.focus?.());
  }

  function renderField(field: SettingField) {
    const action = intervalActions[field.name];
    const help = action ? intervalHelp(draft[field.name], action) : field.help;
    return <Form.Item key={field.name} name={field.name} label={field.label} extra={help} rules={[
      { required: true, message: `请输入${field.label}` },
      { type: 'integer', min: field.min, max: field.max, message: `请输入 ${field.min.toLocaleString('zh-CN')}–${field.max.toLocaleString('zh-CN')} 之间的整数` },
    ]}><InputNumber min={field.min} max={field.max} precision={0} step={1} controls={false} addonAfter={field.unit} disabled={writePending}/></Form.Item>;
  }

  return <div className="settings-page">
    <PageHeading eyebrow="PREFERENCES" title="系统设置" description="让任务、数据与访问策略，按你的节奏运行。" extra={<span className="settings-global-label"><ControlOutlined aria-hidden="true"/>全局配置</span>}/>
    <div className="settings-tablist" role="tablist" aria-label="设置分类">{panels.map((panel, index) => <button key={panel.key} ref={element => { tabButtons.current[index] = element; }} type="button" role="tab" id={`settings-tab-${panel.key}`} aria-controls={`settings-panel-${panel.key}`} aria-selected={activeTab === panel.key} tabIndex={activeTab === panel.key ? 0 : -1} onClick={() => selectTab(panel.key)} onKeyDown={event => handleTabKey(event, index)}>{panel.icon}<span>{panel.label}</span></button>)}</div>
    <div hidden={activeTab === 'security'}>
    <QueryState loading={query.isLoading} error={!query.data ? query.error : undefined}>
      {query.isError && query.data && <Alert className="settings-feedback" type="warning" showIcon message="设置刷新失败，当前修改已保留" description={query.error.message}/>}
      {serverUpdated && <Alert className="settings-feedback" type="info" showIcon message="服务器设置已更新，已保留你的修改" description="未编辑的字段已同步。撤销更改可恢复服务器最新设置。"/>}
      <Form<Settings> form={form} name="admin-settings" layout="vertical" className="settings-config-form" hidden={activeTab === 'security'} onFinish={values => { if (!writePending) save.mutate({ ...saved, ...values }); }} onFinishFailed={revealInvalidField} onValuesChange={() => { setDraft(form.getFieldsValue(true)); if (save.isError) save.reset(); }}>
        {panels.filter(panel => panel.key !== 'security').map((panel, index) => <section className="settings-panel" key={panel.key} id={`settings-panel-${panel.key}`} role="tabpanel" aria-labelledby={`settings-tab-${panel.key}`} hidden={activeTab !== panel.key}>
          <PanelHeading panel={panel} index={index}/>
          {panel.sections.map(section => <div className="settings-group" key={section.title}>
            <div className="settings-group-heading"><h3>{section.title}</h3><p>{section.description}</p></div>
            {section.fields && <div className={`settings-input-grid${section.fields.length === 3 ? ' settings-input-grid-three' : ''}`}>{section.fields.map(renderField)}</div>}
            {section.history && <Form.Item name="artifact_history_scope" className="settings-history" rules={[{ required: true, message: '请选择历史保留范围' }]}>
              <Radio.Group aria-label="历史保留范围" disabled={writePending}>
                <Radio value="group"><span className="settings-history-label">每个分组最近 8 次<span className="settings-default-badge">默认</span></span><span className="settings-history-help">按分组聚合，集中查看该分组的创作结果。</span></Radio>
                <Radio value="model"><span className="settings-history-label">每个模型最近 8 次</span><span className="settings-history-help">按模型聚合，分别保留各模型的创作结果。</span></Radio>
              </Radio.Group>
            </Form.Item>}
            {section.bank && <Form.Item name="fingerprint_bank_dir" label="参考库本地目录" rules={[{ validator: checkBankDir }]}
              extra="留空使用在线参考库（lm-detector）。填写服务器上的绝对路径后，改用该目录里的 unified_bank.json 和 shared_detector.bin；保存后在后台「指纹匹对」页查看参考库是否可用。">
              <Input allowClear autoComplete="off" spellCheck={false} placeholder="留空使用在线参考库" disabled={writePending}/>
            </Form.Item>}
          </div>)}
          <PanelNote>{panel.note}</PanelNote>
        </section>)}
        {save.isError && <Alert className="settings-feedback" type="error" showIcon message="设置保存失败" description={save.error.message}/>}
        <div className="settings-savebar"><div className={`settings-save-status${changeCount ? ' has-changes' : ''}`} role="status" aria-live="polite"><CheckCircleOutlined aria-hidden="true"/><span>{save.isPending ? '正在保存设置…' : changeCount ? `有 ${changeCount} 项更改尚未保存` : '暂无未保存的更改'}</span></div><div className="settings-save-actions"><Button onClick={resetChanges} disabled={!changeCount || writePending}>撤销更改</Button><Button type="primary" htmlType="submit" icon={<CheckOutlined aria-hidden="true"/>} loading={save.isPending} disabled={!saved || changePassword.isPending}>保存设置</Button></div></div>
      </Form>
    </QueryState>
    </div>
      <section className="settings-panel" id="settings-panel-security" role="tabpanel" aria-labelledby="settings-tab-security" hidden={activeTab !== 'security'}>
        <PanelHeading panel={panels[securityIndex]} index={securityIndex}/>
        <div className="settings-group settings-security-group"><div className="settings-group-heading"><h3>修改密码</h3><p>使用至少 12 位字符，<br/>建议组合字母、数字与符号。</p></div>
          <Form<PasswordValues> form={password} name="admin-password" layout="vertical" className="settings-password-form" disabled={writePending} onFinish={values => { if (!writePending) changePassword.mutate(values); }} onValuesChange={() => { if (changePassword.isError) changePassword.reset(); }}>
            <Form.Item name="old_password" label="当前密码" rules={[{ required: true, message: '请输入当前密码' }]}><Input.Password autoComplete="current-password" placeholder="请输入当前密码"/></Form.Item>
            <Form.Item name="new_password" label="新密码" extra="至少 12 位字符，最多 256 字节；中文等字符占多个字节。" rules={[{ required: true, message: '请输入新密码' }, { min: 12, message: '请使用至少 12 位密码' }, { validator: (_, value: string) => !value || new TextEncoder().encode(value).length <= 256 ? Promise.resolve() : Promise.reject(new Error('密码最多为 256 字节')) }]}><Input.Password autoComplete="new-password" placeholder="至少 12 位，建议组合字母、数字与符号"/></Form.Item>
            <Form.Item name="confirm" label="确认新密码" dependencies={['new_password']} rules={[{ required: true, message: '请再次输入新密码' }, ({ getFieldValue }) => ({ validator: (_, value) => !value || getFieldValue('new_password') === value ? Promise.resolve() : Promise.reject(new Error('两次密码不一致')) })]}><Input.Password autoComplete="new-password" placeholder="请再次输入新密码"/></Form.Item>
            {changePassword.isError && <Alert className="settings-password-error" type="error" showIcon message="密码更新失败" description={changePassword.error.message}/>}
            <Button type="primary" htmlType="submit" icon={<KeyOutlined aria-hidden="true"/>} loading={changePassword.isPending}>更新密码</Button>
          </Form>
        </div>
        <PanelNote>{panels[securityIndex].note}</PanelNote>
      </section>
  </div>;
}
