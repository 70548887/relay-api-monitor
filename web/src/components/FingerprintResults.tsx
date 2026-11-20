import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Button, Empty, Input, Modal, Space, Switch, Table, Tag } from 'antd';
import { DownOutlined, HistoryOutlined, InfoCircleOutlined, SearchOutlined } from '@ant-design/icons';
import { api } from '../api';
import { agoText, bankDate, levelMeta, percent, SUMMARY_ROUNDS, verdictMeta, verdictShort } from '../fingerprint';
import { nameOf, shortTime, time } from '../lib';
import type { FingerprintPublicModel, FingerprintPublicResults, FingerprintRound, FingerprintVerdict } from '../types';
import { QueryState } from './Common';
import { FingerprintCandidates, VerdictTag } from './FingerprintResult';

/** What the list says about a model: the verdict drawn from its newest rounds, or pending while it has none. */
type Status = FingerprintVerdict | 'pending';
type Counts = Record<Status, number>;
type Platform = { id: string; name: string; channels: { id: string; name: string }[] };

const statusOf = (model: FingerprintPublicModel): Status => model.summary?.verdict ?? 'pending';
const statusLabels: [Status, string][] = [['match', '一致'], ['family', '同家族'], ['mismatch', '不一致'], ['unlisted', '未收录'], ['undetermined', '无法判定'], ['pending', '待检测']];

function countOf(models: FingerprintPublicModel[]) {
  const counts: Counts = { match: 0, family: 0, mismatch: 0, unlisted: 0, undetermined: 0, pending: 0 };
  for (const model of models) counts[statusOf(model)]++;
  return counts;
}
const describe = (counts: Counts) => statusLabels.filter(([key]) => counts[key] > 0).map(([key, label]) => `${counts[key]} ${label}`).join(' · ');

/** A model is in doubt when its fingerprint is closest to another model than the declared one. */
const inDoubt = (model: FingerprintPublicModel) => model.summary?.verdict === 'family' || model.summary?.verdict === 'mismatch';

const protocolLabel = (protocol: string) => ({ chat: 'Chat Completions', responses: 'Responses', anthropic: 'Anthropic' } as Record<string, string>)[protocol] || protocol;

/** Why a round without a ranking could not be compared. */
function whyUndetermined(round: FingerprintRound) {
  if (!round.analysis.answers.length) return '这一轮的请求都没有成功，没有回答可以比对。';
  if (round.analysis.reason === 'not_model_like') return '这一轮的数字不像是模型直接写出来的（例如按顺序排列），无法比对。';
  return '这一轮没有一个回答的数字够数，无法比对。';
}

/** The platforms and their channels in the order the list names them. */
function platformsOf(models: FingerprintPublicModel[]) {
  const platforms: Platform[] = [];
  for (const model of models) {
    let platform = platforms.find(item => item.id === model.platform_id);
    if (!platform) platforms.push(platform = { id: model.platform_id, name: model.platform_name, channels: [] });
    if (!platform.channels.some(item => item.id === model.channel_id)) platform.channels.push({ id: model.channel_id, name: model.channel_name });
  }
  return platforms;
}

function toggled(set: Set<string>, id: string) {
  const next = new Set(set);
  if (!next.delete(id)) next.add(id);
  return next;
}

function ModelName({ model }: { model: FingerprintPublicModel }) {
  return <div className="monitor-model-name">
    <strong title={nameOf(model)}>{nameOf(model)}</strong>
    {model.display_name && model.display_name !== model.upstream_id && <code>{model.upstream_id}</code>}
    <span>{protocolLabel(model.protocol)}</span>
  </div>;
}

/** The verdict of a round and, for a round that is not a full one, how much it rests on. */
function RoundVerdict({ round }: { round: FingerprintRound }) {
  const { verdict, level, valid_answers } = round.analysis;
  return <div className="fp-conclusion">
    <VerdictTag verdict={verdict}/>
    {(level === 'reference' || level === 'test') && <small>{levelMeta[level].label} · {valid_answers} 个有效回答</small>}
  </div>;
}

