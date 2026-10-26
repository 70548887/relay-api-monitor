import { Form, InputNumber, Select } from 'antd';
import type { FormInstance } from 'antd';
import type { Protocol } from '../types';
import { compatibleReasoning, reasoningOptions, thinkingOptions } from '../reasoning';

export function ReasoningFields({form,protocol,inherit=false}:{form:FormInstance;protocol:Protocol;inherit?:boolean}){
 const mode=Form.useWatch('thinking_mode',form);
 return <div className="form-two reasoning-fields"><Form.Item name="reasoning_effort" label="推理强度" extra={`${inherit?'留空继承；上游默认会覆盖分组设置。':'留空时省略该参数，由上游决定。'}不同模型支持的档位不同，选择后按原值发送，不自动降级。`}><Select options={reasoningOptions(protocol,inherit)}/></Form.Item>{protocol==='anthropic'&&<><Form.Item name="thinking_mode" label="Thinking 模式"><Select options={thinkingOptions(inherit)} onChange={value=>{if(value!=='enabled')form.setFieldsValue({thinking_budget:0})}}/></Form.Item><Form.Item name="thinking_budget" label="Thinking 预算 Tokens" dependencies={['max_tokens','thinking_mode']} rules={[({getFieldValue})=>({validator:(_,value)=>getFieldValue('thinking_mode')!=='enabled'||(Number(value)>=1024&&(!getFieldValue('max_tokens')||Number(value)<Number(getFieldValue('max_tokens'))))?Promise.resolve():Promise.reject(new Error('预算至少为 1024，且小于最大输出 Tokens'))})]}><InputNumber disabled={mode!=='enabled'} min={mode==='enabled'?1024:0} style={{width:'100%'}}/></Form.Item></>}</div>;
}
export function changeReasoningProtocol(form:FormInstance,protocol:Protocol){form.setFieldsValue(compatibleReasoning(protocol,form.getFieldsValue()))}
