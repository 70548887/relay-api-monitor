import { test, expect, type Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { realPelicanGeometry, screenshotPixels, settlePreviewPaint } from './previewEvidence';

const source='<svg xmlns="http://www.w3.org/2000/svg" width="1440" height="900" viewBox="0 0 1440 900"><rect width="1440" height="900" fill="#ecf5dd"/><circle cx="720" cy="450" r="180" fill="#47734d"/><text x="720" y="470" text-anchor="middle" fill="white" font-size="50">Real SVG fixture</text></svg>';
const artifact=(id:string,version=1)=>({id,job_id:`job-${id}`,model_id:'m',channel_id:'g',platform_name:'Fixture platform',channel_name:'Fixture group',template_id:'cn_html',template_version:version,prompt:'exact fixture prompt',requested_model:id,display_name:id,returned_model:`actual-${id}`,reasoning_effort:'',thinking_mode:'',thinking_budget:0,verified_group:'auto',group_verification_status:'verified',protocol:'chat',kind:'svg',created_at:'2026-10-26T00:00:00Z',started_at:'2026-10-25T23:59:59Z',duration_ms:1000,first_text_ms:30,usage:{input_tokens:10,output_tokens:20,total_tokens:30},finish_reason:'stop',code:source,preview_document:`<!doctype html><html><body style="margin:0">${source}</body></html>`});
async function fixtures(page:Page,history=Array.from({length:8},(_,i)=>artifact(`fixture-${i}`))){
 const group={id:'g',channel_id:'g',channel_name:'Fixture group',platform_id:'p',platform_name:'Fixture platform',model_id:'m',requested_model:'fixture-request',models:[{id:'m',display_name:'fixture',upstream_id:'fixture-request',protocol:'chat'}],group_verification:{status:'unsupported',scope:'token_binding'},reasoning_effort:'',thinking_mode:'',thinking_budget:0,interval_seconds:600,next_run_at:'2026-10-26T01:00:00Z',automatic_enabled:true,latest_attempt:{id:'in-progress',state:'running',requested_model:'fixture-request',progress_chars:4321,started_at:'2026-10-26T00:00:00Z',reasoning_effort:''},history};
 await page.route('**/api/v1/public/templates',route=>route.fulfill({json:[{id:'cn_html',prompt:'exact fixture prompt',version:1}]}));
 await page.route('**/api/v1/public/intelligence?**',route=>route.fulfill({json:{groups:[group],history_scope:'group',updated_at:'2026-10-26T00:00:05Z',worker_online:true}}));
 await page.route('**/api/v1/public/artifacts/*',route=>{const id=new URL(route.request().url()).pathname.split('/').at(-1);return route.fulfill({json:history.find(item=>item.id===id)})});
 return group;
}
test('renders real static PNG thumbnails and keeps scripts in opaque sandboxes',async({page})=>{
 const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));await fixtures(page);await page.goto('/intelligence');await expect(page.getByText('4,321',{exact:true})).toBeVisible();await expect(page.getByText('上游不支持分组验证',{exact:true})).toBeVisible();await expect(page.locator('.intelligence-work iframe')).toHaveAttribute('sandbox','allow-scripts');
 await page.locator('.intelligence-history').scrollIntoViewIfNeeded();await expect(page.locator('.history-card img').first()).toBeVisible({timeout:15000});await expect(page.locator('.history-card img').first()).toHaveAttribute('src',/^data:image\/png;base64,/);expect(await page.locator('.thumbnail-capture').count()).toBeLessThanOrEqual(2);expect(await page.locator('svg circle').count()).toBe(0);
 await page.locator('[data-artifact-id="fixture-1"] .history-preview').click();await expect(page.locator('.intelligence-work iframe')).toHaveAttribute('title','fixture-1 · 当前作品');await page.getByRole('button',{name:'刷新',exact:true}).click();await expect(page.locator('.intelligence-work iframe')).toHaveAttribute('title','fixture-1 · 当前作品');expect(errors).toEqual([]);
});

