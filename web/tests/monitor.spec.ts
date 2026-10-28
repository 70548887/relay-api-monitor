import {test,expect,type Page} from '@playwright/test';
import {mkdir} from 'node:fs/promises';
import path from 'node:path';

const snapshot='2026-10-28T08:40:00Z';
function monitoredModel(id:string,platform:string,overrides:Record<string,unknown>={}){
 return {
  id,platform_id:platform,platform_name:platform==='alpha'?'布局夹具 Alpha':'布局夹具 Beta',channel_id:`group-${platform}`,channel_name:`${platform}-group`,
  upstream_id:'shared-model',display_name:'shared-model',protocol:'responses',probe_enabled:true,
  status:'ok',stale:false,running:false,success_rate:100,samples:20,
  last_probe:{id:`probe-${id}`,model_id:id,status:'ok',http_status:200,first_text_ms:1200,duration_ms:3400,returned_model:'shared-model',created_at:snapshot},
  timeline:Array.from({length:24},(_,i)=>({at:new Date(Date.parse('2026-10-27T09:00:00Z')+i*3600000).toISOString(),status:'ok',count:i<3?0:2})),
  ...overrides,
 };
}
async function monitorFixture(page:Page){
 const requests:{path:string;method:string}[]=[];
 const models=[
  monitoredModel('alpha-ok','alpha'),
  monitoredModel('alpha-slow','alpha',{upstream_id:'slow-fixture-model',display_name:'slow-fixture-model',status:'slow'}),
  monitoredModel('alpha-unknown','alpha',{upstream_id:'unknown-fixture-model',display_name:'unknown-fixture-model',status:'unknown',stale:true,samples:0,success_rate:null,last_probe:null,timeline:[]}),
  monitoredModel('alpha-disabled','alpha',{upstream_id:'disabled-fixture-model',display_name:'disabled-fixture-model',status:'disabled',probe_enabled:false,samples:0,success_rate:null,last_probe:null,timeline:[]}),
  monitoredModel('beta-failed','beta',{status:'failed',success_rate:70,samples:10,last_probe:{id:'beta-failure',model_id:'beta-failed',status:'failed',http_status:200,first_text_ms:null,duration_ms:2000,error_code:'upstream_error',error_message:'上游检测失败',created_at:snapshot}}),
  monitoredModel('beta-stale','beta',{upstream_id:'stale-fixture-model',display_name:'stale-fixture-model',stale:true}),
 ];
 const data={
  platforms:[{id:'alpha',name:'布局夹具 Alpha',public:true},{id:'beta',name:'布局夹具 Beta',public:true}],
  channels:[{id:'group-alpha',platform_id:'alpha',name:'alpha-group',sort:0},{id:'group-beta',platform_id:'beta',name:'beta-group',sort:0}],
  models,summary:{platforms:2,models:6,normal:2,slow:1,failed:1,stale:2},updated_at:snapshot,
 };
 await page.route('**/api/v1/**',async route=>{
  const request=route.request(),pathname=new URL(request.url()).pathname;
  requests.push({path:pathname,method:request.method()});
  if(request.method()!=='GET')return route.fulfill({status:405,json:{error:{code:'fixture_read_only',message:'Browser fixtures never generate requests'}}});
  if(pathname==='/api/v1/public/monitor')return route.fulfill({json:data});
  if(pathname==='/api/v1/user/auth/me')return route.fulfill({status:401,json:{error:{code:'unauthorized',message:'请先登录'}}});
  const modelId=pathname.match(/^\/api\/v1\/public\/models\/([^/]+)\/history$/)?.[1];
  if(modelId){
   const selected=models.find(model=>model.id===modelId);
   return route.fulfill({json:selected?.last_probe?[{...selected.last_probe,job_id:`history-${modelId}`,protocol:'responses',usage:{},finish_reason:selected.status==='failed'?'':'completed'}]:[]});
  }
  return route.fulfill({status:404,json:{error:{code:'unexpected_fixture_request',message:pathname}}});
 });
 return requests;
}
function platforms(page:Page){return page.getByRole('group',{name:'平台导航',exact:true})}

for(const width of [1600,390]){
 test(`monitor layout fits ${width}px without page-wide horizontal overflow`,async({page})=>{
  await page.setViewportSize({width,height:1000});const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
  const requests=await monitorFixture(page);await page.goto('/');
  await expect(page.getByRole('heading',{name:'中转 API 监控',exact:true})).toBeVisible();
  await expect(platforms(page)).toBeVisible();await expect(page.locator('.monitor-results')).toBeVisible();
  const dimensions=await page.evaluate(()=>({viewport:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth}));
  expect(dimensions.scroll).toBeLessThanOrEqual(dimensions.viewport);
  if(width>800){
   const rail=(await platforms(page).boundingBox())!,results=(await page.locator('.monitor-results').boundingBox())!;
   expect(results.x).toBeGreaterThanOrEqual(rail.x+rail.width);
   await expect(page.locator('.monitor-model-table').first()).toBeVisible();await expect(page.locator('.monitor-mobile-models').first()).toBeHidden();
  }else{
   await expect(page.locator('.monitor-mobile-models').first()).toBeVisible();await expect(page.locator('.monitor-model-table').first()).toBeHidden();
  }
  expect(requests.every(request=>request.method==='GET')).toBe(true);expect(errors).toEqual([]);
  const folder=path.resolve('../release/monitor-implementation-20261028');await mkdir(folder,{recursive:true});
  await page.screenshot({path:path.join(folder,width===1600?'monitor-desktop.png':'monitor-mobile.png'),fullPage:true});
 });
}

