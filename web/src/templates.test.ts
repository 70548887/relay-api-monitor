import { describe, expect, it } from 'vitest';
import { CN_PROMPT, EN_PROMPT } from './lib';
describe('original prompt templates',()=>{
 it('does not rewrite, translate or append to the Chinese template',()=>{expect(CN_PROMPT).toBe('创建一个HTML，内容是SVG绘一个鹈鹕骑自行车的2D动画')});
 it('does not rewrite, translate or append to the English template',()=>{expect(EN_PROMPT).toBe('Create code for an SVG of a pelican riding a bicycle as nicely as you can.')});
});
