# Relay Lab · 中转观测站

Go + React 的中转 API 监控与模型测试平台。公开页面包含中转 API 监控、鹈鹕作品比较和访客自带 Key 在线测试；管理后台配置平台、分组、模型与自动任务。

当前版本 **1.1.1**。智商测试采用分组状态、主动画和八次真实历史预览，支持实际推理强度配置。手动新增和自动发现的模型均默认关闭自动付费任务；归档对象不再出现在可运行目标中。升级保留账号、加密Key与历史数据。

## 本机启动

开发构建需要 Go 1.26+、Node.js 22.16+；成品启动只需要 `bin/relaylab.exe`。

如果使用已经打包的 Windows 成品，直接运行 `scripts/start.ps1`，跳过下面的构建步骤。项目固定使用 Go 1.26.10 修复版本工具链，Go 会按 `go.mod` 自动获取。

```powershell
Set-Location 'D:\中转API监控'
powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\start.ps1
```

打开 `http://127.0.0.1:42783`，进入管理后台创建首个管理员。没有预置密码，密码至少12字节。初始化接口仅允许本机请求。默认生产端口42783、开发端口42784。

停止程序：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\stop.ps1
```

`start.ps1` 在后台启动网站与 Worker，保存进程ID和启动时间，用于停止时核对身份。日志位于数据目录。强制停止时，上游已收到但本机未完成的任务可能被标为“结果未知”，恢复后不自动重发。

## 配置流程

1. 登录后台，添加平台名称，开启平台公开展示。
2. 在平台下添加分组，填写 API 地址、API Key、模型发现协议和默认生成协议。不同分组可使用不同 Key。
3. 获取模型；模型目录获取成功不等于推理请求已通过。目录失败保留原模型，可手动补录。
4. 选中需要公开、监控和生成作品的模型。所有新发现模型默认不公开、不执行付费任务。
5. 可点击立即检测／生成；自动任务默认监控5分钟一次、作品30分钟一次，按模型错峰。分组可指定代表测试模型，指定后该组自动创作仅使用它。
6. 在运行概览确认 Worker 在线。未运行 Worker 时网站仍可访问，但定时任务不执行。

API 地址只填根站点时自动使用 `/v1`；已带路径时保留。高级设置允许指定模型列表和生成接口路径。生成协议支持 Chat Completions、Responses、Anthropic Messages，不根据模型名称猜测协议，也不自动换接口重试。

本产品的分组是本地配置标签；不宣称普通模型调用 Key 能获取平台真实账号分组、余额或全部运营数据。

## 三个公开栏目

- **中转 API 监控**：按平台和分组展开；展示真实检测结果、首正文耗时、完整耗时、7天成功率与24小时状态色块。缺样本显示空白，超过检测间隔两倍显示过期。
- **智商测试**：主区域直接展示当前HTML/SVG，下面切换八次真实历史缩略图；默认按分组及模板保留8次，可在设置改为按模型。页面每5秒自动刷新，展示排队、生成、字符进度、下一次执行和Worker状态；无成功作品时仍展示状态。仅同一提示词与版本可并排比较，不输出“智商分”。
- **在线测试**：访客填写自己的地址和 Key，获取模型并测试文本或动画。地址、Key、输入和回复仅在页面内存与当前请求使用，不入库，不自动发布到作品墙。

内置提示词保持原文：

```text
Create code for an SVG of a pelican riding a bicycle as nicely as you can.
```

```text
创建一个HTML，内容是SVG绘一个鹈鹕骑自行车的2D动画
```

作品在不具备同源权限的 iframe 中预览，允许所需脚本并限制外部资源、表单、弹窗和导航。主页面不会直接插入生成 HTML；源码下载为纯文本附件。沙箱用于页面隔离，不承诺浏览器级绝对断网。生成代码无法完成提取或上游输出被截断时，任务明确失败并保留上一份成功作品。

历史封面从作品实际SVG内容生成PNG，最多同时使用2个隔离采集frame；生成后静态缓存并卸载采集frame。不具备可提取SVG的内容显示“暂无静态封面”，仍可查看原始作品。

## 推理强度和分组核验

分组可设置默认推理强度，模型可继承或覆盖；“上游默认”表示不发送该参数。Chat发送`reasoning_effort`，Responses发送`reasoning.effort`，Anthropic发送`output_config.effort`及显式选择的thinking配置。协议不支持的组合会在请求前被拒绝；上游模型拒绝参数时明确失败，不自动降级重试。页面和作品保存的是实际请求参数。

可在分组选择New API或自定义验证提供方，点击“验证绑定组”。New API请求单一`/api/token/self`；自定义提供方填写接口路径和JSON字段路径。核验结果仅表示Token绑定，不证明模型请求实际命中了哪个账号或路由；自动分组`auto`同样不作实际命中承诺。不支持的接口会明确显示“不支持核验”。

本次使用用户提供的接口完成了Chat/Responses High、中文HTML、英文SVG和监控实测；模型均为`gpt-6.1-sol`。中文作品约208秒、英文约143秒；结果已显示在本机页面。该平台没有提供标准绑定组验证接口。详细脱敏结果见`docs/LIVE_TESTS.md`。

## 默认任务参数

监控并发4、作品并发2、每分组后台并发2；同一模型同类任务最多运行1个。访客生成使用单独的8个全局并发、每IP 2个额度。后台可调整。

模型发现超时20秒；监控60秒／输出上限1024；新作品配置默认600秒／输出上限8192，均可按模型覆盖。已有用户显式保存的超时不会被静默覆盖。首正文超过10秒标记偏慢。未返回用量时显示“未提供”。

任务状态为 `queued`、`claimed`、`dispatching`、`succeeded`、`failed`、`canceled`、`uncertain`，另有跳过记录。领取租约45秒，每10秒续租；已发出请求而结果无法确认时不自动重发。停止请求可取消本系统的等待，但不能保证上游没有产生用量。

SQLite 使用 WAL、每连接外键与5秒忙等待。配置、任务、检测、作品及审计保存在同一数据库；网络调用不占数据库事务。记录保留默认30天，作品每模型每模板最多保留8份成功结果。

写事务采用 immediate 模式，服务与 Worker 的独立连接先获取写入权再读改写。任务提交同时核对 Worker 全局租约和任务租约；渠道配置修改后，旧配置发起的模型同步结果会被丢弃。调短周期会重新安排下一次执行时间，停用计划会取消其排队任务。

## 数据与备份

默认数据目录 `data/`，数据库 `relaylab.db`，密钥 `master.key`。后台 Key 使用 AES-256-GCM 加密；密码使用 Argon2id。**数据库和 master.key 必须一起备份**。数据库已存在但主密钥丢失时程序会停止，不生成新密钥覆盖旧配置。

```powershell
.\scripts\backup.ps1
.\scripts\restore.ps1 -InputPath '.\backups\relaylab-日期.zip' -DataDir '.\data-restored'
```

备份通过 SQLite 一致性快照生成，不直接复制运行中的数据库文件。恢复仅允许空目录，校验数据库与密钥后提交。备份 ZIP 含密钥，应作为私密数据保管。

## 命令行与开发

```powershell
.\bin\relaylab.exe serve --addr 127.0.0.1:42783 --data-dir .\data
.\bin\relaylab.exe worker --data-dir .\data
.\bin\relaylab.exe init-admin --username admin --data-dir .\data
.\bin\relaylab.exe backup --data-dir .\data --output .\backups\manual.zip
.\bin\relaylab.exe restore --input .\backups\manual.zip --data-dir .\restored
```

可通过 `RELAYLAB_DATA_DIR` 和 `RELAYLAB_ADDR` 设置默认路径、监听地址。开发前端使用 `cd web; npm run dev`，监听42784，API代理到42783。本机初始化建议从42783同源页面完成。域名、反向代理与正式服务器部署不包含在本机首版交付内。

所有上游请求仅连接公共 HTTP/HTTPS 地址，固定已校验DNS结果，禁止自动重定向与内网目标；生产配置不包含绕过此检查的开关。本地测试通过显式注入测试HTTP客户端完成。

## 接口与验证

接口根路径 `/api/v1`。OpenAPI 为 `docs/openapi.json`，前端共享类型为 `web/src/generated/domain.ts`。运行 `go run ./cmd/specgen` 从 Go 类型刷新契约。管理写操作携带 `X-CSRF-Token`；会话通过 HttpOnly SameSite Cookie 保存。

登录和初始化支持非浏览器 JSON CLI；浏览器来源必须同源，首次初始化还要求本机 Host 和连接。JSON 请求只接受一个完整对象，最大2MiB。公开历史默认返回最近1000条，可用 `limit` 调整至最多5000条。

```powershell
.\scripts\test.ps1
# 启动网站后执行真实浏览器检查
Set-Location web
$env:PLAYWRIGHT_CHANNEL = 'chrome'
node node_modules/@playwright/test/cli.js test
```

测试使用本地HTTP夹具，不需要生产Key。真实平台地址和Key配置后，需要再做真实接口兼容性验证。实际已执行的结果及未执行项目见 `docs/VALIDATION.md`。

## 实现参考

本项目独立实现。产品与技术研究参考了 MIT 项目 WizisCool/model-status、prehisle/relay-pulse、yigehaozi/relay-status-monitor、LibreChat。没有将它们的服务端源代码复制到本项目；项目依赖的许可证以各依赖声明为准。