for(const state of ['claimed','dispatching']){
 test(`reports the actual ${state} worker state and elapsed time`,async({page})=>{
  const group=await fixtures(page,[]);group.latest_attempt.state=state;
  group.latest_attempt.started_at=new Date(Date.now()-15000).toISOString();
  await page.goto('/intelligence');const status=page.getByRole('region',{name:'当前分组状态'});
  await expect(status.getByText(state==='claimed'?'准备生成':'生成中',{exact:true})).toBeVisible();
  const elapsed=status.locator('.group-metrics > div').filter({has:page.getByText('本次耗时',{exact:true})}).locator('strong');
  await expect(elapsed).toHaveText(/\d+\.\d{2} s/);
 });
}
test('retains empty group progress and fits the mobile viewport',async({page})=>{
 await fixtures(page,[]);await page.setViewportSize({width:390,height:844});await page.goto('/intelligence');await expect(page.getByText('这个分组还没有成功作品')).toBeVisible();await expect(page.getByText('4,321',{exact:true})).toBeVisible();await expect(page.getByText('正在接收生成内容，完成后会显示作品。')).toBeVisible();const dimensions=await page.evaluate(()=>({width:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth}));expect(dimensions.scroll).toBeLessThanOrEqual(dimensions.width);
});
test('compares matching prompt versions and rejects a different template version',async({page})=>{
 await fixtures(page,[artifact('first'),artifact('second'),artifact('different',2)]);await page.goto('/intelligence');await page.getByLabel('选择作品 1',{exact:true}).check();await expect(page.getByLabel('选择作品 3',{exact:true})).toBeDisabled();await page.getByLabel('选择作品 2',{exact:true}).check();await page.getByRole('button',{name:'并排比较',exact:true}).click();await expect(page.getByText('已核对提示词原文与模板 v1 完全一致')).toBeVisible();await expect(page.getByRole('dialog').locator('iframe')).toHaveCount(2);
});
test('scales the full 1440 by 900 main artwork when switching history on mobile',async({page})=>{
 await fixtures(page,[artifact('mobile-first'),artifact('mobile-second')]);await page.setViewportSize({width:390,height:844});await page.goto('/intelligence');await page.locator('.intelligence-work').scrollIntoViewIfNeeded();await expect(page.locator('.intelligence-work iframe')).toBeVisible();let frame=await page.locator('.intelligence-work iframe').boundingBox();let stage=await page.locator('.intelligence-work .preview-stage').boundingBox();expect(frame!.width).toBeLessThanOrEqual(stage!.width+1);expect(frame!.height).toBeLessThanOrEqual(stage!.height+1);expect(frame!.width/frame!.height).toBeCloseTo(1.6,1);await page.locator('[data-artifact-id="mobile-second"] .history-preview').click();await expect(page.locator('.intelligence-work iframe')).toHaveAttribute('title','mobile-second · 当前作品');frame=await page.locator('.intelligence-work iframe').boundingBox();stage=await page.locator('.intelligence-work .preview-stage').boundingBox();expect(frame!.width).toBeLessThanOrEqual(stage!.width+1);const dimensions=await page.evaluate(()=>({width:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth}));expect(dimensions.scroll).toBeLessThanOrEqual(dimensions.width);
});
test('the unchanged real generated HTML paints its complete head and scene after returning from history',async({page})=>{
 const original=artifact('actual-generated-layout');original.code=await readFile(new URL('./fixtures/pelican-832b1c7b33ca41c176e7f79714e75d6a-code.html',import.meta.url),'utf8');original.preview_document=await readFile(new URL('./fixtures/pelican-832b1c7b33ca41c176e7f79714e75d6a-preview.html',import.meta.url),'utf8');await fixtures(page,[original]);await page.setViewportSize({width:1280,height:900});await page.goto('/intelligence');const card=page.locator('[data-artifact-id="actual-generated-layout"]');await card.scrollIntoViewIfNeeded();await expect(card.locator('.thumbnail img')).toHaveAttribute('src',/^data:image\/png;base64,/);await settlePreviewPaint(page);const geometry=await realPelicanGeometry(page.locator('.intelligence-work iframe'));const screenshot=await page.screenshot({type:'png',scale:'css'});const pixels=await screenshotPixels(page,screenshot,[geometry.head,{x:geometry.outer.x+8,y:geometry.outer.y+8,width:geometry.outer.width-16,height:geometry.outer.height/2-16}]);expect(pixels[0].whiteRatio).toBeLessThan(0.2);expect(pixels[1].warmRatio).toBeGreaterThan(0.1);
});
