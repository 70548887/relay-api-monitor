import {describe,expect,it} from 'vitest';
import zhCN from 'antd/es/locale/zh_CN';

describe('Chinese component locale',()=>{
 it('uses the actual ESM locale object in production-compatible imports',()=>{
  expect(zhCN.locale).toBe('zh-cn');
  expect(zhCN.Modal?.okText).toBe('确定');
  expect(zhCN.Modal?.cancelText).toBe('取消');
 });
});