function Conclusion({ model }: { model: FingerprintPublicModel }) {
  return model.round ? <RoundVerdict round={model.round}/> : <div className="fp-conclusion"><Tag className="fp-verdict">待检测</Tag></div>;
}

function Closest({ round }: { round: FingerprintRound | null }) {
  const closest = round?.analysis.candidates[0];
  return closest
    ? <div className="fp-match"><strong title={closest.display_name}>{closest.display_name}</strong><small>{closest.family_name}</small></div>
    : <div className="fp-match none"><strong>—</strong></div>;
}

function Confidence({ round }: { round: FingerprintRound | null }) {
  const confidence = round?.analysis.confidence;
  if (confidence == null) return <span className="muted">—</span>;
  return <div className={`fp-confidence ${round!.analysis.verdict}`}>
    <strong>{percent(confidence)}</strong>
    <span className="bar" aria-hidden="true"><i style={{ width: `${confidence * 100}%` }}/></span>
  </div>;
}

/** One bar for each of the newest rounds, the oldest on the left, and how many of them reached the conclusion. */
function Rounds({ model, note = true }: { model: FingerprintPublicModel; note?: boolean }) {
  const summary = model.summary;
  const verdicts = summary ? [...summary.verdicts].reverse() : [];
  const label = summary ? `最近 ${verdicts.length} 轮，从旧到新：${verdicts.map(verdict => verdictMeta[verdict].label).join('、')}` : '尚未检测';
  return <div className="fp-rounds">
    <span className="dots" role="img" aria-label={label} title={label}>{Array.from({ length: SUMMARY_ROUNDS }, (_, index) => <i key={index} className={verdicts[index] || 'none'}/>)}</span>
    {note && summary && <small>{summary.agree} / {verdicts.length} {verdictShort(summary.verdict)}</small>}
  </div>;
}

function Checked({ model, now }: { model: FingerprintPublicModel; now: number }) {
  const at = model.checked_at;
  return <div className="monitor-last-probe">
    <span title={at ? time(at) : undefined}>{at ? shortTime(at) : '尚未检测'}</span>
    <small className="monitor-row-reason">{at ? agoText(at, now) : '等待后台发起'}</small>
  </div>;
}

/** What one round found: its ranking, its answers and the facts around it. */
function RoundDetail({ round, protocol }: { round: FingerprintRound; protocol: string }) {
  const analysis = round.analysis;
  const level = analysis.level ? levelMeta[analysis.level] : null;
  return <div className="fp-detail">
    <div>
      <h4>{analysis.candidates.length ? `候选排名（前 ${analysis.candidates.length}）` : '候选排名'}</h4>
      {analysis.candidates.length
        ? <FingerprintCandidates analysis={analysis}/>
        : <p className="fp-detail-empty">{whyUndetermined(round)}</p>}
    </div>
    <div>
      <h4>本轮 {round.questions} 道题</h4>
      <ul className="fp-samples">
        {analysis.answers.map((answer, index) => <li className="fp-sample" key={`answer-${index}`}>
          <div><strong>回答 {index + 1}</strong><small>写出 {answer.numbers} 个整数 · 至少需要 {answer.minimum} 个</small></div>
          <Tag color={answer.valid ? 'success' : undefined}>{answer.valid ? '有效' : '无效'}</Tag>
        </li>)}
        {round.errors.map(failure => <li className="fp-sample" key={`failure-${failure.question}`}>
          <div><strong>第 {failure.question} 题</strong><small>{failure.message} · {failure.code}</small></div>
          <Tag color="error">请求失败</Tag>
        </li>)}
      </ul>
    </div>
    <div>
      <h4>检测信息</h4>
      <dl className="fp-facts">
        <div><dt>检测时间</dt><dd>{time(round.created_at)}</dd></div>
        <div><dt>结果等级</dt><dd>{level ? `${level.label}（${level.note}）` : '无法判定'}</dd></div>
        <div><dt>声明的模型</dt><dd>{analysis.expected ? analysis.expected.display_name : '参考库未收录'}</dd></div>
        {analysis.expected && analysis.expected_rank > 0 && <div><dt>声明模型的排名</dt><dd>第 {analysis.expected_rank} 位</dd></div>}
        <div><dt>上游返回的模型名</dt><dd>{round.returned_model || '—'}</dd></div>
        <div><dt>请求协议</dt><dd>{protocolLabel(protocol)}</dd></div>
        <div><dt>参考库版本</dt><dd>{bankDate(analysis.bank_built_at)}</dd></div>
      </dl>
    </div>
  </div>;
}

