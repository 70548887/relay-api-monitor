import {useEffect,useRef,useState} from 'react';
import {Alert,Button,Modal,Switch,Tag} from 'antd';
import {CheckOutlined,CodeOutlined,CopyOutlined,DownloadOutlined,ExpandOutlined,FileTextOutlined,PlayCircleOutlined,LoadingOutlined} from '@ant-design/icons';
import {ScaledFrame} from './Preview';
import {duration,downloadText} from '../lib';
import {reasoningLabel,thinkingLabel,testErrorHint} from '../intelligence';
import type {TestDone,Usage} from '../types';
import '../private-tests.css';

interface Props {
 output:string;running:boolean;stopped:boolean;error?:string;
 meta?:Record<string,unknown>;usage?:Usage;done?:TestDone;
 artifact?:{code:string;kind:string;preview_document:string};
 recordID?:string;saveState?:'pending'|'saved'|'failed';readOnly?:boolean;
}
export function TestResponse({output,running,stopped,error,meta,usage,done,artifact,recordID,saveState,readOnly=false}:Props){
 const [view,setView]=useState<'response'|'code'|'preview'>('response');const [wrap,setWrap]=useState(true);const [follow,setFollow]=useState(true);const [large,setLarge]=useState(false);const [copied,setCopied]=useState(false);const [copyError,setCopyError]=useState('');const scroll=useRef<HTMLPreElement>(null);
 useEffect(()=>{if(running){setView('response');setFollow(true);setLarge(false)}else if(artifact&&!error)setView('preview')},[running,artifact?.code,artifact?.preview_document,error]);
 useEffect(()=>{if(!artifact&&view!=='response')setView('response')},[artifact,view]);
 useEffect(()=>{if(running&&follow&&view==='response'&&scroll.current)scroll.current.scrollTop=scroll.current.scrollHeight},[output,running,follow,view]);
 useEffect(()=>{if(!copied)return;const timer=setTimeout(()=>setCopied(false),1600);return()=>clearTimeout(timer)},[copied]);
 const currentUsage=done?.usage||usage;const hasResult=!!output||!!done||!!meta||!!error||stopped;const text=view==='code'?artifact?.code||'':output;const count=Array.from(text).length;const lines=text?text.split('\n').length:0;
 const status=running?'正在生成':error?'请求失败':done?'已完成':stopped?'已停止':'等待请求';
 const requested=done?.requested_model||String(meta?.requested_model||meta?.model||'—');
 const returned=done?.returned_model||String(meta?.returned_model||'—');
 const metrics=[['首段正文',duration(done?.first_text_ms??(typeof meta?.first_text_ms==='number'?meta.first_text_ms:undefined))],['请求总耗时',duration(done?.duration_ms??(typeof meta?.duration_ms==='number'?meta.duration_ms:undefined))],['总 Tokens',currentUsage?.total_tokens??'—'],['HTTP 状态',done?.http_status||String(meta?.http_status||'—')]];
 const details=[['上下文','独立请求'],['传输接收',typeof (done?.received_bytes??meta?.received_bytes)==='number'?`${Number(done?.received_bytes??meta?.received_bytes).toLocaleString()} B`:'未记录'],['正文大小',typeof (done?.text_bytes??meta?.text_bytes)==='number'?`${Number(done?.text_bytes??meta?.text_bytes).toLocaleString()} B`:'未记录'],['请求模型',requested],['返回模型',returned],['实际推理强度',reasoningLabel(done?.reasoning_effort??String(meta?.reasoning_effort||''))],['实际 Thinking',thinkingLabel(done?.thinking_mode??String(meta?.thinking_mode||''),done?.thinking_budget??Number(meta?.thinking_budget||0))],['输入 Tokens',currentUsage?.input_tokens??'未提供'],['输出 Tokens',currentUsage?.output_tokens??'未提供'],['缓存命中输入',currentUsage?.cached_input_tokens??'未提供'],['缓存写入输入',currentUsage?.cache_creation_input_tokens??'未提供'],['结束原因',done?.finish_reason||String(meta?.finish_reason||'—')]];
 const download=()=>{const source=artifact&&view!=='response'?artifact.code:output;const extension=artifact&&view!=='response'?(artifact.kind==='svg'?'svg':'html'):'txt';downloadText(source,`relay-test-${recordID||'response'}.${extension}`)};
 const copy=async()=>{try{await navigator.clipboard.writeText(artifact&&view!=='response'?artifact.code:output);setCopied(true);setCopyError('')}catch{setCopyError('复制失败，请使用下载按钮保存内容。')}};
 return <section className="response-workspace" aria-label="测试响应工作区">
  <div className="response-metrics">{metrics.map(([label,value])=><div key={label}><span>{label}</span><strong>{value}</strong></div>)}</div>
  <div className="response-card output-panel">
   <header className="response-heading"><div><span className="response-eyebrow">RESPONSE / 模型输出</span><h2>响应内容</h2><p>{running?'正在接收上游内容，完成后自动保存。':artifact?'作品已生成，可预览画面或检查原始内容。':'正文、代码与执行信息集中显示在这里。'}</p></div><div className="response-heading-status"><Tag color={running?'processing':error?'error':done?'success':'default'}>{running&&<LoadingOutlined aria-hidden="true" spin/>}{status}</Tag>{saveState&&<span className={`response-save-state ${saveState}`} role="status">{saveState==='saved'?'已保存到私有历史':saveState==='failed'?'结果未完整保存':'记录已建立 · 结果保存中'}</span>}</div></header>
   {(error||copyError)&&<div className="response-alert"><Alert showIcon type={error?'error':'warning'} message={error?'测试失败':'复制未完成'} description={<>{error||copyError}{error&&testErrorHint(String(meta?.code||''))&&<p>{testErrorHint(String(meta?.code||''))}</p>}</>}/></div>}
   <div className="response-toolbar"><div className="response-tabs" role="tablist" aria-label="响应显示方式">
    {([{value:'response',label:'完整回复',icon:<FileTextOutlined aria-hidden="true"/>},...(artifact?[{value:'code',label:'提取源码',icon:<CodeOutlined aria-hidden="true"/>},{value:'preview',label:'作品预览',icon:<PlayCircleOutlined aria-hidden="true"/>}]:[])] as const).map(item=><button key={item.value} type="button" role="tab" aria-selected={view===item.value} onClick={()=>setView(item.value as typeof view)}>{item.icon}{item.label}</button>)}
   </div><div className="response-tools"><Button type="text" size="small" icon={copied?<CheckOutlined aria-hidden="true"/>:<CopyOutlined aria-hidden="true"/>} disabled={!output&&!artifact} onClick={()=>void copy()}>{copied?'已复制':'复制'}</Button><Button type="text" size="small" icon={<DownloadOutlined aria-hidden="true"/>} disabled={!output&&!artifact} onClick={download}>下载</Button>{view==='preview'&&<Button type="text" size="small" icon={<ExpandOutlined aria-hidden="true"/>} aria-label="放大作品预览" onClick={()=>setLarge(true)}/>}</div></div>
   {view==='preview'&&artifact?<div className="response-preview" role="tabpanel" aria-label="作品预览">{!large&&<ScaledFrame document={artifact.preview_document} title="在线测试作品"/>}</div>:text?<div className="response-code-area" role="tabpanel" aria-label={view==='code'?'提取源码':'完整回复'}><div className="response-code-header"><span><CodeOutlined aria-hidden="true"/>{view==='code'?artifact?.kind?.toUpperCase():'RAW RESPONSE'}</span><label><Switch size="small" checked={wrap} onChange={setWrap} aria-label="自动换行"/> 自动换行</label></div><pre ref={scroll} tabIndex={0} className={`response-text ${wrap?'is-wrapped':''}`} onScroll={event=>{if(!running)return;const element=event.currentTarget;setFollow(element.scrollHeight-element.scrollTop-element.clientHeight<60)}}><code>{text}</code></pre></div>:<div className="response-empty"><span className="response-empty-icon">{running?<LoadingOutlined aria-hidden="true" spin/>:<CodeOutlined aria-hidden="true"/>}</span><h3>{running?'等待首段正文':'开始一次新的测试'}</h3><p>{running?'思考过程可能需要一些时间，收到正文后将在此显示。':'选择连接与模型，响应内容和生成作品会显示在这里。'}</p><span className="response-empty-hint">每次请求独立，不携带历史对话</span></div>}
   <footer className="response-content-footer"><span>{view==='preview'?'沙箱预览 · 仅当前作品加载':`${count.toLocaleString()} 字符${lines?' · '+lines.toLocaleString()+' 行':''}`}</span>{running&&view==='response'&&!readOnly&&<label><Switch size="small" checked={follow} onChange={setFollow} aria-label="跟随输出"/> 跟随输出</label>}{recordID&&<a href={`/account/tests/${encodeURIComponent(recordID)}`}>打开私有记录 ↗</a>}</footer>
   {hasResult&&<div className="response-details"><div className="response-details-heading"><span>执行详情</span><small>用量由上游返回</small></div><dl>{details.map(([label,value])=><div key={label}><dt>{label}</dt><dd aria-label={`${label} ${value}`} title={String(value)}><span className="response-a11y-label">{label} </span>{value}</dd></div>)}</dl></div>}
  </div>
  <Modal title="在线测试作品" open={large&&!!artifact} onCancel={()=>setLarge(false)} footer={null} width="92vw" destroyOnClose><div className="large-preview">{large&&artifact&&<ScaledFrame document={artifact.preview_document} title="放大作品预览"/>}</div></Modal>
 </section>;
}
