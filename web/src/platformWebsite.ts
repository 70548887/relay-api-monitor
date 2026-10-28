export function platformWebsiteURL(value?:string):string|undefined {
 const text=value?.trim();
 if(!text||text.length>2048||/[\s\\\u0000-\u001f\u007f-\u009f]/u.test(text))return undefined;
 try{
  const url=new URL(text);
  if(!['http:','https:'].includes(url.protocol)||!url.hostname||url.username||url.password||url.port==='0')return undefined;
  return text;
 }catch{return undefined}
}
