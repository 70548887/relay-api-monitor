# Relay Lab · 中转观测站

Go + React 的中转 API 监控与模型测试平台。用户端公开展示监控与鹈鹕作品，普通用户登录后使用自己的 Key 在线测试；独立管理端配置平台、分组、模型、任务、账户和系统升级。

当前版本 **1.4.2**，唯一版本来源为根目录 `VERSION`。本版修复作品预览所用 HTML 依赖的两项已知漏洞，纠正 Docker 数据恢复流程，并增加依赖漏洞检查。继续支持单模型调度、自适应后台、Docker/Compose、宝塔和 HTTPS 反代部署。新分组默认启用并公开；健康检测与定时创作仅运行指定的一个模型，默认精确匹配 `gpt-6.1-sol`，缺失时等待选择或后续同步。每次测试独立发送当前提示词，不自动加入历史上下文。完整变化、兼容范围及限制见 [v1.4.2 发布说明](docs/releases/v1.4.2.md)。

后台“在线升级”页面可检查并安装 `70548887/relay-api-monitor` 的 GitHub Release。复制 `config.example.yaml` 为 `config.yaml`，通过 `RELAYLAB_UPDATE_GITHUB_TOKEN` 配置私有仓库读取 Token；不要把 Token、代理凭据或本地配置提交到仓库。完整流程见 [docs/UPDATES.md](docs/UPDATES.md)。

## 宝塔与 Docker

支持通过宝塔 Docker 编排部署。源码仓库提供多阶段构建；Linux 完整发布包内提供基于预编译程序的 Dockerfile 和 Compose，无需在服务器安装 Go 或 Node.js。

```sh
docker compose build
docker compose run --rm --no-deps --entrypoint relaylab relaylab init-admin --username admin
docker compose up -d
```

初始化会交互要求管理员密码，没有预置账号密码。默认仅向宿主机 `127.0.0.1:42783` 暴露网站，由宝塔 Nginx 反代；启用域名 HTTPS 时，在编排环境中设置 `RELAYLAB_PUBLIC_URL=https://你的域名`。网站和 Worker 由同一容器内的 launcher 管理，数据保存在持久化命名卷。容器升级使用重建镜像与容器，保留数据卷；应用内二进制自更新在容器中关闭。

完整的源码/完整包选择、宝塔界面配置、HTTPS 与 SSE 反代、数据备份和升级步骤见 [Docker 与宝塔部署说明](docs/DOCKER.md)。
## 本机启动

开发构建需要 `go.mod` 指定的 Go 工具链、Node.js 22 和 Python 3.11+（发布打包测试）。成品启动使用完整包中的 `bin/relaylab.exe` 与 `bin/relaylab-launcher.exe`，无需这些开发工具。

如果使用已经打包的 Windows 成品，直接运行 `scripts/start.ps1`，跳过下面的构建步骤。项目固定使用 Go 1.26.10 修复版本工具链，Go 会按 `go.mod` 自动获取。

```powershell
Set-Location 'D:\中转API监控'
powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\start.ps1
```

用户端入口为 `http://127.0.0.1:42783/`，用户注册 `/register`、登录 `/login`、个人账号 `/account`。管理员从 `http://127.0.0.1:42783/admin/login` 独立登录，首次进入会引导至 `/admin/setup` 创建管理员。用户端不展示管理菜单；后台提供“访问用户端”入口。没有预置密码，密码至少12字节，管理员初始化仅允许本机请求。默认生产端口42783、开发端口42784。

普通用户和管理员使用独立账户表、Cookie 与 CSRF Token。普通用户注册不会获得管理权限；只有管理员能访问 `/admin/users` 中的“普通用户 / 管理员账号”两个列表，并创建、启用、停用或重置密码。停用与重置密码立即撤销相应账户会话；当前管理员不能停用或后台重置自己的密码。数据库自动迁移到 schema 3，保留旧管理员、会话和业务数据。