function History({ model, onClose }: { model: FingerprintPublicModel; onClose: () => void }) {
  const rounds = useQuery({
    queryKey: ['fingerprint-history', model.id],
    queryFn: ({ signal }) => api<FingerprintRound[]>(`/public/fingerprint/models/${encodeURIComponent(model.id)}/results`, { signal }),
  });
  const analysis = model.round?.analysis;
  return <Modal title={`指纹历史 · ${nameOf(model)}`} open onCancel={onClose} footer={null} width={980} destroyOnClose>
    <div className="monitor-history-current">
      <span>{model.platform_name} / {model.channel_name}</span><code>{model.upstream_id}</code>
      {model.summary && <><VerdictTag verdict={model.summary.verdict}/><Tag>最近 {model.summary.verdicts.length} 轮中 {model.summary.agree} 轮的结论</Tag></>}
    </div>
    <div className="monitor-history-summary">
      <div><span>声明的模型</span><strong>{analysis?.expected ? analysis.expected.display_name : '参考库未收录'}</strong></div>
      <div><span>指纹最接近</span><strong>{analysis?.candidates[0]?.display_name || '—'}</strong></div>
      <div><span>置信度</span><strong>{analysis?.confidence != null ? percent(analysis.confidence) : '—'}</strong></div>
    </div>
    <p className="monitor-history-note">每轮最多发 3 次请求 · 每个模型保留最近 30 轮 · 北京时间 · 置信度是参考库范围内的概率，不是身份证明</p>
    <QueryState loading={rounds.isLoading} error={rounds.error}>
      <div className="monitor-history-list"><Table<FingerprintRound> rowKey="id" size="small" dataSource={rounds.data} scroll={{ x: 760 }}
        pagination={{ pageSize: 10, showSizeChanger: false, hideOnSinglePage: true }} locale={{ emptyText: '还没有检测记录' }}
        expandable={{ expandedRowRender: round => <RoundDetail round={round} protocol={model.protocol}/> }}
        columns={[
          { title: '检测时间', dataIndex: 'created_at', width: 170, render: time },
          { title: '结论', key: 'verdict', width: 170, render: (_: unknown, round) => <RoundVerdict round={round}/> },
          { title: '指纹最接近', key: 'closest', render: (_: unknown, round) => round.analysis.candidates[0]?.display_name || '—' },
          { title: '置信度', key: 'confidence', width: 100, render: (_: unknown, round) => round.analysis.confidence != null ? percent(round.analysis.confidence) : '—' },
          { title: '有效回答', key: 'valid', width: 100, render: (_: unknown, round) => `${round.analysis.valid_answers} / ${round.questions}` },
          { title: '参考库版本', key: 'bank', width: 120, render: (_: unknown, round) => bankDate(round.analysis.bank_built_at) },
        ]}/></div>
    </QueryState>
  </Modal>;
}