test('global search and abnormal filtering preserve navigation and exclude a never-probed stale flag',async({page})=>{
 const requests=await monitorFixture(page);await page.goto('/');
 await platforms(page).getByRole('button',{name:'布局夹具 Beta',exact:true}).click();
 await page.getByRole('textbox',{name:'搜索平台、分组或模型'}).fill('slow-fixture-model');
 await expect(platforms(page).getByRole('button',{name:'布局夹具 Alpha',exact:true})).toHaveAttribute('aria-pressed','true');
 await expect(platforms(page).getByRole('button',{name:'布局夹具 Beta',exact:true})).toBeVisible();
 await expect(page.locator('.monitor-results')).toContainText('slow-fixture-model');
 await page.getByRole('textbox',{name:'搜索平台、分组或模型'}).fill('');
 await platforms(page).getByRole('button',{name:'布局夹具 Alpha',exact:true}).click();
 await page.getByRole('switch',{name:'仅看异常',exact:true}).check();
 await expect(page.locator('.monitor-results')).toContainText('slow-fixture-model');
 await expect(page.locator('.monitor-results')).not.toContainText('unknown-fixture-model');
 await expect(page.locator('.monitor-results')).not.toContainText('disabled-fixture-model');
 await platforms(page).getByRole('button',{name:'布局夹具 Beta',exact:true}).click();
 await expect(page.locator('.monitor-results')).toContainText('stale-fixture-model');
 await expect(page.locator('.monitor-results')).toContainText('shared-model');
 expect(requests.filter(request=>request.method!=='GET')).toEqual([]);
});

test('group disclosure and expand-all controls preserve the selected platform',async({page})=>{
 await monitorFixture(page);await page.goto('/');
 const toggle=page.getByRole('button',{name:/alpha-group/});
 await expect(toggle).toHaveAttribute('aria-expanded','true');await toggle.click();
 await expect(toggle).toHaveAttribute('aria-expanded','false');
 await expect(page.getByRole('button',{name:'查看 shared-model 检测历史',exact:true})).toHaveCount(0);
 await page.getByRole('button',{name:'展开全部',exact:true}).click();await expect(toggle).toHaveAttribute('aria-expanded','true');
 await page.getByRole('button',{name:'折叠全部',exact:true}).click();await expect(toggle).toHaveAttribute('aria-expanded','false');
 await expect(platforms(page).getByRole('button',{name:'布局夹具 Alpha',exact:true})).toHaveAttribute('aria-pressed','true');
});

test('same-name history uses the selected model ID and keeps a failed HTTP 200 result failed',async({page})=>{
 const requests=await monitorFixture(page);await page.goto('/');
 await platforms(page).getByRole('button',{name:'布局夹具 Beta',exact:true}).click();
 await page.getByRole('button',{name:'查看 shared-model 检测历史',exact:true}).click();
 const modal=page.getByRole('dialog');await expect(modal).toBeVisible();
 await expect(modal.locator('.monitor-history-current')).toContainText('布局夹具 Beta / beta-group');
 const row=modal.getByRole('row').filter({has:page.getByRole('cell',{name:'200',exact:true})});
 await expect(row.getByRole('cell',{name:'失败',exact:true})).toBeVisible();await expect(row).toContainText('上游检测失败');
 expect(requests.filter(request=>request.path.endsWith('/history')).map(request=>request.path)).toEqual(['/api/v1/public/models/beta-failed/history']);
 await expect(modal).toContainText('HTTP 状态本身不代表模型调用成功');
});

test('empty hourly intervals remain gray and tooltips explicitly use Beijing time',async({page})=>{
 await monitorFixture(page);await page.goto('/');
 const timeline=page.locator('.monitor-model-table').first().getByLabel('shared-model 最近24小时状态');
 const blocks=timeline.locator('.timeline-block');await expect(blocks).toHaveCount(24);
 await expect(blocks.first()).toHaveClass(/unknown/);await expect(blocks.nth(3)).toHaveClass(/ok/);
 const colors=await blocks.evaluateAll(nodes=>[getComputedStyle(nodes[0]).backgroundColor,getComputedStyle(nodes[3]).backgroundColor]);
 const channels=colors[0].match(/\d+/g)!.map(Number);expect(Math.max(...channels)-Math.min(...channels)).toBeLessThan(18);expect(colors[0]).not.toEqual(colors[1]);
 await blocks.first().hover();const tooltip=page.getByRole('tooltip');
 await expect(tooltip).toContainText('2026/10/27 17:00:00');await expect(tooltip).toContainText('暂无样本');await expect(tooltip).toContainText('北京时间');
});
