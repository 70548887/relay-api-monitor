import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, AutoComplete, Button, Segmented, Select, Skeleton, Space, Tag } from 'antd';
import { InfoCircleOutlined, PlayCircleOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { APIError } from '../api';
import { modelGroups, ROUND_SIZE } from '../fingerprint';
import { time } from '../lib';
import type { Connection } from '../privateTests';
import type { FingerprintCheck, FingerprintCheckRequest, FingerprintModel } from '../types';
import { refreshUserSession, userApi, useUserSession } from '../userApi';
import { FingerprintResult } from './FingerprintResult';

type Panel = { models: FingerprintModel[]; ready: boolean; head: ReactNode; hidden: boolean };

/** What the declared model is set to: matched by the model name, left out, or one model of the reference bank. */
type Claim = 'auto' | 'unlisted' | `model:${string}`;

/** A finished round with what it was run on, so that the result still explains itself after the form changes. */
type Round = { check: FingerprintCheck; model: string; mode: FingerprintCheckRequest['mode'] };

/** Where signing in returns to. */
const returnTo = encodeURIComponent('/fingerprint?tab=self&mode=api');

const seconds = (ms: number) => `${(ms / 1000).toFixed(1)} 秒`;

function hostOf(baseURL: string) {
  try {
    return new URL(baseURL).host;
  } catch {
    return baseURL;
  }
}

function usageText(usage: FingerprintCheck['usage']) {
  if (usage.input_tokens == null && usage.output_tokens == null && usage.total_tokens == null) return '接口未返回';
  return `输入 ${usage.input_tokens ?? '—'} · 输出 ${usage.output_tokens ?? '—'} · 合计 ${usage.total_tokens ?? '—'}`;
}

/**
 * Checks a model through a connection the member saved: the server asks the questions and scores the replies, and
 * stores nothing. head opens the settings panel; a hidden panel keeps its result.
 */
export function FingerprintApi(props: Panel) {
  const session = useUserSession();
  const navigate = useNavigate();
  const signedIn = useQuery({ queryKey: ['user-session'], queryFn: ({ signal }) => refreshUserSession(signal), retry: false, staleTime: 30000 });
  if (session) return <MemberCheck {...props} memberID={session.user.id}/>;

  return <div className="fp-self" hidden={props.hidden}>
    <aside className="panel fp-self-config">
      {props.head}
      <div className="fp-note"><InfoCircleOutlined aria-hidden="true"/><span>通过 API 检测时，本站用你保存的连接向模型发题，所以需要登录。不想登录可以用「手动粘贴」。</span></div>
    </aside>
    <div className="fp-self-main">
      {signedIn.isLoading ? <div className="panel"><Skeleton active paragraph={{ rows: 4 }}/></div>
        : signedIn.error ? <Alert type="error" showIcon message="用户登录状态读取失败" description={signedIn.error.message} action={<Button size="small" onClick={() => void signedIn.refetch()}>重试</Button>}/>
        : <div className="panel fp-api-empty">
          <h3>登录后才能通过 API 检测</h3>
          <p>登录后选一个保存的连接和模型，本站会同时发出题目并比对回答。结果只显示在这个页面。</p>
          <Space>
            <Button type="primary" onClick={() => navigate(`/login?next=${returnTo}`)}>登录</Button>
            <Button onClick={() => navigate(`/register?next=${returnTo}`)}>注册</Button>
          </Space>
        </div>}
    </div>
  </div>;
}

function MemberCheck({ models, ready, head, hidden, memberID }: Panel & { memberID: string }) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const [connectionID, setConnectionID] = useState<string>();
  const [model, setModel] = useState('');
  const [claim, setClaim] = useState<Claim>('auto');
  const [questions, setQuestions] = useState(ROUND_SIZE);
  const [listed, setListed] = useState<{ connection: string; models: string[] } | null>(null);
  const request = useRef<AbortController | null>(null);
  const resultRef = useRef<HTMLDivElement>(null);

  const connections = useQuery({ queryKey: ['private-connections', memberID], queryFn: ({ signal }) => userApi<Connection[]>('/connections', { signal }) });
  const usable = (connections.data || []).filter(item => !item.disabled);
  const connection = usable.find(item => item.id === connectionID) || usable[0];

  // The server refuses a connection that was edited after the list was read. Reading the list again gives the next
  // try the current revision.
  const reread = (error: Error) => {
    if (error instanceof APIError && error.code === 'connection_changed') void client.invalidateQueries({ queryKey: ['private-connections'] });
  };
  const discover = useMutation({
    mutationFn: (target: Connection) => userApi<{ models: string[] }>('/test/models', { method: 'POST', body: JSON.stringify({ connection_id: target.id, connection_revision: target.revision }) }),
    onSuccess: (data, target) => setListed({ connection: target.id, models: data.models }),
    onError: reread,
  });
  const check = useMutation({
    mutationFn: async (body: FingerprintCheckRequest): Promise<Round> => {
      request.current = new AbortController();
      const outcome = await userApi<FingerprintCheck>('/test/fingerprint', { method: 'POST', body: JSON.stringify(body), signal: request.current.signal });
      return { check: outcome, model: body.model, mode: body.mode };
    },
    onError: reread,
  });
  // Only the round that was asked for last is shown: a new one clears it, and so does a refusal.
  const round = check.data ?? null;

  useEffect(() => () => request.current?.abort(), []);
  useEffect(() => {
    if (round) resultRef.current?.scrollIntoView?.({ behavior: 'smooth', block: 'nearest' });
  }, [round]);

  const claimOptions = useMemo(() => [
    { value: 'auto', label: '按模型名自动匹配' },
    { value: 'unlisted', label: '不指定，只看最接近的模型' },
    ...modelGroups(models, id => `model:${id}`),
  ], [models]);

  const wanted = model.trim();
  const listedHere = listed && listed.connection === connection?.id ? listed.models : null;
  const cancelled = check.error?.name === 'AbortError';
  const failure = check.error && !cancelled ? check.error : null;
  const asked = check.variables?.questions ?? questions;

  const submit = () => {
    if (!connection) return;
    check.mutate({
      connection_id: connection.id, connection_revision: connection.revision, model: wanted, questions,
      mode: claim === 'auto' || claim === 'unlisted' ? claim : 'manual',
      expected_model: claim.startsWith('model:') ? claim.slice('model:'.length) : '',
    });
  };

  return <div className="fp-self" hidden={hidden}>
    <aside className="panel fp-self-config">
      {head}
      {connections.isPending ? <Skeleton active paragraph={{ rows: 5 }}/>
        : connections.isError ? <Alert type="error" showIcon message="连接读取失败" description={connections.error.message} action={<Button size="small" onClick={() => void connections.refetch()}>重试</Button>}/>
        : !connection ? <>
          <div className="fp-note"><InfoCircleOutlined aria-hidden="true"/><span>{connections.data.length ? '保存的连接都已停用。' : '还没有保存的连接。'}先在「保存的连接」里{connections.data.length ? '启用' : '添加'}一个连接，再回来检测。</span></div>
          <Button block type="primary" className="fp-api-action" onClick={() => navigate('/account/connections')}>管理保存的连接</Button>
        </> : <>
          <div className="fp-field">
            <label htmlFor="fp-api-connection">我的连接</label>
            <Select id="fp-api-connection" value={connection.id} disabled={check.isPending}
              options={connections.data.map(item => ({ value: item.id, label: `${item.name} · ${hostOf(item.base_url)}${item.disabled ? '（已停用）' : ''}`, disabled: item.disabled }))}
              onChange={value => { setConnectionID(value); discover.reset(); }}/>
            <small>使用「保存的连接」里的连接，密钥不会显示，也不会出现在结果里。</small>
          </div>
          <div className="fp-field">
            <label htmlFor="fp-api-model">模型</label>
            <div className="fp-api-model">
              <AutoComplete id="fp-api-model" value={model} disabled={check.isPending} placeholder="要检测的模型 ID" options={(listedHere || []).map(value => ({ value }))}
                filterOption={(typed, option) => String(option?.value).toLowerCase().includes(typed.toLowerCase())} onChange={setModel}/>
              <Button loading={discover.isPending} disabled={check.isPending} onClick={() => discover.mutate(connection)}>获取模型</Button>
            </div>
            {discover.isError ? <small className="error-text">模型列表获取失败：{discover.error.message}。可以直接填写模型 ID。</small>
              : listedHere ? <small>{listedHere.length ? `已获取 ${listedHere.length} 个模型，可以在输入框里选择或搜索。` : '连接没有返回模型，请直接填写模型 ID。'}</small>
              : <small>填写发给连接的模型 ID，或点「获取模型」从连接读取列表。</small>}
          </div>
          <div className="fp-field">
            <label htmlFor="fp-api-claim">声明的模型</label>
            <Select id="fp-api-claim" showSearch optionFilterProp="label" notFoundContent="参考库里没有这个模型" value={claim} options={claimOptions} disabled={check.isPending} onChange={setClaim}/>
            <small>自动匹配按模型名在参考库里找对应的型号，匹配不对可以手动选。</small>
          </div>
          <div className="fp-field">
            <span className="fp-field-label">题数</span>
            <Segmented block aria-label="题数" value={questions} disabled={check.isPending} options={[3, 2, 1].map(value => ({ value, label: `${value} 题` }))} onChange={setQuestions}/>
            <small>3 题最准确，2 题可参考，1 题仅作测试。连续请求会被拦截的模型可以减少题数。</small>
          </div>
          <Button block type="primary" icon={<PlayCircleOutlined aria-hidden="true"/>} loading={check.isPending} disabled={!ready || !wanted} onClick={submit}>{check.isPending ? '检测中' : round ? '再检测一轮' : '开始检测'}</Button>
          {check.isPending && <Button block className="fp-api-action" onClick={() => request.current?.abort()}>取消</Button>}
          <div className="fp-note"><InfoCircleOutlined aria-hidden="true"/><span>一轮会用你的连接同时发 {questions} 次请求，每次让模型写约 300 个整数，费用按你的接口计费。每个账号每小时能检测的轮数有限，开始后即计一轮。</span></div>
        </>}
    </aside>
    <div className="fp-self-main">
      {!ready && <Alert type="warning" showIcon message="参考库暂不可用" description="参考库就绪后才能检测。页面会自动重试。"/>}
      {check.isPending && <div className="fp-api-round" role="status" aria-label="正在检测">
        <div className="fp-api-samples">
          {Array.from({ length: asked }, (_, index) => <section className="fp-sample-card" key={index}>
            <header><strong>样本 {index + 1}</strong><Tag color="processing">等待回答</Tag></header>
            <p>题目已发出，正在等模型写完数字。</p>
          </section>)}
        </div>
        <p className="fp-api-wait">已同时发出 {asked} 道题。通常几十秒内完成，带思考的模型可能要几分钟；不想等可以点「取消」。</p>
      </div>}
      {failure && <Alert type="error" showIcon message="检测没有完成" description={failure.message}/>}
      {cancelled && <Alert type="info" showIcon message="已取消本轮检测" description="已经发出的请求仍可能按你的接口计费，这一轮也计入每小时的检测次数。"/>}
      {!check.isPending && !check.error && !round && <div className="panel fp-api-empty">
        <h3>还没有检测结果</h3>
        <p>{connection ? '在左侧选好连接和模型，点「开始检测」。本站会同时发出题目，等模型答完后给出比对结果。' : '添加连接后，在左侧选好连接和模型就可以开始检测。'}</p>
      </div>}
      <div ref={resultRef}>{round && <RoundOutcome round={round}/>}</div>
    </div>
  </div>;
}

