export const CN_PROMPT = '创建一个HTML，内容是SVG绘一个鹈鹕骑自行车的2D动画';
export const EN_PROMPT = 'Create code for an SVG of a pelican riding a bicycle as nicely as you can.';
export const time = (value?:string|null) => value && !value.startsWith('0001-') ? new Date(value).toLocaleString('zh-CN',{hour12:false,timeZone:'Asia/Shanghai'}) : '—';
/** Month, day, hour and minute in Shanghai time, for a time near enough not to need its year: 11/21 21:59. */
export const shortTime = (value:string) => new Date(value).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hourCycle:'h23'});
export const duration = (value?:number|null) => value == null ? '—' : value < 1000 ? `${value} ms` : `${(value/1000).toFixed(2)} s`;
export const nameOf = (model:{display_name?:string;upstream_id?:string;requested_model?:string}) => model.display_name || model.upstream_id || model.requested_model || '未命名模型';
export const stateLabel:Record<string,string> = {ok:'正常',slow:'缓慢',failed:'失败',unknown:'待观测',disabled:'已停用',queued:'排队中',claimed:'准备执行',dispatching:'正在请求',uncertain:'结果未知',skipped:'已跳过',running:'运行中',succeeded:'已完成',completed:'已完成',canceled:'已取消',cancelled:'已取消',pending:'等待中'};
export const jobKindLabel:Record<string,string> = {probe:'健康检测',artifact:'作品生成',fingerprint:'指纹检测'};
export const stateColor:Record<string,string> = {ok:'success',slow:'warning',failed:'error',unknown:'default',disabled:'default',queued:'processing',claimed:'processing',dispatching:'processing',uncertain:'warning',skipped:'default',running:'processing',succeeded:'success',completed:'success',canceled:'default',cancelled:'default'};
export function downloadText(text:string,filename:string) {const url=URL.createObjectURL(new Blob([text],{type:'text/plain;charset=utf-8'}));const link=document.createElement('a');link.href=url;link.download=filename;link.click();setTimeout(()=>URL.revokeObjectURL(url),0)}
