import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';
import type { Artifact } from '../types';
import { previewSandbox } from './Preview';

export const MAX_THUMBNAIL_BYTES=500*1024;
const cache=new Map<string,string>();
let activeSlots=0;
const waiting=new Set<()=>void>();
function useCaptureSlot(enabled:boolean){
 const [granted,setGranted]=useState(false);
 useEffect(()=>{if(!enabled){setGranted(false);return}let acquired=false;const acquire=()=>{if(!acquired&&activeSlots<2){acquired=true;activeSlots++;waiting.delete(acquire);setGranted(true)}};waiting.add(acquire);acquire();return()=>{waiting.delete(acquire);if(acquired){activeSlots--;for(const next of [...waiting])next()}setGranted(false)}},[enabled]);
 return granted;
}
export function acceptsThumbnail(event:Pick<MessageEvent,'source'|'origin'|'data'>,source:Window|null,token:string){
 const data=event.data as Record<string,unknown>|null;
 return !!source&&event.source===source&&event.origin==='null'&&!!data&&data.type==='relay-thumbnail'&&data.token===token&&typeof data.png==='string'&&data.png.length<=MAX_THUMBNAIL_BYTES&&/^data:image\/png;base64,iVBORw0KGgo[A-Za-z0-9+/=]+$/.test(data.png);
}
export function thumbnailDocument(document:string,token:string){
 // This bridge runs only inside an opaque origin. Only its bounded PNG is accepted.
 const bridge=`<script>(function(){const token=${JSON.stringify(token)};let sent=false;function send(png){if(sent)return;sent=true;parent.postMessage({type:'relay-thumbnail',token,png},'*')}async function capture(){try{await new Promise(r=>setTimeout(r,350));const svg=document.querySelector('svg');if(!svg){send('');return}const rect=svg.getBoundingClientRect(),copy=svg.cloneNode(true),originals=[svg,...svg.querySelectorAll('*')],copies=[copy,...copy.querySelectorAll('*')];for(let i=0;i<originals.length;i++){const styles=getComputedStyle(originals[i]);for(const p of ['fill','stroke','stroke-width','stroke-linecap','stroke-linejoin','font-family','font-size','font-weight','opacity','transform','transform-origin'])copies[i].style.setProperty(p,styles.getPropertyValue(p))}copy.querySelectorAll('script').forEach(n=>n.remove());copy.setAttribute('xmlns','http://www.w3.org/2000/svg');const w=rect.width||1440,h=rect.height||900;copy.setAttribute('width',String(w));copy.setAttribute('height',String(h));if(!copy.hasAttribute('viewBox'))copy.setAttribute('viewBox','0 0 '+w+' '+h);const image=new Image();image.onload=()=>{try{const canvas=document.createElement('canvas');canvas.width=720;canvas.height=450;const c=canvas.getContext('2d');c.fillStyle=getComputedStyle(document.body).backgroundColor||'#fff';c.fillRect(0,0,720,450);const s=Math.min(720/w,450/h);c.drawImage(image,(720-w*s)/2,(450-h*s)/2,w*s,h*s);send(canvas.toDataURL('image/png'))}catch{send('')}};image.onerror=()=>send('');image.src='data:image/svg+xml;charset=utf-8,'+encodeURIComponent(new XMLSerializer().serializeToString(copy))}catch{send('')}}if(document.readyState==='complete')capture();else window.addEventListener('load',capture,{once:true});setTimeout(()=>send(''),3500)})();<\/script>`;
 return `${document}\n${bridge}`;
}
export function Thumbnail({artifact,title}:{artifact:Artifact;title:string}){
 const holder=useRef<HTMLDivElement>(null);const frame=useRef<HTMLIFrameElement>(null);const [visible,setVisible]=useState(typeof IntersectionObserver==='undefined');
 const [png,setPng]=useState(()=>cache.get(artifact.id));const [failed,setFailed]=useState(false);const [token]=useState(()=>Array.from(crypto.getRandomValues(new Uint32Array(4)),n=>n.toString(16)).join('-'));
 useEffect(()=>{const element=holder.current;if(!element||typeof IntersectionObserver==='undefined')return;const observer=new IntersectionObserver(entries=>setVisible(entries.some(entry=>entry.isIntersecting)),{rootMargin:'100px'});observer.observe(element);return()=>observer.disconnect()},[]);
 const detail=useQuery({queryKey:['artifact',artifact.id],queryFn:()=>api<Artifact>(`/public/artifacts/${encodeURIComponent(artifact.id)}`),enabled:visible&&!png&&!failed&&!artifact.preview_document,staleTime:Infinity});
 const document=artifact.preview_document||detail.data?.preview_document;
 const slot=useCaptureSlot(visible&&!png&&!failed&&!!document);
 useEffect(()=>{if(!slot)return;const receive=(event:MessageEvent)=>{if(acceptsThumbnail(event,frame.current?.contentWindow||null,token)){const value=String(event.data.png);cache.set(artifact.id,value);if(cache.size>128)cache.delete(cache.keys().next().value!);setPng(value)}else if(event.source===frame.current?.contentWindow&&event.origin==='null'&&event.data?.type==='relay-thumbnail'&&event.data?.token===token&&event.data?.png==='')setFailed(true)};window.addEventListener('message',receive);const timeout=setTimeout(()=>setFailed(true),4500);return()=>{window.removeEventListener('message',receive);clearTimeout(timeout)}},[slot,artifact.id,token]);
 return <div className="thumbnail" ref={holder}>{png?<img src={png} alt={title} loading="lazy"/>:<div className="thumbnail-placeholder"><span>{failed||detail.error?'暂无静态封面':'正在读取真实作品'}</span><small>{failed||detail.error?'点击查看实际作品':'仅从作品实际 SVG 生成'}</small></div>}{slot&&document&&<iframe ref={frame} className="thumbnail-capture" sandbox={previewSandbox} referrerPolicy="no-referrer" srcDoc={thumbnailDocument(document,token)} title={`${title} · 生成缩略图`} allow=""/>}</div>;
}