/** What each question of a round came to, and what the round as a whole says. */
function RoundOutcome({ round }: { round: Round }) {
  const { check } = round;
  const analysis = check.analysis;
  const failures = new Map(check.errors.map(failure => [failure.question, failure]));
  const nothingArrived = failures.size >= check.questions && analysis.valid_answers === 0;

  return <div className="fp-api-round">
    <div className="fp-api-samples">
      {Array.from({ length: check.questions }, (_, index) => {
        const answer = analysis.answers[index];
        const failure = failures.get(index + 1);
        return <section className="fp-sample-card" key={index} aria-label={`样本 ${index + 1}`}>
          <header>
            <strong>样本 {index + 1}</strong>
            {answer?.valid ? <Tag color="success">有效</Tag> : failure ? <Tag color="error">请求失败</Tag> : <Tag color="warning">未采用</Tag>}
          </header>
          <p>要求 {check.expected_counts[index]} 个 · 识别 {answer?.numbers ?? 0} 个</p>
          <small>用时 {seconds(check.question_ms[index] ?? 0)}</small>
          {failure ? <small className="error-text">{failure.message} · {failure.code}</small>
            : answer && !answer.valid && <small>{answer.numbers < answer.minimum ? `至少需要 ${answer.minimum} 个整数` : '这个回答没有被采用'}</small>}
        </section>;
      })}
    </div>
    {nothingArrived
      ? <Alert type="error" showIcon message={`本轮 ${check.questions} 道题的请求都没有成功，无法比对`} description="失败原因见上面的每个样本。请检查连接和模型 ID 后再试；这一轮已计入每小时的检测次数。"/>
      : <FingerprintResult analysis={analysis} submitted={check.questions}
        unclaimed={round.mode === 'auto'
          ? { line: `参考库里没有和「${round.model}」对应的模型，只给出最接近的模型`, fact: '未匹配到' }
          : { line: '没有指定声明的模型，只给出最接近的模型', fact: '未指定' }}
        facts={<>
          {analysis.expected && <div><dt>匹配方式</dt><dd>{round.mode === 'auto' ? '按模型名自动匹配' : '手动指定'}</dd></div>}
          <div><dt>检测的模型</dt><dd>{round.model}</dd></div>
          <div><dt>接口返回的模型名</dt><dd>{check.returned_model || '未返回'}</dd></div>
          <div><dt>本轮用时</dt><dd>{seconds(check.duration_ms)}</dd></div>
          <div><dt>Token 用量</dt><dd>{usageText(check.usage)}</dd></div>
          <div><dt>检测时间</dt><dd>{time(check.created_at)}</dd></div>
        </>}
        footnote="结果表示数字分布最接近哪个模型，不是身份证明。结果只显示在这个页面，不保存，也不会公开。"/>}
  </div>;
}
