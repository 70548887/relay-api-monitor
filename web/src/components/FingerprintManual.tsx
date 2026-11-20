import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Alert, App, Button, Input, Popconfirm, Select, Skeleton, Space, Tag } from 'antd';
import { CopyOutlined, InfoCircleOutlined, PlayCircleOutlined, ReloadOutlined } from '@ant-design/icons';
import { api, post } from '../api';
import { modelGroups, ROUND_SIZE } from '../fingerprint';
import type { FingerprintAnalysis, FingerprintAnalyzeRequest, FingerprintChallengeSet, FingerprintModel } from '../types';
import { FingerprintResult } from './FingerprintResult';

/** cards holds, for each scored answer in order, the index of the question card it was pasted into. */
type Outcome = { analysis: FingerprintAnalysis; cards: number[] };

const blankAnswers = () => Array.from({ length: ROUND_SIZE }, () => '');

/**
 * Scores answers that the visitor collected by hand. Nothing is sent to a model and nothing is stored.
 * head opens the settings panel; a hidden panel keeps what was pasted into it.
 */
export function FingerprintManual({ models, ready, head, hidden }: { models: FingerprintModel[]; ready: boolean; head: ReactNode; hidden: boolean }) {
  const { message } = App.useApp();
  const [answers, setAnswers] = useState(blankAnswers);
  const [claimed, setClaimed] = useState<string>();
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const resultRef = useRef<HTMLDivElement>(null);

  const challenges = useQuery({
    queryKey: ['fingerprint-challenges'],
    queryFn: ({ signal }) => api<FingerprintChallengeSet>(`/public/fingerprint/challenges?count=${ROUND_SIZE}`, { signal }),
    // Questions are random, so a background refetch would replace the ones the visitor is already answering.
    staleTime: Infinity, refetchOnMount: false, refetchOnReconnect: false,
  });
  const compare = useMutation({
    mutationFn: async (round: { body: FingerprintAnalyzeRequest; cards: number[] }): Promise<Outcome> =>
      ({ analysis: await post<FingerprintAnalysis>('/public/fingerprint/analyze', round.body), cards: round.cards }),
    onSuccess: setOutcome,
  });

  useEffect(() => {
    if (outcome) resultRef.current?.scrollIntoView?.({ behavior: 'smooth', block: 'nearest' });
  }, [outcome]);

  const modelOptions = useMemo(() => modelGroups(models), [models]);

  const questions = challenges.data?.challenges || [];
  const pasted = answers.map((text, card) => ({ text: text.trim(), card })).filter(item => item.text && questions[item.card]);

  const discardOutcome = () => { setOutcome(null); compare.reset(); };
  const edit = (card: number, text: string) => { setAnswers(current => current.map((value, index) => index === card ? text : value)); discardOutcome(); };
  const clear = () => { setAnswers(blankAnswers()); discardOutcome(); };
  const renew = async () => {
    const next = await challenges.refetch();
    if (next.isSuccess) clear();
    else void message.error('换题失败，请稍后重试');
  };
  const submit = () => compare.mutate({
    body: { answers: pasted.map(item => ({ text: item.text, expected_count: questions[item.card].expected_count })), expected_model: claimed || '' },
    cards: pasted.map(item => item.card),
  });
  const copy = async (prompt: string) => {
    try {
      await navigator.clipboard.writeText(prompt);
      void message.success('题目已复制');
    } catch {
      void message.error('复制失败，请手动选中题目复制');
    }
  };

  const answerTag = (card: number) => {
    const position = outcome ? outcome.cards.indexOf(card) : -1;
    const report = position >= 0 ? outcome?.analysis.answers[position] : undefined;
    if (report?.valid) return <Tag color="success">已识别 {report.numbers} 个 · 有效</Tag>;
    if (report) return <Tag color="warning">{report.numbers < report.minimum ? `只识别到 ${report.numbers} 个 · 至少需要 ${report.minimum} 个` : `已识别 ${report.numbers} 个 · 未被采用`}</Tag>;
    if (outcome) return <Tag>未提交</Tag>;
    return answers[card].trim() ? <Tag color="processing">已粘贴</Tag> : <Tag>待粘贴</Tag>;
  };

  const renewButton = <Button block icon={<ReloadOutlined aria-hidden="true"/>} loading={challenges.isFetching} disabled={compare.isPending} onClick={pasted.length ? undefined : () => void renew()}>换一组题目</Button>;

  return <div className="fp-self" hidden={hidden}>
    <aside className="panel fp-self-config">
      {head}
      <ol className="fp-steps">
        <li>复制题目，原样发给要检测的模型。每题新开一个对话，不要加系统提示词。</li>
        <li>把模型的完整回答粘贴到对应的输入框。</li>
        <li>点「开始比对」。3 个回答最准确，2 个可参考，1 个仅作测试。</li>
      </ol>
      <div className="fp-field">
        <label htmlFor="fp-claimed-model">声明的模型（可选）</label>
        <Select id="fp-claimed-model" showSearch allowClear optionFilterProp="label" placeholder="不选则只给出最接近的模型" notFoundContent="参考库里没有这个模型"
          value={claimed} options={modelOptions} disabled={compare.isPending} onChange={value => { setClaimed(value); discardOutcome(); }}/>
        <small>选了才会给出「一致 / 不一致」。只能选参考库收录的模型。</small>
      </div>
      {pasted.length
        ? <Popconfirm title="换一组题目？" description="已粘贴的回答会被清空。" okText="确认换题" cancelText="取消" onConfirm={() => void renew()}>{renewButton}</Popconfirm>
        : renewButton}
      <div className="fp-note"><InfoCircleOutlined aria-hidden="true"/><span>手动方式下本站不向任何模型接口发请求，无需登录。粘贴的内容只用于这一次比对，不保存。</span></div>
    </aside>
    <div className="fp-self-main">
      {!ready && <Alert type="warning" showIcon message="参考库暂不可用" description="可以先取题、收集回答，参考库就绪后才能比对。页面会自动重试。"/>}
      {!questions.length && (challenges.isError
        ? <Alert type="error" showIcon message="题目读取失败" description={challenges.error.message} action={<Button size="small" onClick={() => void challenges.refetch()}>重试</Button>}/>
        : <div className="panel"><Skeleton active paragraph={{ rows: 6 }}/></div>)}
      {questions.map((question, card) => <section className="fp-sample-card" key={question.id} aria-label={`题目 ${card + 1}`}>
        <header><div><strong>题目 {card + 1}</strong><small>要求 {question.expected_count} 个整数</small></div>{answerTag(card)}</header>
        <div className="fp-prompt"><span>{question.prompt}</span><Button size="small" icon={<CopyOutlined aria-hidden="true"/>} onClick={() => void copy(question.prompt)}>复制题目</Button></div>
        <Input.TextArea rows={answers[card] ? 4 : 3} value={answers[card]} disabled={compare.isPending} aria-label={`题目 ${card + 1} 的回答`}
          placeholder="把模型的完整回答粘贴到这里" onChange={event => edit(card, event.target.value)}/>
      </section>)}
      {questions.length > 0 && <div className="fp-self-actions">
        <span><strong>已粘贴 {pasted.length} / {questions.length} 个回答</strong>3 个回答都有效才给出置信度；只有 1–2 个时只给出排名。</span>
        <Space>
          <Button disabled={compare.isPending || answers.every(text => !text)} onClick={clear}>清空</Button>
          <Button type="primary" icon={<PlayCircleOutlined aria-hidden="true"/>} loading={compare.isPending} disabled={!ready || !pasted.length} onClick={submit}>开始比对</Button>
        </Space>
      </div>}
      {compare.isError && <Alert type="error" showIcon message="比对失败" description={compare.error.message}/>}
      <div ref={resultRef}>
        {outcome && <FingerprintResult analysis={outcome.analysis} submitted={outcome.cards.length}
          footnote="结果表示数字分布最接近哪个模型，不是身份证明。粘贴的内容只用于这一次比对，不保存，也不会公开。"/>}
      </div>
    </div>
  </div>;
}
