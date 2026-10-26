import { useEffect, useRef, useState } from 'react';
import { Button, Empty, Modal, Segmented, Space } from 'antd';
import { DownloadOutlined, ExpandOutlined } from '@ant-design/icons';
import { downloadText } from '../lib';
export const previewSandbox='allow-scripts';
export function SafeFrame({document,title}:{document:string;title:string}){return <iframe className="preview-frame" sandbox={previewSandbox} referrerPolicy="no-referrer" srcDoc={document} title={title} allow=""/>}
export function ScaledFrame({document,title}:{document:string;title:string}){
 const holder=useRef<HTMLDivElement>(null);const [scale,setScale]=useState(1);
 useEffect(()=>{const element=holder.current;if(!element)return;const fit=()=>{const rect=element.getBoundingClientRect();if(rect.width&&rect.height)setScale(Math.min(rect.width/1440,rect.height/900))};fit();const observer=typeof ResizeObserver==='undefined'?null:new ResizeObserver(fit);observer?.observe(element);window.addEventListener('resize',fit);return()=>{observer?.disconnect();window.removeEventListener('resize',fit)}},[]);
 return <div className="preview-stage" ref={holder}><div className="preview-scaled" style={{transform:`translate(-50%, -50%) scale(${scale})`}}><SafeFrame document={document} title={title}/></div></div>;
}
export function Preview({document,code,kind='html',title='作品预览',active=true}:{document?:string;code?:string;kind?:string;title?:string;active?:boolean}){
 const [mode,setMode]=useState('预览');const [large,setLarge]=useState(false);
 const content=()=>mode==='源码'?<pre className="source-code">{code||'暂无源码'}</pre>:document&&active?<ScaledFrame document={document} title={title}/>:<Empty description="暂无预览"/>;
 return <div className="preview"><div className="preview-toolbar"><Segmented size="small" options={['预览','源码']} value={mode} onChange={value=>setMode(String(value))}/><Space><Button size="small" icon={<ExpandOutlined aria-hidden="true"/>} onClick={()=>setLarge(true)} aria-label="放大预览"/><Button size="small" icon={<DownloadOutlined aria-hidden="true"/>} disabled={!code} onClick={()=>downloadText(code||'',`relay-artifact.${kind==='svg'?'svg':'html'}`)}>下载</Button></Space></div>{!large&&<div className="preview-content">{content()}</div>}<Modal title={title} open={large} onCancel={()=>setLarge(false)} footer={null} width="92vw" destroyOnClose><div className="large-preview">{content()}</div></Modal></div>
}
