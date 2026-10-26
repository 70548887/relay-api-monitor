import type { Protocol } from './types';

export const reasoningOptions=(protocol:Protocol|''|undefined,inherit=false)=>[
 {value:'',label:inherit?'继承分组':'上游默认'},
 ...(inherit?[{value:'default',label:'上游默认（覆盖分组）'}]:[]),
 ...(protocol==='anthropic'?['low','medium','high','max']:['none','minimal','low','medium','high','xhigh']).map(value=>({value,label:value})),
];
export const thinkingOptions=(inherit=false)=>[
 {value:'',label:inherit?'继承分组':'上游默认'},
 ...(inherit?[{value:'default',label:'上游默认（覆盖分组）'}]:[]),
 {value:'adaptive',label:'adaptive · 自适应'},{value:'enabled',label:'enabled · 指定预算'},
];
/** A protocol change never carries a setting unsupported by the new protocol. */
export function compatibleReasoning(protocol:Protocol,values:{reasoning_effort?:string;thinking_mode?:string;thinking_budget?:number}){
 const allowed=reasoningOptions(protocol,true).map(option=>option.value);
 return {reasoning_effort:allowed.includes(values.reasoning_effort||'')?values.reasoning_effort||'':'',thinking_mode:protocol==='anthropic'?values.thinking_mode||'':'',thinking_budget:protocol==='anthropic'&&values.thinking_mode==='enabled'?values.thinking_budget||0:0};
}
