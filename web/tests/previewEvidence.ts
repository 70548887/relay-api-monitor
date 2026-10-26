import { expect, type Locator, type Page } from '@playwright/test';

export interface PixelRegion {x:number;y:number;width:number;height:number}

/** A scrolled opaque iframe is composited after the outer document's scroll. */
export async function settlePreviewPaint(page:Page){
 await page.locator('.intelligence-work .preview').scrollIntoViewIfNeeded();
 await page.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve()))));
}

export async function realPelicanGeometry(main:Locator){
 const outer=await main.boundingBox();expect(outer).toBeTruthy();
 const inner=await main.contentFrame().locator('svg').first().evaluate(svg=>{
  const rect=(element:Element)=>{const bounds=element.getBoundingClientRect();return {x:bounds.x,y:bounds.y,width:bounds.width,height:bounds.height}};
  const head=svg.querySelector('.pelican > circle[cx="506"][cy="171"]');
  return {width:innerWidth,height:innerHeight,scrollX,scrollY,svg:rect(svg),head:head?rect(head):null};
 });
 expect(inner.scrollX).toBe(0);expect(inner.scrollY).toBe(0);expect(inner.svg.x).toBeGreaterThanOrEqual(0);expect(inner.svg.y).toBeGreaterThanOrEqual(0);expect(inner.svg.x+inner.svg.width).toBeLessThanOrEqual(inner.width+1);expect(inner.svg.y+inner.svg.height).toBeLessThanOrEqual(inner.height+1);expect(inner.head,'The original generated pelican head must be present.').toBeTruthy();
 const head=inner.head!;const sx=outer!.width/inner.width,sy=outer!.height/inner.height;
 const region={x:outer!.x+head.x*sx+3,y:outer!.y+head.y*sy+3,width:head.width*sx-6,height:head.height*sy-6};
 expect(region.y).toBeGreaterThanOrEqual(outer!.y);expect(region.y+region.height).toBeLessThanOrEqual(outer!.y+outer!.height);
 return {outer:outer!,head:region};
}

/** Inspect the captured bitmap, not the SVG DOM's existence or visibility flags. */
export async function screenshotPixels(page:Page,png:Buffer,regions:PixelRegion[]){
 return page.evaluate(async({data,regions})=>{
  const image=new Image();image.src=data;await image.decode();const canvas=document.createElement('canvas');canvas.width=image.naturalWidth;canvas.height=image.naturalHeight;const context=canvas.getContext('2d')!;context.drawImage(image,0,0);
  return regions.map(region=>{const x=Math.max(0,Math.floor(region.x)),y=Math.max(0,Math.floor(region.y)),width=Math.min(canvas.width-x,Math.floor(region.width)),height=Math.min(canvas.height-y,Math.floor(region.height));if(width<=0||height<=0)throw new Error('Screenshot evidence region is outside the viewport');const pixels=context.getImageData(x,y,width,height).data;let white=0,warm=0;for(let i=0;i<pixels.length;i+=4){if(pixels[i]>245&&pixels[i+1]>245&&pixels[i+2]>245)white++;if(pixels[i]>170&&pixels[i+1]>140&&pixels[i+2]<220)warm++}return{whiteRatio:white/(width*height),warmRatio:warm/(width*height)}});
 },{data:`data:image/png;base64,${png.toString('base64')}`,regions});
}
