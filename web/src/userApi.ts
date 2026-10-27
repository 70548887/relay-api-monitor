import { useSyncExternalStore } from 'react';
import { APIError } from './api';

export interface Member { id:string; username:string; disabled?:boolean; created_at?:string }
export interface UserSession { user:Member; csrf_token:string }
let session:UserSession|null=null;
let generation=0;
const listeners=new Set<()=>void>();
const clearListeners=new Set<()=>void>();
export function onUserSessionCleared(callback:()=>void){clearListeners.add(callback);return()=>clearListeners.delete(callback)}
export function setUserSession(next:UserSession|null){const cleared=!!session&&(!next||session.user.id!==next.user.id);generation++;session=next;if(cleared)clearListeners.forEach(fn=>fn());listeners.forEach(fn=>fn())}
export const useUserSession=()=>useSyncExternalStore(callback=>{listeners.add(callback);return()=>listeners.delete(callback)},()=>session);
export function userRequestHeaders(init?:HeadersInit){const headers=new Headers(init);if(session)headers.set('X-CSRF-Token',session.csrf_token);return headers}
export async function userApi<T>(path:string,init:RequestInit={}):Promise<T>{
 const started=generation;const headers=new Headers(init.headers);
 if(init.body)headers.set('Content-Type','application/json');
 if(init.method&&!['GET','HEAD'].includes(init.method.toUpperCase())&&session)headers.set('X-CSRF-Token',session.csrf_token);
 const url=path.startsWith('/test/')?`/api/v1${path}`:`/api/v1/user${path}`;
 const res=await fetch(url,{...init,headers,credentials:'same-origin'});
 if(!res.ok){const body=await res.json().catch(()=>null);if(res.status===401&&started===generation&&path!=='/auth/login')setUserSession(null);throw new APIError(res.status,body?.error?.code||'request_failed',body?.error?.message||`请求失败 (${res.status})`)}
 const content=await res.text();return content?JSON.parse(content) as T:undefined as T;
}
export async function refreshUserSession(signal?:AbortSignal):Promise<UserSession|null>{
 const started=generation;
 try{const next=await userApi<UserSession>('/auth/me',{signal});if(started===generation&&!signal?.aborted)setUserSession(next);return next}catch(error){if(error instanceof APIError&&error.status===401)return null;throw error}
}
export const userPost=<T,>(path:string,body?:unknown)=>userApi<T>(path,{method:'POST',body:body===undefined?'{}':JSON.stringify(body)});

// The member portal can only return to its own pages; admin navigation uses
// a different guard and never accepts this member login as authorization.
export function safeUserNext(value:string|null):string{
 if(!value||!value.startsWith('/')||value.startsWith('//')||/[\\\u0000-\u001f]/.test(value))return '/account';
 try{const url=new URL(value,'https://relaylab.invalid');if(url.origin!=='https://relaylab.invalid'||(!['/','/test','/intelligence','/account','/account/connections','/account/tests'].includes(url.pathname)&&!/^\/account\/tests\/[a-f0-9]{32}$/.test(url.pathname)))return '/account';return url.pathname+url.search+url.hash}catch{return '/account'}
}