停止程序：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\stop.ps1
```

`start.ps1` 通过协议 2 启动器管理网站与 Worker，保存进程路径、ID 和启动时间，用于停止时核对身份。v1.2.3 启动器还登记所启动的子进程，并核对覆盖数据库整个打开期间的占用锁，防止尚未报告状态或正在退出的旧进程被遗漏。显式脚本参数优先于环境变量，环境变量优先于 YAML；未指定 `-Address` / `-DataDir` 时不会覆盖配置。启动器控制端口默认由系统分配，实际地址记录在数据目录 `launcher.json`，与开发端口 42784 独立。日志位于数据目录。停止脚本先发送优雅退出请求，退出未确认时保留记录并报错。

**v1.2.2 及更早版本升级到 v1.3.0 时，需手动迁移一次完整包**：停止旧程序，备份数据库与 `master.key`，同时替换服务端、启动器和脚本，保留原数据与本地配置后重新启动。本版保持协议 2，并新增 `usage_guard: 1` 数据库占用保护；只替换服务端不会更新旧启动器。详见 [升级说明](docs/UPDATES.md) 和 [v1.3.0 本地候选说明](docs/releases/v1.3.0.md)；[v1.2.2 发布说明](docs/releases/v1.2.2.md) 保留作为历史记录。

## 配置流程

1. 登录后台，添加平台名称，开启平台公开展示。
2. 在平台下添加分组，填写 API 地址、API Key、模型发现协议和默认生成协议。不同分组可使用不同 Key。
3. 获取模型；模型目录获取成功不等于推理请求已通过。目录失败保留原模型，可手动补录。
4. 在分组编辑中统一配置公开、健康监控与中英文创作开关；新模型默认继承这些开关。可在模型中单独关闭，重新同步或普通保存不会覆盖个别关闭项。需要重置个别设置时，使用“保存并应用到全部模型”。
5. 选择分组的“定时测试模型”，健康监控和自动创作均仅使用它；默认 `gpt-6.1-sol`，缺失时等待指定。自动任务默认监控5分钟一次、作品30分钟一次。手动检测／生成可另行选择模型。
6. 在运行概览确认 Worker 在线。未运行 Worker 时网站仍可访问，但定时任务不执行。

API 地址只填根站点时自动使用 `/v1`；已带路径时保留。高级设置允许指定模型列表和生成接口路径。生成协议支持 Chat Completions、Responses、Anthropic Messages，不根据模型名称猜测协议，也不自动换接口重试。

本产品的分组是本地配置标签；不宣称普通模型调用 Key 能获取平台真实账号分组、余额或全部运营数据。

## 用户端三个栏目

- **中转 API 监控**：按平台和分组展开；展示真实检测结果、首正文耗时、完整耗时、7天成功率与24小时状态色块。缺样本显示空白，超过检测间隔两倍显示过期。
- **智商测试**：主区域直接展示当前HTML/SVG，下面切换八次真实历史缩略图；默认按分组及模板保留8次，可在设置改为按模型。页面每5秒自动刷新，展示排队、生成、字符进度、下一次执行和Worker状态；无成功作品时仍展示状态。仅同一提示词与版本可并排比较，不输出“智商分”。
- **在线测试**：普通用户登录后填写自己的地址和 Key，获取模型并测试文本或动画。页面与两个测试接口均验证普通用户会话，管理员登录不能代替用户登录。用户可保存自己的连接，Key 经 AES-256-GCM 加密保存，接口仅返回掩码。文本、HTML/SVG 测试的提示词、参数、完整或部分回复及实际用量自动写入私有历史，默认保留 30 天；用户仅可查看自己的，管理员可审查全部记录和禁用连接，但不能查看或使用用户 Key。私有记录不自动发布到作品墙。

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

OpenAI 兼容接口提供 `none/minimal/low/medium/high/xhigh/max`，Anthropic 提供 `low/medium/high/xhigh/max`；选项表示协议可传递的参数，具体模型的支持范围由上游决定。不会将界面名称自动转换成另一档位。

后台“平台与分组 → 分组”提供“执行测试”：选择本分组模型和中文 HTML／英文 SVG 模板，提交一次作品任务，可在“任务与作品”查看执行进度与结果。是否在用户端展示仍由平台、分组和模型的公开配置决定。

分组列表的“定时测试”必须选择该分组的一个模型，可选择中文、英文或两个模板并配置创作间隔（60 秒至 7 天）。健康监控也仅对同一个目标运行，其开关及检测间隔独立配置。首次开启需保存配置；关闭创作计划会取消尚未发出的定时作品任务，已发出的请求继续完成，手动任务仍可执行。同步目录不会扩大测试范围，目标缺失或归档时暂停其自动任务，不静默切换模型。修改目标会取消旧目标尚未发送的定时任务，已有历史及独立手动任务保留。旧版显式全部模型计划仅为兼容保留，通过新界面明确选择模型后转为单模型范围。分组计划与对应任务调度在同一事务中保存。同时选择两个模板时，同一模型仍按顺序执行；另一模板保留待执行状态，避免因同一时刻到期而跳过整个周期。

每次模型调用使用独立上下文：只发送本次提示词，不发送历史消息、历史作品、会话 ID 或上一轮回复 ID。新作品参数记录 `context_policy=independent`；历史记录不会用于生成下一份作品。上游缓存与上下文独立是不同概念，不能保证中转平台内部禁用缓存；上游实际提供缓存命中／缓存写入 Token 时记录并展示，未提供时明确显示“未提供”。不会为绕过缓存随机改写标准模板。

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

代理使用 Fake-IP 时，域名可能被解析为 `198.18.0.0/15` 或 `fdfe:dcba:9876::/48` 中的虚拟地址。可以在代理的 `fake-ip-filter` 中排除 API 域名；也可以在本机 `config.yaml` 中设置 `upstream.dns_over_https: "https://1.1.1.1/dns-query"`（或环境变量 `RELAYLAB_UPSTREAM_DNS_OVER_HTTPS`）。回退仅针对全部为上述虚拟地址的域名结果，DNS 服务只接收域名和查询类型，返回地址仍逐一检查并固定连接；解析失败、内网地址及混合公网/内网结果继续拦截。

加密 DNS 对 A／AAAA 并行查询，每类临时故障最多尝试三次；一类连接临时失败时可使用另一类已经校验的公网结果，内网或无效结果仍会使整次解析失败。完整成功结果按 DNS TTL 缓存（最多 5 分钟、256 个域名），不缓存错误或不完整结果。该缓存只保存域名解析地址，不缓存模型回复；模型生成请求不会因 DNS 重试而被重复发送。

GitHub 更新连接提前关闭时会显示网络故障提示；只读 GET 在收到响应前的临时断线最多尝试三次，模型生成不重试。网络需要代理时配置 `update.proxy` 或 `RELAYLAB_UPDATE_PROXY`，例如本机 HTTP 代理 `http://127.0.0.1:7897`；实际端口以本机代理为准。该代理只用于 GitHub 更新请求，不转发模型请求。检查更新失败不会开始安装，也不会关闭网站和 Worker。

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

## 用户连接与私有在线测试记录

`/account/connections` 保存和管理用户连接；`/account/tests` 查询测试历史、下载正文或源码、删除终态记录；详情中的“再次测试”仅预填，不自动发出调用。删除连接会清除该 Key，保留历史及当时接口快照。更换域名、端口或 HTTP/HTTPS 协议时需重新输入 Key。

管理员可在 `/admin/test-records` 查询全体用户的测试记录，在 `/admin/user-connections` 查看掩码和禁用连接。私有历史清理由网站进程每小时执行，与 Worker 是否在线无关，后台设置“私有测试记录保留（天）”允许 1–365 天；清理不删除保存的连接。

本功能使用 Schema 3。首次升级前备份数据库和 `master.key`，旧版仅支持 Schema 2 的程序不能直接回滚运行；恢复旧快照会回退升级后的数据，需另行明确操作。备份与恢复包含用户连接密文并验证其与主密钥及用户归属一致。
