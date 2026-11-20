import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, App, Button, Input, Modal, Select, Space, Table, Tag } from 'antd';
import { PlayCircleOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons';
import { api, post, put } from '../api';
import { PageHeading, QueryState, StateTag } from '../components/Common';
import { FingerprintResult, VerdictTag } from '../components/FingerprintResult';
import { bankDate, levelMeta, percent, ROUND_SIZE } from '../fingerprint';
import { duration, nameOf, time } from '../lib';
import type { AdminFingerprintModel, AdminFingerprintOverview, FingerprintBankRefresh, FingerprintModelSettings, FingerprintRound } from '../types';
import '../fingerprint.css';

const overviewKey = ['admin-fingerprint'];
const runningStates = ['queued', 'claimed', 'dispatching', 'running'];
const running = (row: AdminFingerprintModel) => !!row.job && runningStates.includes(row.job.state);

/** The expected-model select holds the mode and, for a hand-picked reference model, its ID. */
const picked = 'model:';
const choiceOf = (settings: FingerprintModelSettings) => settings.mode === 'manual' ? picked + settings.model : settings.mode;
const fromChoice = (choice: string): Pick<FingerprintModelSettings, 'mode' | 'model'> => choice.startsWith(picked)
  ? { mode: 'manual', model: choice.slice(picked.length) }
  : { mode: choice === 'unlisted' ? 'unlisted' : 'auto', model: '' };

const questionOptions = Array.from({ length: ROUND_SIZE }, (_, index) => ({ value: ROUND_SIZE - index, label: `${ROUND_SIZE - index} 题` }));

function RoundSummary({ round }: { round: FingerprintRound }) {
  const analysis = round.analysis, closest = analysis.candidates[0];
  return <div className="fp-admin-round">
    <VerdictTag verdict={analysis.verdict}/>
    {closest && <span>最接近 {closest.display_name}</span>}
    {analysis.level && <small>{levelMeta[analysis.level].label}{analysis.confidence != null && ` · ${percent(analysis.confidence)}`}</small>}
  </div>;
}

function Latest({ row }: { row: AdminFingerprintModel }) {
  const { latest, job } = row;
  // The newest job has no round while it runs, and none at all when it failed or was cancelled.
  const open = job && job.id !== latest?.job_id ? job : null;
  if (!latest && !open) return <span className="muted">尚未检测</span>;
  return <div className="fp-admin-latest">
    {open && <div><StateTag state={open.state}/>{open.error_message && <span className="error-text">{open.error_message}</span>}</div>}
    {latest && <>
      <RoundSummary round={latest}/>
      <div className="muted">{open && '上一轮 · '}{time(latest.created_at)} · 有效回答 {latest.analysis.valid_answers} / {latest.questions}</div>
    </>}
  </div>;
}

function RoundDetail({ round }: { round: FingerprintRound }) {
  return <div className="fp-admin-detail">
    <FingerprintResult analysis={round.analysis} submitted={round.questions} footnote="结果表示数字分布最接近哪个模型，不是身份证明。"/>
    {round.errors.length > 0 && <Alert type="warning" showIcon message="有题目没有拿到回答"
      description={<ul>{round.errors.map(item => <li key={item.question}>第 {item.question} 题：{item.message}（{item.code}）</li>)}</ul>}/>}
  </div>;
}

function History({ row, onClose }: { row: AdminFingerprintModel; onClose: () => void }) {
  const rounds = useQuery({
    queryKey: ['admin-fingerprint-results', row.id],
    queryFn: ({ signal }) => api<FingerprintRound[]>(`/admin/fingerprint/models/${row.id}/results`, { signal }),
  });
  return <Modal title={`检测历史 · ${nameOf(row)}`} open onCancel={onClose} footer={null} width={1000}>
    <QueryState loading={rounds.isLoading} error={rounds.error}>
      <Table<FingerprintRound> rowKey="id" size="small" dataSource={rounds.data} scroll={{ x: 760 }}
        pagination={{ pageSize: 10, hideOnSinglePage: true }} locale={{ emptyText: '还没有检测记录' }}
        expandable={{ expandedRowRender: round => <RoundDetail round={round}/> }}
        columns={[
          { title: '时间', dataIndex: 'created_at', render: time },
          { title: '结论', render: (_: unknown, round) => <RoundSummary round={round}/> },
          { title: '有效回答', render: (_: unknown, round) => `${round.analysis.valid_answers} / ${round.questions}` },
          { title: '返回模型', dataIndex: 'returned_model', render: (value: string) => value || '—' },
          { title: '耗时', dataIndex: 'duration_ms', render: duration },
          { title: 'Tokens', render: (_: unknown, round) => round.usage.total_tokens ?? '—' },
        ]}/>
    </QueryState>
  </Modal>;
}

export function FingerprintAdminPage() {
  const client = useQueryClient();
  const { message } = App.useApp();
  const [search, setSearch] = useState('');
  const [history, setHistory] = useState<AdminFingerprintModel>();
  const overview = useQuery({
    queryKey: overviewKey,
    queryFn: ({ signal }) => api<AdminFingerprintOverview>('/admin/fingerprint', { signal }),
    // Follow running detections, and the first download of the bank, without a manual refresh.
    refetchInterval: query => query.state.data?.bank.ready && !query.state.data.models.some(running) ? false : 5000,
  });
  // Returned from onSettled, so a mutation stays pending until the table shows its outcome.
  const reload = () => client.invalidateQueries({ queryKey: overviewKey });
  const fail = (error: Error) => void message.error(error.message);
  const save = useMutation({
    mutationFn: ({ id, settings }: { id: string; settings: FingerprintModelSettings }) => put<FingerprintModelSettings>(`/admin/fingerprint/models/${id}`, settings),
    onSuccess: () => void message.success('已保存'),
    onError: fail,
    onSettled: reload,
  });
  const detect = useMutation({
    mutationFn: (id: string) => post('/admin/jobs', { model_id: id, kind: 'fingerprint' }),
    onSuccess: () => void message.success('已加入队列，由 Worker 执行'),
    onError: fail,
    onSettled: reload,
  });
  const refresh = useMutation({
    mutationFn: () => post<FingerprintBankRefresh>('/admin/fingerprint/bank/refresh'),
    onSuccess: result => void message.success(result.changed ? `参考库已更新到 ${bankDate(result.bank.built_at)} 版` : '参考库已是最新'),
    onError: fail,
    onSettled: reload,
  });

  const data = overview.data;
  const bank = data?.bank;
  const local = bank?.source === 'local';
  const references = data?.references || [];
  const referenceOptions = references.map(item => ({ value: picked + item.id, label: item.display_name }));
  const referenceName = (id: string) => references.find(item => item.id === id)?.display_name || id;
  const keyword = search.trim().toLowerCase();
  const rows = (data?.models || []).filter(row => !keyword || [nameOf(row), row.upstream_id, row.platform_name, row.channel_name].some(text => text.toLowerCase().includes(keyword)));

  const saving = (row: AdminFingerprintModel) => save.isPending && save.variables.id === row.id;
  const settingsOf = (row: AdminFingerprintModel) => saving(row) ? save.variables!.settings : row.settings;
  const expectedOptions = (settings: FingerprintModelSettings) => [
    { value: 'auto', label: '自动匹配' },
    { value: 'unlisted', label: '未收录（只看最接近的模型）' },
    {
      label: '指定参考模型',
      // A hand-picked model stays selectable by name after a newer bank dropped it.
      options: settings.mode === 'manual' && !references.some(item => item.id === settings.model)
        ? [{ value: picked + settings.model, label: settings.model }, ...referenceOptions]
        : referenceOptions,
    },
  ];
  const expectedNote = (row: AdminFingerprintModel) => {
    if (!bank?.ready || row.settings.mode === 'unlisted') return '';
    if (row.expected) return row.settings.mode === 'auto' ? `匹配到 ${referenceName(row.expected)}` : '';
    return row.settings.mode === 'auto' ? '参考库里没有对应的模型，只给出最接近的模型' : '参考库里已没有这个模型，只给出最接近的模型';
  };
  const blocked = (row: AdminFingerprintModel) => !bank?.ready ? '参考库暂不可用'
    : !row.runnable ? '模型在上游已不存在，或所在分组已停用'
      : running(row) ? '检测正在进行' : '';

  return <>
    <PageHeading eyebrow="FINGERPRINT / 指纹匹对" title="指纹匹对"
      description="让模型回答 1 到 3 道写数字的题，把数字分布和参考库比对，看声明的模型和实际返回的是否对得上。"
      extra={<Button icon={<ReloadOutlined aria-hidden="true"/>} onClick={() => void overview.refetch()}>刷新</Button>}/>
    <QueryState loading={overview.isLoading} error={overview.error}>{data && bank && <>
      <div className={`panel fp-admin-bank${bank.ready ? '' : ' unavailable'}`}>
        <div>
          <strong>{bank.ready ? `参考库 ${bankDate(bank.built_at)} 版` : '参考库暂不可用，暂时不能检测'}</strong>
          <span className="muted">{bank.ready
            ? `收录 ${bank.models} 个模型 · ${local ? '来自本地目录' : '来自 lm-detector'} · 上次更新 ${time(bank.updated_at)}`
            : bank.error || '服务器正在准备参考库，页面会自动重试'}</span>
        </div>
        <Button icon={<SyncOutlined aria-hidden="true"/>} loading={refresh.isPending} disabled={local}
          title={local ? '参考库来自本地目录，不能在线更新' : undefined} onClick={() => refresh.mutate()}>更新参考库</Button>
      </div>
      <div className="panel">
        <div className="section-toolbar">
          <Input allowClear aria-label="搜索模型" placeholder="搜索模型、平台或分组" style={{ maxWidth: 280 }} value={search} onChange={event => setSearch(event.target.value)}/>
          <span className="muted">3 个有效回答给出完整结论和置信度，2 个只供参考，1 个仅作测试。期望模型和题数改动后立即保存。</span>
        </div>
        <Table<AdminFingerprintModel> rowKey="id" size="middle" dataSource={rows} scroll={{ x: 1100 }}
          pagination={{ pageSize: 20, hideOnSinglePage: true }} locale={{ emptyText: data.models.length ? '没有匹配的模型' : '还没有模型，请先在“模型配置”里添加' }}
          columns={[
            {
              title: '模型', width: 230, render: (_: unknown, row) => <div>
                <Space size={6}><strong>{nameOf(row)}</strong>{!row.runnable && <Tag>不可检测</Tag>}</Space>
                <div className="muted">{row.platform_name} / {row.channel_name}</div>
              </div>,
            },
            {
              title: '期望模型', width: 250, render: (_: unknown, row) => {
                const settings = settingsOf(row), note = saving(row) ? '' : expectedNote(row);
                return <div className="fp-admin-expected">
                  <Select showSearch optionFilterProp="label" aria-label={`${nameOf(row)} 的期望模型`} style={{ width: 220 }}
                    value={choiceOf(settings)} options={expectedOptions(settings)} disabled={save.isPending} loading={saving(row)}
                    onChange={choice => save.mutate({ id: row.id, settings: { ...row.settings, ...fromChoice(choice) } })}/>
                  {note && <small className="muted">{note}</small>}
                </div>;
              },
            },
            {
              title: '题数', width: 100, render: (_: unknown, row) => <Select aria-label={`${nameOf(row)} 的题数`} style={{ width: 84 }}
                value={settingsOf(row).questions} options={questionOptions} disabled={save.isPending}
                onChange={questions => save.mutate({ id: row.id, settings: { ...row.settings, questions } })}/>,
            },
            { title: '最近检测', render: (_: unknown, row) => <Latest row={row}/> },
            {
              title: '操作', width: 160, render: (_: unknown, row) => {
                const reason = blocked(row);
                return <Space>
                  <Button size="small" icon={<PlayCircleOutlined aria-hidden="true"/>} disabled={!!reason} title={reason || undefined}
                    loading={detect.isPending && detect.variables === row.id} onClick={() => detect.mutate(row.id)}>检测</Button>
                  <Button size="small" onClick={() => setHistory(row)}>历史</Button>
                </Space>;
              },
            },
          ]}/>
      </div>
    </>}</QueryState>
    {history && <History row={history} onClose={() => setHistory(undefined)}/>}
  </>;
}
