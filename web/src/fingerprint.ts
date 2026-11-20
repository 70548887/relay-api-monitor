import type { FingerprintLevel, FingerprintModel, FingerprintVerdict } from './types';

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

/** The wording of a verdict where space is short. */
export const verdictShort = (verdict: FingerprintVerdict) => verdict === 'family' ? '同家族' : verdictMeta[verdict].label;

/** A full round has three questions; fewer answers give a weaker result. */
export const ROUND_SIZE = 3;

/** The conclusion shown for a model is drawn from this many of its newest rounds. */
export const SUMMARY_ROUNDS = 3;

/** The bank reports its build time as an ISO timestamp; the page shows only the date. */
export const bankDate = (builtAt: string) => builtAt.slice(0, 10);

export const percent = (value: number) => `${(value * 100).toFixed(1)}%`;

/** The reference models as the option groups of a select, one group per family. value turns a model ID into the value of its option. */
export function modelGroups(models: FingerprintModel[], value: (id: string) => string = id => id) {
  const families = new Map<string, { label: string; options: { value: string; label: string }[] }>();
  for (const model of models) {
    const family = families.get(model.family) || { label: model.family_name || model.family, options: [] };
    family.options.push({ value: value(model.id), label: model.display_name });
    families.set(model.family, family);
  }
  return [...families.values()];
}

/** How long before now, given in milliseconds, a time was: 不到 1 分钟前, 5 分钟前, 3 小时前, 2 天前. */
export function agoText(value: string, now: number) {
  const seconds = Math.floor((now - Date.parse(value)) / 1000);
  if (seconds < 60) return '不到 1 分钟前';
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`;
  return `${Math.floor(seconds / 86400)} 天前`;
}

/** The time between scheduled detections in the largest unit that divides it: 24 小时, 2 天, 90 分钟. */
export const intervalText = (seconds: number) => seconds > 86400 && seconds % 86400 === 0 ? `${seconds / 86400} 天`
  : seconds % 3600 === 0 ? `${seconds / 3600} 小时` : `${Math.round(seconds / 60)} 分钟`;
