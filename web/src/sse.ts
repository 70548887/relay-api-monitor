export interface SSEEvent {event:string;data:unknown}
/** Incremental SSE decoding: CRLF, chunk boundaries, UTF-8 and multi-line data are preserved. */
export async function readSSE(stream:ReadableStream<Uint8Array>,onEvent:(event:SSEEvent)=>void,signal?:AbortSignal){
 const reader=stream.getReader();const decoder=new TextDecoder();let buffer='';let event='message';let data:string[]=[];
 const dispatch=()=>{if(data.length){const raw=data.join('\n');let parsed:unknown;try{parsed=JSON.parse(raw)}catch{throw new Error('服务返回了无法解析的流数据')}onEvent({event,data:parsed})}event='message';data=[]};
 const line=(value:string)=>{if(value===''){dispatch();return}if(value.startsWith(':'))return;const index=value.indexOf(':');const key=index<0?value:value.slice(0,index);const content=index<0?'':value.slice(index+1).replace(/^ /,'');if(key==='event')event=content;if(key==='data')data.push(content)};
 const abort=()=>{void reader.cancel().catch(()=>{})};signal?.addEventListener('abort',abort,{once:true});
 try{while(true){if(signal?.aborted)throw new DOMException('已停止','AbortError');const result=await reader.read();if(signal?.aborted)throw new DOMException('已停止','AbortError');buffer+=decoder.decode(result.value,{stream:!result.done});let index:number;while((index=buffer.indexOf('\n'))>=0){line(buffer.slice(0,index).replace(/\r$/,''));buffer=buffer.slice(index+1)}if(result.done){if(buffer)line(buffer.replace(/\r$/,''));dispatch();break}}}catch(error){await reader.cancel().catch(()=>{});throw error}finally{signal?.removeEventListener('abort',abort);reader.releaseLock()}
}
