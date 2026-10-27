import type { UserConnection, OnlineTestRun } from './generated/domain';
export type Connection = UserConnection;
export type TestRecord = OnlineTestRun & {preview_document?:string};
export interface TestPage {items:TestRecord[];total:number;page:number;page_size:number}
export const privateNotice='连接密钥加密保存，页面仅显示掩码。测试内容和结果保存至私有历史，仅本人及管理员可查看。';
export const terminalTest=(state:string)=>!['created','running'].includes(state);
export const testState=(state:string)=>({created:'已建立',running:'执行中',succeeded:'成功',failed:'失败',canceled:'已停止',interrupted:'进程中断'}[state]||state);
export const testType=(template:string)=>template==='cn_html'?'中文动画':template==='en_svg'?'英文 SVG':'文本';
