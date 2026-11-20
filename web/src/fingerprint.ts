import type { FingerprintLevel, FingerprintVerdict } from './types';

export const verdictMeta: Record<FingerprintVerdict, { label: string; color?: string }> = {
  match: { label: '一致', color: 'success' },
  family: { label: '同家族不同型号', color: 'warning' },
  mismatch: { label: '不一致', color: 'error' },
  unlisted: { label: '未收录' },
  undetermined: { label: '无法判定' },
};

export const levelMeta: Record<FingerprintLevel, { label: string; note: string }> = {
  full: { label: '完整', note: '3 个回答都有效，给出置信度' },
  reference: { label: '参考', note: '2 个回答有效，只给出排名' },
  test: { label: '测试', note: '1 个回答有效，仅作测试' },
};

/** A full round has three questions; fewer answers give a weaker result. */
export const ROUND_SIZE = 3;

/** The bank reports its build time as an ISO timestamp; the page shows only the date. */
export const bankDate = (builtAt: string) => builtAt.slice(0, 10);

export const percent = (value: number) => `${(value * 100).toFixed(1)}%`;

/** The time between scheduled detections in the largest unit that divides it: 24 小时, 2 天, 90 分钟. */
export const intervalText = (seconds: number) => seconds > 86400 && seconds % 86400 === 0 ? `${seconds / 86400} 天`
  : seconds % 3600 === 0 ? `${seconds / 3600} 小时` : `${Math.round(seconds / 60)} 分钟`;