/** The conclusions of the detections the site runs on its public models. onSelf leads to the self check. */
export function FingerprintResults({ onSelf }: { onSelf: () => void }) {
  const [search, setSearch] = useState('');
  const [doubtOnly, setDoubtOnly] = useState(false);
  const [platformId, setPlatformId] = useState<string>();
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [historyId, setHistoryId] = useState<string>();
  const query = useQuery({
    queryKey: ['fingerprint-results'],
    queryFn: ({ signal }) => api<FingerprintPublicResults>('/public/fingerprint/results', { signal }),
    refetchInterval: 60000,
  });
  const all = query.data?.models;
  const platforms = useMemo(() => platformsOf(all || []), [all]);
  const keyword = search.trim().toLowerCase();
  const filtering = !!keyword || doubtOnly;
  const matched = useMemo(() => (all || []).filter(model => (!doubtOnly || inDoubt(model))
    && (!keyword || [nameOf(model), model.upstream_id, model.platform_name, model.channel_name].some(text => text.toLowerCase().includes(keyword)))), [all, keyword, doubtOnly]);

  if (!all?.length) {
    return <QueryState loading={query.isLoading} error={query.error}>
      <div className="panel empty-panel"><Empty description="还没有平台检测结果"><Button type="primary" onClick={onSelf}>去自助检测</Button></Empty></div>
    </QueryState>;
  }

  // Relative times are counted from when the list was last read, which is at most a minute ago.
  const now = query.dataUpdatedAt;
  const counts = countOf(all);
  const eligible = filtering ? platforms.filter(platform => matched.some(model => model.platform_id === platform.id)) : platforms;
  const current = eligible.find(platform => platform.id === platformId) || eligible[0];
  const visible = matched.filter(model => model.platform_id === current?.id);
  const channels = (current?.channels || []).filter(channel => visible.some(model => model.channel_id === channel.id));
  const platformModels = all.filter(model => model.platform_id === current?.id);
  const platformCounts = countOf(platformModels);
  const allCollapsed = channels.length > 0 && channels.every(channel => collapsed.has(channel.id));
  const history = all.find(model => model.id === historyId);
  const toggleAll = () => setCollapsed(previous => {
    const next = new Set(previous);
    for (const channel of channels) {
      if (allCollapsed) next.delete(channel.id); else next.add(channel.id);
    }
    return next;
  });
  const actions = (model: FingerprintPublicModel, text: boolean) => {
    if (!model.round) return null;
    const open = expanded.has(model.id), name = nameOf(model);
    return <Space size={0}>
      <Button size="small" type="text" aria-expanded={open} aria-label={`${open ? '收起' : '展开'} ${name} 的检测详情`} title={text ? undefined : '详情'}
        icon={<DownOutlined aria-hidden="true" rotate={open ? 180 : 0}/>} onClick={() => setExpanded(previous => toggled(previous, model.id))}>{text && '详情'}</Button>
      <Button size="small" type="text" aria-label={`查看 ${name} 的指纹历史`} title={text ? undefined : '历史'}
        icon={<HistoryOutlined aria-hidden="true"/>} onClick={() => setHistoryId(model.id)}>{text && '历史'}</Button>
    </Space>;
  };
  const columns = [
    { title: '声明的模型', key: 'name', width: 236, render: (_: unknown, model: FingerprintPublicModel) => <ModelName model={model}/> },
    { title: '指纹最接近', key: 'closest', width: 168, render: (_: unknown, model: FingerprintPublicModel) => <Closest round={model.round}/> },
    { title: '置信度', key: 'confidence', width: 118, render: (_: unknown, model: FingerprintPublicModel) => <Confidence round={model.round}/> },
    { title: '结论', key: 'verdict', width: 150, render: (_: unknown, model: FingerprintPublicModel) => <Conclusion model={model}/> },
    { title: `最近 ${SUMMARY_ROUNDS} 轮`, key: 'rounds', width: 176, render: (_: unknown, model: FingerprintPublicModel) => <Rounds model={model}/> },
    { title: '上次检测', key: 'checked', width: 118, render: (_: unknown, model: FingerprintPublicModel) => <Checked model={model} now={now}/> },
    { title: '', key: 'actions', width: 96, render: (_: unknown, model: FingerprintPublicModel) => actions(model, false) },
  ];
  const cards = [
    { label: '检测范围', value: `${platforms.length} 个平台`, caption: `${platforms.reduce((sum, platform) => sum + platform.channels.length, 0)} 个分组 · ${all.length} 个公开模型`, tone: 'range' },
    { label: '一致', value: counts.match, caption: '指纹与声明的模型对得上', tone: 'ok' },
    { label: '同家族不同型号', value: counts.family, caption: '同一家族，型号不同', tone: 'slow' },
    { label: '不一致', value: counts.mismatch, caption: '最接近其他家族的模型', tone: 'failed' },
    { label: '未收录 / 无法判定', value: counts.unlisted + counts.undetermined, caption: `${counts.unlisted} 未收录 · ${counts.undetermined} 无法判定`, tone: 'idle' },
    { label: '待检测', value: counts.pending, caption: '还没有检测记录', tone: 'neutral' },
  ];

  return <>
    <section className="monitor-summary" aria-label="指纹总览">{cards.map(card => <div className={`monitor-summary-card ${card.tone}`} key={card.label}>
      <span className="label">{card.label}</span><strong className="value">{card.value}</strong><span className="caption">{card.caption}</span>
    </div>)}</section>
    <div className="fp-disclaimer"><InfoCircleOutlined aria-hidden="true"/><span><strong>怎么看这个结果：</strong>指纹匹对是在参考库范围内做统计比对，表示“数字分布最接近哪个模型”。它不是身份证明，也会受模型版本更新、平台附加提示词等因素影响，请结合多轮结果判断，不要把单次结论当作唯一依据。</span></div>
    <section className="monitor-workspace panel" aria-label="平台指纹结果">
      <div className="monitor-toolbar">
        <h2>平台指纹结果 <Tag>{platforms.length} 个平台</Tag></h2>
        <div className="monitor-toolbar-controls">
          <Input value={search} allowClear onChange={event => setSearch(event.target.value)} prefix={<SearchOutlined aria-hidden="true"/>} placeholder="搜索平台 / 分组 / 模型" aria-label="搜索平台、分组或模型"/>
          <Space><Switch size="small" checked={doubtOnly} onChange={setDoubtOnly} aria-label="仅看存疑"/><span>仅看存疑</span></Space>
          <Button onClick={toggleAll} disabled={!channels.length}>{allCollapsed ? '展开全部' : '折叠全部'}</Button>
        </div>
      </div>
      <div className="monitor-layout">
        <div className="monitor-platforms" role="group" aria-label="平台导航">
          <div className="monitor-platforms-label"><span>平台</span><span>分组</span></div>
          {platforms.map(platform => {
            const matches = matched.filter(model => model.platform_id === platform.id).length;
            return <button type="button" className={`monitor-platform-button ${platform.id === current?.id ? 'active' : ''}`} key={platform.id} aria-label={platform.name}
              aria-pressed={platform.id === current?.id} disabled={filtering && !matches} onClick={() => setPlatformId(platform.id)}>
              <span className="platform-title"><strong>{platform.name}</strong><span className="platform-group-count" title="公开分组数量">{platform.channels.length}</span></span>
              <span className="platform-status-summary">{describe(countOf(all.filter(model => model.platform_id === platform.id)))}</span>
              {filtering && <small>{matches} 个匹配模型</small>}
            </button>;
          })}
          <p className="monitor-platforms-note">结论按各模型分别给出<br/>只展示已公开的模型</p>
        </div>
        <div className="monitor-results">
          {current ? <>
            <div className="monitor-results-heading">
              <div><h2>{current.name}</h2><p>{current.channels.length} 个分组 / {platformModels.length} 个模型{filtering ? ` · ${visible.length} 个匹配` : ''}</p></div>
              <Space wrap className="monitor-status-counts">
                {platformCounts.match > 0 && <Tag color="success">{platformCounts.match} 一致</Tag>}
                {platformCounts.family > 0 && <Tag color="warning">{platformCounts.family} 同家族不同型号</Tag>}
                {platformCounts.mismatch > 0 && <Tag color="error">{platformCounts.mismatch} 不一致</Tag>}
                {platformCounts.unlisted > 0 && <Tag>{platformCounts.unlisted} 未收录</Tag>}
                {platformCounts.undetermined > 0 && <Tag>{platformCounts.undetermined} 无法判定</Tag>}
                {platformCounts.pending > 0 && <Tag>{platformCounts.pending} 待检测</Tag>}
              </Space>
            </div>
            <div className="monitor-channel-list">{channels.map(channel => {
              const models = visible.filter(model => model.channel_id === channel.id);
              const open = !collapsed.has(channel.id);
              return <section className="monitor-channel" key={channel.id} aria-label={`分组 ${channel.name}`}>
                <button type="button" className="monitor-channel-toggle" aria-expanded={open} aria-controls={`fingerprint-channel-${channel.id}`} onClick={() => setCollapsed(previous => toggled(previous, channel.id))}>
                  <span className="monitor-channel-title"><DownOutlined aria-hidden="true" rotate={open ? 0 : -90}/><strong>{channel.name}</strong><small>{models.length} 个模型</small></span>
                  <span className="monitor-channel-counts">{describe(countOf(models))}</span>
                </button>
                {open && <div id={`fingerprint-channel-${channel.id}`}>
                  <div className="monitor-model-table fp-table"><Table<FingerprintPublicModel> rowKey="id" columns={columns} dataSource={models} pagination={false} size="small" scroll={{ x: 1062 }} tableLayout="fixed"
                    expandable={{ expandedRowKeys: models.filter(model => expanded.has(model.id)).map(model => model.id), showExpandColumn: false, rowExpandable: model => !!model.round,
                      expandedRowRender: model => model.round && <RoundDetail round={model.round} protocol={model.protocol}/> }}/></div>
                  <div className="monitor-mobile-models">{models.map(model => <article className="monitor-model-card" key={model.id}>
                    <div className="monitor-mobile-heading"><ModelName model={model}/><Conclusion model={model}/></div>
                    <div className="monitor-mobile-metrics">
                      <div><span>指纹最接近</span><strong>{model.round?.analysis.candidates[0]?.display_name || '—'}</strong></div>
                      <div><span>置信度</span><strong>{model.round?.analysis.confidence != null ? percent(model.round.analysis.confidence) : '—'}</strong></div>
                      <div><span>最近 {SUMMARY_ROUNDS} 轮</span><Rounds model={model} note={false}/></div>
                    </div>
                    <div className="monitor-mobile-bottom"><Checked model={model} now={now}/>{actions(model, true)}</div>
                    {model.round && expanded.has(model.id) && <RoundDetail round={model.round} protocol={model.protocol}/>}
                  </article>)}</div>
                </div>}
              </section>;
            })}</div>
          </> : <div className="monitor-no-matches"><Empty description="没有符合条件的模型"/><Button onClick={() => { setSearch(''); setDoubtOnly(false); }}>清除筛选</Button></div>}
          <div className="monitor-legend fp-legend">
            <span><i className="match"/>一致</span><span><i className="family"/>同家族不同型号</span><span><i className="mismatch"/>不一致</span><span><i className="unlisted"/>未收录 / 无法判定</span>
            <small>每格 1 轮检测，从旧到新 · 每轮最多 3 次请求</small>
            <span className="monitor-visible-count">当前展示 {visible.length} / {all.length} 个模型</span>
          </div>
        </div>
      </div>
    </section>
    <div className="monitor-footer">
      <p>置信度是参考库收录模型范围内的概率，只在 3 个回答都有效时给出；声明的模型不在参考库时显示“未收录”，只给出最接近的模型。</p>
      <p>结论取每个模型最近 {SUMMARY_ROUNDS} 轮中多数轮次的结果，表中展示得出该结论的那一轮。时间统一为北京时间。</p>
    </div>
    {history && <History model={history} onClose={() => setHistoryId(undefined)}/>}
  </>;
}
