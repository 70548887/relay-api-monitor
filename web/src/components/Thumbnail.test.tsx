import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, describe, expect, it } from 'vitest';
import type { Artifact } from '../types';
import { acceptsThumbnail, MAX_THUMBNAIL_BYTES, Thumbnail, thumbnailDocument } from './Thumbnail';
const png='data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLbtAAAAABJRU5ErkJggg==';
afterEach(cleanup);
describe('real thumbnail capture bridge',()=>{
 it('accepts bounded PNGs only from the exact opaque sandbox and random token',()=>{const source=window;const valid={source,origin:'null',data:{type:'relay-thumbnail',token:'random-token',png}};expect(acceptsThumbnail(valid,source,'random-token')).toBe(true);expect(acceptsThumbnail({...valid,source:null},source,'random-token')).toBe(false);expect(acceptsThumbnail({...valid,origin:'https://example.test'},source,'random-token')).toBe(false);expect(acceptsThumbnail(valid,source,'wrong-token')).toBe(false);expect(acceptsThumbnail({...valid,data:{...valid.data,png:'data:image/svg+xml,<svg onload="alert(1)"/>'}},source,'random-token')).toBe(false);expect(acceptsThumbnail({...valid,data:{...valid.data,png:png+'A'.repeat(MAX_THUMBNAIL_BYTES)}},source,'random-token')).toBe(false)});
 it('preserves original document and keeps its SVG and scripts inside the frame',()=>{const original='<svg id="untrusted-only"><circle/></svg><script>window.untrusted=true</script>';const captured=thumbnailDocument(original,'unique');expect(captured.startsWith(original)).toBe(true);expect(captured).toContain("document.querySelector('svg')");expect(captured).toContain("canvas.toDataURL('image/png')");expect(captured).toContain('unique');expect(captured.endsWith('</script>')).toBe(true);expect(()=>new Function(captured.slice(captured.lastIndexOf('<script>')+8,-9))).not.toThrow()});
 it('runs at most two capture frames then replaces a validated frame with a cached static image',async()=>{
  const client=new QueryClient({defaultOptions:{queries:{retry:false}}});const artifacts=Array.from({length:3},(_,i)=>({id:`thumb-fixture-${i}`,preview_document:'<svg id="untrusted-only" xmlns="http://www.w3.org/2000/svg"><circle/></svg>'} as Artifact));
  const view=render(<QueryClientProvider client={client}>{artifacts.map((artifact,i)=><Thumbnail key={artifact.id} artifact={artifact} title={`thumb-${i}`}/>)}</QueryClientProvider>);
  await waitFor(()=>expect(view.container.querySelectorAll('iframe')).toHaveLength(2));expect(view.container.querySelector('#untrusted-only')).toBeNull();const frame=view.container.querySelector('iframe')!;expect(frame).toHaveAttribute('sandbox','allow-scripts');expect(frame).not.toHaveAttribute('src');const token=frame.getAttribute('srcdoc')!.match(/const token=("[^"]+")/)?.[1];expect(token).toBeTruthy();
  act(()=>window.dispatchEvent(new MessageEvent('message',{origin:'null',source:window,data:{type:'relay-thumbnail',token:JSON.parse(token!),png}})));expect(screen.queryByRole('img',{name:'thumb-0'})).not.toBeInTheDocument();
  act(()=>window.dispatchEvent(new MessageEvent('message',{origin:'null',source:frame.contentWindow,data:{type:'relay-thumbnail',token:JSON.parse(token!),png}})));await screen.findByRole('img',{name:'thumb-0'});await waitFor(()=>expect(view.container.querySelectorAll('iframe')).toHaveLength(2));view.unmount();client.clear();
 });
});
