import { Tag } from 'antd';
import { InfoCircleOutlined } from '@ant-design/icons';
import { bankDate, levelMeta, percent, verdictMeta } from '../fingerprint';
import type { FingerprintAnalysis, FingerprintVerdict } from '../types';

export function VerdictTag({ verdict }: { verdict: FingerprintVerdict }) {
  const meta = verdictMeta[verdict];
  return <Tag color={meta.color} className="fp-verdict">{meta.label}</Tag>;
}

const undeterminedReason: Record<string, string> = {
  no_valid_answers: '没有一个回答的数字够数，无法比对。请让模型把题目要求的数字写完整后再试。',
  not_model_like: '这些数字不像是模型直接写出来的（例如按顺序排列，或由程序生成），无法比对。',
};

function claimText(analysis: FingerprintAnalysis) {
  const expected = analysis.expected;
  if (!expected) return '没有选择声明的模型，只给出最接近的模型';
  if (analysis.verdict === 'match') return `与声明的 ${expected.display_name} 对得上`;
  if (analysis.verdict === 'family') return `声明的是 ${expected.display_name}，最接近的是同家族的另一个型号`;
  return analysis.expected_rank > 0 ? `声明的是 ${expected.display_name}，它排在第 ${analysis.expected_rank} 位` : `声明的是 ${expected.display_name}`;
}

/** The outcome of one round. submitted is the number of answers that were sent for scoring. */
export function FingerprintResult({ analysis, submitted, footnote }: { analysis: FingerprintAnalysis; submitted: number; footnote: string }) {
  const closest = analysis.candidates[0];
  if (analysis.verdict === 'undetermined' || !analysis.level || !closest) {
    return <section className="fp-result" aria-label="比对结果">
      <div className="fp-result-head">
        <div>
          <div className="eyebrow">比对结果</div>
          <h3>无法判定</h3>
          <div className="fp-result-line"><span className="fp-result-claim">{undeterminedReason[analysis.reason] || '这一轮回答无法比对。'}</span></div>
        </div>
        <div className="fp-result-score partial"><strong>{analysis.valid_answers} / {submitted}</strong><span>有效回答</span></div>
      </div>
      <div className="fp-result-foot"><InfoCircleOutlined aria-hidden="true"/><span>{footnote}</span></div>
    </section>;
  }
  const level = levelMeta[analysis.level];
  const expected = analysis.expected;
  const tone = expected ? analysis.verdict : 'plain';
  return <section className="fp-result" aria-label="比对结果">
    <div className="fp-result-head">
      <div>
        <div className="eyebrow">指纹最接近</div>
        <h3>{closest.display_name}</h3>
        <div className="fp-result-line">
          {expected && <VerdictTag verdict={analysis.verdict}/>}
          <span className="fp-result-claim">{claimText(analysis)}{closest.family_name && ` · ${closest.family_name} 家族`}</span>
        </div>
      </div>
      {analysis.confidence != null
        ? <div className={`fp-result-score ${tone}`}><strong>{percent(analysis.confidence)}</strong><span>置信度（参考库范围内）</span></div>
        : <div className="fp-result-score partial"><strong>{level.label}</strong><span>{level.note}</span></div>}
    </div>
    <div className="fp-result-body">
      <div>
        <h4>候选排名（前 {analysis.candidates.length}）</h4>
        <ol className="fp-candidates">{analysis.candidates.map((item, index) => <li className={`fp-candidate${index === 0 ? ` top ${tone}` : ''}${item.probability == null ? ' unscored' : ''}`} key={item.model}>
          <span className="rank">{index + 1}</span>
          <span className="name"><strong>{item.display_name}{item.model === expected?.id && <em>声明的模型</em>}</strong><small>{item.family_name}</small></span>
          {item.probability != null && <>
            <span className="track" aria-hidden="true"><i style={{ width: `${Math.max(item.probability * 100, 1.5)}%` }}/></span>
            <span className="pct">{percent(item.probability)}</span>
          </>}
        </li>)}</ol>
      </div>
      <div>
        <h4>检测信息</h4>
        <dl className="fp-facts">
          <div><dt>有效回答</dt><dd>{analysis.valid_answers} / {submitted}</dd></div>
          <div><dt>结果等级</dt><dd>{level.label}（{level.note}）</dd></div>
          <div><dt>声明的模型</dt><dd>{expected ? expected.display_name : '未选择'}</dd></div>
          {expected && analysis.expected_rank > 0 && <div><dt>声明模型的排名</dt><dd>第 {analysis.expected_rank} 位</dd></div>}
          <div><dt>参考库版本</dt><dd>{bankDate(analysis.bank_built_at)}</dd></div>
        </dl>
      </div>
    </div>
    <div className="fp-result-foot"><InfoCircleOutlined aria-hidden="true"/><span>{footnote}</span></div>
  </section>;
}
