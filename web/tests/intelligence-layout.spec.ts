import {test,expect,type Page} from '@playwright/test';
import {readFile,mkdir} from 'node:fs/promises';
import path from 'node:path';
import {realPelicanGeometry,screenshotPixels,settlePreviewPaint} from './previewEvidence';

async function workbenchFixture(page:Page){
 const code=await readFile(new URL('./fixtures/pelican-832b1c7b33ca41c176e7f79714e75d6a-code.html',import.meta.url),'utf8');
 const document=await readFile(new URL('./fixtures/pelican-832b1c7b33ca41c176e7f79714e75d6a-preview.html',import.meta.url),'utf8');
 const prompt='创建一个HTML，内容是SVG绘一个鹈鹕骑自行车的2D动画';
 const history=Array.from({length:8},(_,i)=>({id:`layout-${i}`,job_id:`job-${i}`,model_id:'model',channel_id:'channel',platform_name:'布局验证平台',channel_name:'plus 线路',template_id:'cn_html',template_version:1,prompt,requested_model:'fixture-model',display_name:'fixture-model',returned_model:'fixture-model',reasoning_effort:i?'medium':'high',parameters:JSON.stringify({context_policy:'independent'}),protocol:'responses',kind:'html',created_at:new Date(Date.UTC(2026,9,28,6-i,6,14)).toISOString(),started_at:new Date(Date.UTC(2026,9,28,6-i,1,42)).toISOString(),duration_ms:272260+i*1000,first_text_ms:700,usage:{input_tokens:400,output_tokens:800,total_tokens:1200},finish_reason:'completed',code,preview_document:document}));
 const group={id:'channel',channel_id:'channel',channel_name:'plus 线路',platform_id:'platform',platform_name:'布局验证平台',model_id:'model',requested_model:'fixture-model',models:[{id:'model',upstream_id:'fixture-model',display_name:'fixture-model',protocol:'responses'}],group_verification:{status:'unconfigured'},interval_seconds:7200,next_run_at:new Date(Date.now()+600000).toISOString(),automatic_enabled:true,schedule_state:'waiting',schedule_message:'等待下次预计触发时间',latest_attempt:{id:'job-0',state:'succeeded',requested_model:'fixture-model',started_at:history[0].started_at,finished_at:history[0].created_at,duration_ms:272260,progress_chars:16239,context_policy:'independent'},history};
 await page.route('**/api/v1/public/templates',route=>route.fulfill({json:[{id:'cn_html',version:1,prompt}]}));
 await page.route('**/api/v1/public/intelligence?**',route=>route.fulfill({json:{groups:[group],history_scope:'group',worker_online:true,updated_at:new Date().toISOString()}}));
 await page.route('**/api/v1/public/artifacts/*',route=>route.fulfill({json:history.find(item=>item.id===new URL(route.request().url()).pathname.split('/').at(-1))}));
}

for(const width of [1600,1024,768,390]){
 test(`workbench fits ${width}px and keeps the artwork complete`,async({page})=>{
  await page.setViewportSize({width,height:1000});
  await workbenchFixture(page);
  await page.goto('/intelligence');
  const sidebar=page.getByRole('complementary',{name:'作品信息与计划'});
  const artwork=page.getByRole('region',{name:'所选作品预览'});
  await expect(artwork.locator('iframe')).toBeVisible();
  const left=(await sidebar.boundingBox())!,right=(await artwork.boundingBox())!;
  if(width>800){expect(right.x).toBeGreaterThanOrEqual(left.x+left.width);expect(Math.abs(right.y-left.y)).toBeLessThan(3)}
  else expect(right.y).toBeGreaterThanOrEqual(left.y+left.height);
  const layout=await page.evaluate(()=>({client:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth,columns:getComputedStyle(document.querySelector('.history-grid')!).gridTemplateColumns.split(' ').length}));
  expect(layout.scroll).toBeLessThanOrEqual(layout.client);
  expect(layout.columns).toBe(width<=800?2:4);
  const stage=(await artwork.locator('.preview-stage').boundingBox())!,frame=(await artwork.locator('iframe').boundingBox())!;
  expect(frame.width).toBeLessThanOrEqual(stage.width+1);expect(frame.height).toBeLessThanOrEqual(stage.height+1);
  await page.getByRole('button',{name:'预览 fixture-model 2',exact:true}).click();
  await expect(sidebar.getByRole('region',{name:'所选作品信息'}).getByText('medium',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'回到最新',exact:true}).click();
  await expect(sidebar.getByRole('region',{name:'所选作品信息'}).getByText('high',{exact:true})).toBeVisible();
  await page.locator('.history-grid').scrollIntoViewIfNeeded();
  await expect(page.locator('.history-card img').first()).toBeVisible({timeout:10000});
  if(width===1600||width===390){
   for(const card of await page.locator('.history-card').all()){
    await card.scrollIntoViewIfNeeded();
    await expect(card.locator('img')).toBeVisible({timeout:10000});
   }
   const output=path.resolve('../release/ui-design-20261028');await mkdir(output,{recursive:true});
   await page.evaluate(()=>window.scrollTo(0,0));
   await page.screenshot({path:path.join(output,`implemented-${width}.png`),fullPage:true});
  }
 });
}

test('source, download and enlarged preview remain usable in the workbench',async({page})=>{
 await workbenchFixture(page);await page.goto('/intelligence');
 const work=page.getByRole('region',{name:'所选作品预览'});
 await work.getByText('源码',{exact:true}).click();await expect(work.locator('.source-code')).toContainText('<html');
 expect(await work.locator('iframe').count()).toBe(0);
 const download=page.waitForEvent('download');await work.getByRole('button',{name:'下载',exact:true}).click();expect((await download).suggestedFilename()).toBe('relay-artifact.html');
 await work.getByText('预览',{exact:true}).click();await work.getByRole('button',{name:'放大预览',exact:true}).click();
 const dialog=page.getByRole('dialog');await expect(dialog.locator('iframe')).toHaveAttribute('sandbox','allow-scripts');expect(await work.locator('iframe').count()).toBe(0);
 await dialog.getByRole('button',{name:'Close',exact:true}).click();await expect(work.locator('iframe')).toBeVisible();
});

test('mobile viewport paints the real artwork after scrolling from history',async({page})=>{
 await page.setViewportSize({width:390,height:844});await workbenchFixture(page);await page.goto('/intelligence');
 await page.getByRole('button',{name:'预览 fixture-model 2',exact:true}).click();
 await settlePreviewPaint(page);
 const geometry=await realPelicanGeometry(page.locator('.intelligence-work iframe'));
 const screenshot=await page.screenshot({path:path.resolve('../release/ui-design-20261028/mobile-artwork-viewport.png'),type:'png',scale:'css'});
 const pixels=await screenshotPixels(page,screenshot,[geometry.head,{x:geometry.outer.x+3,y:geometry.outer.y+3,width:geometry.outer.width-6,height:geometry.outer.height-6}]);
 expect(pixels[0].whiteRatio).toBeLessThan(0.2);expect(pixels[1].warmRatio).toBeGreaterThan(0.1);
});
