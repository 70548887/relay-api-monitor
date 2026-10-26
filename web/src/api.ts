import { useSyncExternalStore } from 'react';
import type { Session } from './types';
let session:Session|null=null;
let sessionVersion=0;
const listeners=new Set<()=>void>();
const clearListeners=new Set<()=>void>();
export function onSessionCleared(callback:()=>void){clearListeners.add(callback);return()=>clearListeners.delete(callback)}
export function setSession(next:Session|null){const cleared=!!session&&!next;sessionVersion++;session=next;if(cleared)clearListeners.forEach(fn=>fn());listeners.forEach(fn=>fn())}
export async function refreshSession(signal?:AbortSignal){const version=sessionVersion;const data=await api<Session>('/auth/me',{signal});if(version===sessionVersion&&!signal?.aborted)setSession(data);return data}
export const useSession=()=>useSyncExternalStore(callback=>{listeners.add(callback);return()=>listeners.delete(callback)},()=>session);
export class APIError extends Error {constructor(public status:number,public code:string,message:string){super(message)}}
export async function api<T>(path:string,init:RequestInit={}):Promise<T>{
 const version=sessionVersion;
 const headers=new Headers(init.headers);if(init.body)headers.set('Content-Type','application/json');
 if(init.method && !['GET','HEAD'].includes(init.method.toUpperCase()) && session)headers.set('X-CSRF-Token',session.csrf_token);
 const res=await fetch(`/api/v1${path}`,{...init,headers,credentials:'same-origin'});
 if(!res.ok){const body=await res.json().catch(()=>null);if(res.status===401&&version===sessionVersion&&(path==='/auth/me'||path.startsWith('/admin/')))setSession(null);throw new APIError(res.status,body?.error?.code||'request_failed',body?.error?.message||`请求失败 (${res.status})`)}
 if(res.status===204 || res.headers.get('content-length')==='0')return undefined as T;
 const content=await res.text();return content ? JSON.parse(content) as T : undefined as T;
}
export const post=<T,>(path:string,body?:unknown)=>api<T>(path,{method:'POST',body:body===undefined?undefined:JSON.stringify(body)});
export const put=<T,>(path:string,body:unknown)=>api<T>(path,{method:'PUT',body:JSON.stringify(body)});
