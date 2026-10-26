import { test, expect } from '@playwright/test';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { realPelicanGeometry, screenshotPixels, settlePreviewPaint } from './previewEvidence';

const base=new URL(process.env.PLAYWRIGHT_BASE_URL||'http://127.0.0.1:42783');
const enabled=process.env.PLAYWRIGHT_LIVE_EVIDENCE==='1'&&base.origin==='http://127.0.0.1:42783';
const artifactID='832b1c7b33ca41c176e7f79714e75d6a';
const screenshotLimit=500*1024;
test.use({viewport:{width:1280,height:900},deviceScaleFactor:1});

test('read-only live artwork has actual SVG animation and an actual PNG cover',async({page},testInfo)=>{
 test.skip(!enabled,'Requires PLAYWRIGHT_LIVE_EVIDENCE=1 and the exact http://127.0.0.1:42783 live origin.');
 // Every explicit network operation in this test is GET. No routes replace live data.
 const writes:string[]=[];page.on('request',request=>{if(!['GET','HEAD','OPTIONS'].includes(request.method()))writes.push(`${request.method()} ${request.url()}`)});
 const response=await page.request.get('/api/v1/public/intelligence?template_id=cn_html');expect(response.ok()).toBe(true);
 const data=await response.json() as {groups:{platform_name:string;channel_name:string;history:{id:string}[]}[]};
 const group=data.groups.find(item=>item.history.some(artifact=>artifact.id===artifactID));expect(group,'The real Chinese artifact must be in its public group history.').toBeTruthy();
 await page.goto('/intelligence');await page.getByRole('switch',{name:'自动刷新',exact:true}).uncheck();
 await page.getByRole('tablist',{name:'平台',exact:true}).getByRole('tab',{name:group!.platform_name,exact:true}).click();
 await page.getByRole('tablist',{name:'分组',exact:true}).getByRole('tab',{name:group!.channel_name,exact:true}).click();
 const card=page.locator(`[data-artifact-id="${artifactID}"]`);await expect(card).toBeVisible();await card.locator('.history-preview').click();
 const main=page.locator('.intelligence-work iframe');await expect(main).toHaveAttribute('sandbox','allow-scripts');await expect(main).toHaveAttribute('referrerpolicy','no-referrer');
 const frame=main.contentFrame();await expect(frame.locator('svg').first()).toBeVisible();
 await expect.poll(()=>frame.locator('svg').first().evaluate(svg=>svg.getAnimations({subtree:true}).some(animation=>animation.playState==='running')),{message:'The actual generated SVG must contain a running DOM/CSS animation.'}).toBe(true);
 await card.scrollIntoViewIfNeeded();const cover=card.locator('.thumbnail img');await expect(cover).toHaveAttribute('src',/^data:image\/png;base64,/,{timeout:15000});await expect.poll(()=>cover.evaluate(image=>(image as HTMLImageElement).naturalWidth)).toBeGreaterThan(0);
 await settlePreviewPaint(page);
 const geometry=await realPelicanGeometry(main);
 const output=path.resolve(import.meta.dirname,'../../release/screenshots');await mkdir(output,{recursive:true});
 let filename=path.join(output,'intelligence-live-1.1.0.png');let screenshot=await page.screenshot({type:'png',fullPage:false,scale:'css'});
 expect(screenshot.readUInt32BE(16),'PNG viewport width must be 1280 pixels.').toBe(1280);
 const pixels=await screenshotPixels(page,screenshot,[geometry.head,{x:geometry.outer.x+8,y:geometry.outer.y+8,width:geometry.outer.width-16,height:geometry.outer.height/2-16}]);
 expect(pixels[0].whiteRatio,'The actual screenshot must include the pelican head instead of an unpainted white iframe tile.').toBeLessThan(0.2);
 expect(pixels[1].warmRatio,'The upper scene must include the actual sunset sky pixels.').toBeGreaterThan(0.1);
 if(screenshot.length>screenshotLimit){filename=path.join(output,'intelligence-live-1.1.0.jpg');screenshot=await page.screenshot({type:'jpeg',quality:80,fullPage:false,scale:'css'})}
 expect(screenshot.length,'Screenshot must be no larger than 500 KB.').toBeLessThanOrEqual(screenshotLimit);await writeFile(filename,screenshot);await testInfo.attach('Actual live artwork',{path:filename,contentType:filename.endsWith('.png')?'image/png':'image/jpeg'});
 await page.setViewportSize({width:390,height:844});await settlePreviewPaint(page);await expect(main).toBeVisible();await realPelicanGeometry(main);const size=await page.evaluate(()=>({width:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth}));expect(size.scroll).toBeLessThanOrEqual(size.width);expect(writes,'Live evidence must not send any mutating requests.').toEqual([]);
});
