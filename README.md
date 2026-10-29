# Relay Lab · 中转观测站

面向中转 API 的可用性监控与模型测试平台，基于 Go、React 和 SQLite 构建，支持自托管部署。

Relay Lab 将 API 健康监控、固定提示词作品对照和在线模型测试整合到同一站点：访客可查看公开监控与作品，普通用户可使用自己的 API Key 测试模型，管理员通过独立后台管理平台、分组、任务和账户。

[快速开始](#快速开始) · [首次配置](#首次配置) · [使用说明](#使用说明) · [文档导航](#文档导航) · [下载安装包](https://github.com/70548887/relay-api-monitor/releases)

仓库版本以 [VERSION](VERSION) 为准。本文部署示例对应 **v1.4.2**，版本变化见 [发布说明](docs/releases/v1.4.2.md) 与 [更新日志](docs/CHANGELOG.md)。

## 核心功能

- **API 健康监控**：按平台和分组展示可用性、首段有效正文耗时、完整耗时、近 7 天成功率及近 24 小时状态；缺失与过期样本单独标识。
- **模型作品对照**：通过固定的中文 HTML、英文 SVG 提示词生成作品，支持定时创作、历史预览与源码下载。界面栏目名为“智商测试”，用于观察输出差异，不提供标准化智力评分。
- **在线模型测试**：用户登录后使用自己的接口地址和 API Key 测试文本或 HTML/SVG，支持保存连接、查询私有历史和预填再次测试。
- **独立管理后台**：集中管理平台、分组、模型、定时任务、账户与系统设置，查看任务状态和运行记录；普通用户与管理员使用独立账户和会话。
- **多协议与自托管**：支持 Chat Completions、Responses、Anthropic Messages，提供 Windows、Linux、Docker Compose 和宝塔部署方式。网站与后台任务进程（Worker）由启动器统一管理。

## 快速开始

根据运行环境选择一种部署方式。完整发布包包含程序、启动器、前端资源、脚本和示例配置，运行时无需安装 Go 或 Node.js。首次安装请选择对应系统和架构的**完整包**，并保留目录结构。

### Docker Compose / 宝塔

以下命令在仓库根目录执行，通过多阶段构建生成镜像：

```sh
git clone --branch v1.4.2 --depth 1 https://github.com/70548887/relay-api-monitor.git
cd relay-api-monitor
docker compose build --pull

# 首次部署：按提示设置管理员密码
docker compose run --rm --no-deps \
  --entrypoint /usr/local/bin/relaylab relaylab \
  init-admin --config /etc/relaylab/config.yaml --username admin

docker compose up -d --no-build
```

默认访问地址为 `http://127.0.0.1:42783`，端口仅绑定宿主机回环地址，数据保存在持久化命名卷中。也可下载 Linux 完整包，在解压目录执行构建、初始化和启动命令；包内 Dockerfile 直接使用预编译程序。

使用宝塔或 Nginx 配置域名 HTTPS 时，在项目的 `.env` 中设置实际对外地址，然后重新创建容器：

```dotenv
RELAYLAB_PUBLIC_URL=https://relay.example.com
```

```sh
docker compose up -d --no-build --force-recreate
```

容器部署通过重建镜像与容器升级，应用内二进制自更新默认关闭。宝塔配置、SSE 反向代理、数据卷及恢复步骤见 [Docker 与宝塔部署指南](docs/DOCKER.md)。

### Windows 完整包

从 [Releases](https://github.com/70548887/relay-api-monitor/releases) 下载 Windows amd64 完整 ZIP，解压后在该目录打开 PowerShell：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\start.ps1
```

启动器会同时启动网站和 Worker。停止时执行：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\stop.ps1
```

### Linux 完整包

下载与服务器架构匹配的 Linux amd64 或 arm64 完整 `tar.gz`，解压后进入程序目录：

```sh
# 首次部署：在服务器终端初始化管理员
./bin/relaylab init-admin --username admin

# 前台运行网站与 Worker
sh scripts/start.sh
```

长期运行可使用仓库提供的 systemd 服务示例，见 [Linux 部署指南](deploy/README.md)。

### 访问与管理员初始化

- 用户端：`http://127.0.0.1:42783/`
- 管理后台：`http://127.0.0.1:42783/admin/login`
- 普通用户注册 / 登录：`/register`、`/login`

项目没有预置管理员密码，密码至少为 **12 字节**。Windows 本机首次访问后台时，会引导至 `/admin/setup` 创建管理员；网页初始化仅接受本机请求。Docker 和远程服务器使用上面的终端命令完成初始化。

## 首次配置

1. **添加平台**：进入管理后台，填写平台名称并按需开启公开展示。
2. **添加分组**：配置 API 地址、API Key、模型发现协议和默认生成协议。不同分组可以使用不同 Key。
3. **同步模型**：获取模型目录，必要时手动补录。目录获取成功仅说明模型列表可用，仍需实际调用验证推理接口。
4. **设置可见性与任务开关**：核对分组的公开、健康监控和中英文创作设置。新分组默认启用并公开，新模型继承分组设置；普通保存与重新同步保留模型的个别关闭项，“保存并应用到全部模型”会统一应用分组设置。
5. **选择定时测试模型**：新建配置采用单模型调度，健康监控与自动创作使用同一个指定模型。默认精确匹配 `gpt-6.1-sol`；目标缺失或归档时暂停相应自动任务，等待选择或后续同步，不自动切换模型。手动任务可另选模型。
6. **确认 Worker 在线**：在运行概览查看 Worker 和任务状态，再执行一次手动检测或创作。网站可以独立运行，但定时任务依赖 Worker。

默认健康检测间隔为 **5 分钟**，作品生成间隔为 **30 分钟**，可在后台调整。中英文模板同时启用时，同一模型按顺序执行。

## 使用说明

### 接口协议与调用行为

- API 地址只填写根站点时，程序默认使用 `/v1`；已有路径会保留。高级设置支持自定义模型列表和生成接口路径。
- 生成协议需明确选择，不根据模型名称推断协议，也不在失败后自动切换接口或降低推理强度。
- 推理强度可按分组设置，模型可继承或覆盖；选择“上游默认”时不发送该参数。具体支持范围由所选协议和上游模型决定。
- 每次测试仅发送本次提示词，不自动加入历史消息、作品或上一轮回复。上游提供缓存用量时会记录，未提供的用量显示“未提供”。
- 分组是本地配置标签。“验证绑定组”仅核验 Token 的绑定信息，不代表模型请求实际命中的账户、路由或余额；不支持该接口时会明确提示。

### 作品预览与历史

内置提示词分别为：

```text
Create code for an SVG of a pelican riding a bicycle as nicely as you can.
```

```text
创建一个HTML，内容是SVG绘一个鹈鹕骑自行车的2D动画
```

作品历史默认按“分组 + 模板”保留最近 **8 份**，可切换为按“模型 + 模板”保留。仅相同提示词与版本的作品适合并排对照。

生成内容在不具备同源权限的 iframe 中预览，主页面不直接插入生成的 HTML，源码以纯文本附件下载。预览会限制外部资源、表单、弹窗和导航，但不保证浏览器级绝对断网。输出截断或代码提取失败时，任务标记失败并保留上一份成功作品。

### 用户连接与私有记录

在线测试需要**普通用户登录**，管理员会话不能代替普通用户会话。用户可在 `/account/connections` 管理连接，在 `/account/tests` 查看记录、下载内容或删除已结束的记录；“再次测试”仅预填参数，不自动发送请求。

保存的 API Key 经 AES-256-GCM 加密，接口仅返回掩码。测试提示词、参数、回复和用量会写入私有历史，默认保留 **30 天**，可配置为 1–365 天。记录不自动发布到作品墙；**本人和管理员可查看测试内容**，管理员可禁用用户连接，但不能查看或使用用户 Key。

### 默认任务参数

- 并发上限：健康检测 4、作品生成 2、每分组后台任务 2；同一模型同类任务最多运行 1 个。
- 在线测试：独立使用全局 8、每 IP 2 的并发额度，可在后台调整。
- 超时：模型发现 20 秒、健康检测 60 秒、新作品配置默认 600 秒。
- 输出上限：健康检测 1,024，新作品配置默认 8,192；作品参数可按模型覆盖。
- 首段有效正文超过 10 秒标记为偏慢；监控样本超过检测间隔的两倍标记为过期。

已发出但结果无法确认的请求不会自动重发。停止本系统的等待不代表上游已经停止计算或计费。

## 运行配置

需要自定义配置时，将 [config.example.yaml](config.example.yaml) 复制为程序目录下的 `config.yaml`，按部署环境调整。配置优先级为：**显式命令行参数 > 环境变量 > YAML**。

常用环境变量：

- `RELAYLAB_ADDR`：服务监听地址，默认 `127.0.0.1:42783`。
- `RELAYLAB_DATA_DIR`：数据目录，默认 `./data`。
- `RELAYLAB_PUBLIC_URL`：浏览器访问的完整 origin，如 `https://relay.example.com`，用于 HTTPS 反代配置。
- `RELAYLAB_UPDATE_GITHUB_TOKEN`：GitHub 更新访问 Token，用于需要认证的访问场景。
- `RELAYLAB_UPDATE_PROXY`：GitHub 更新请求使用的代理，不转发模型请求。
- `RELAYLAB_UPSTREAM_DNS_OVER_HTTPS`：代理 Fake-IP 环境下可选的 DNS-over-HTTPS 解析地址。

上游请求仅允许公共 HTTP/HTTPS 地址，解析结果会校验并固定连接，禁止自动重定向与内网目标。代理使用 Fake-IP 时，可在代理的 `fake-ip-filter` 中排除 API 域名，或配置上述 DoH 回退；返回地址仍须通过公网校验。

本地配置、API Key、Token 和代理凭据不应提交到仓库。Docker 的端口映射、数据卷和环境变量配置以 [Docker 部署指南](docs/DOCKER.md) 为准。

## 数据备份与升级

原生部署默认将数据保存在 `data/`，核心文件为数据库 `relaylab.db` 和加密主密钥 `master.key`。**数据库与主密钥必须一起备份、一起恢复。** 备份文件包含密钥，应按私密数据保管；主密钥丢失时，程序不会生成新密钥覆盖已有配置。

Windows 默认目录的备份与恢复示例：

```powershell
.\scripts\backup.ps1
.\scripts\restore.ps1 -InputPath '.\backups\relaylab-日期.zip' -DataDir '.\data-restored'
```

使用自定义数据目录时，为备份脚本显式传入 `-DataDir`。备份采用 SQLite 一致性快照；恢复要求目标目录为空，并校验数据库与密钥。Docker 使用 [部署指南中的备份与新卷恢复流程](docs/DOCKER.md#备份升级与回滚)。

- **原生部署**：后台“在线升级”支持检查 GitHub Release、下载安装包并校验，再由启动器完成安装。v1.2.2 及更早版本需先按 [升级说明](docs/UPDATES.md) 完整更新启动器和脚本。
- **容器部署**：使用目标版本源码或完整包重建镜像与容器，保留原数据卷。
- **版本回退**：当前数据库为 Schema 3，不能仅替换成只支持 Schema 2 的旧程序；恢复旧快照会回退备份之后的数据。

## 开发与验证

开发环境使用 [go.mod](go.mod) 声明的 Go 工具链（当前为 `go1.26.10`）、Node.js 22；运行发布打包测试还需要 Python 3.11+。

以下命令在源码仓库根目录的 PowerShell 中执行：

```powershell
# 生成接口契约、构建前端、编译服务端和启动器
powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1

# 启动网站与 Worker
powershell -ExecutionPolicy Bypass -File .\scripts\start.ps1

# Go、前端、类型检查及脚本测试
powershell -ExecutionPolicy Bypass -File .\scripts\test.ps1
```

前端开发时另开终端运行：

```powershell
npm --prefix web run dev
```

开发页面监听 `127.0.0.1:42784`，API 代理至 `127.0.0.1:42783`。管理员首次初始化建议从后端同源页面完成。

接口根路径为 `/api/v1`，契约见 [OpenAPI](docs/openapi.json)，前端共享类型位于 [domain.ts](web/src/generated/domain.ts)。修改 Go 接口类型后，运行 `go run ./cmd/specgen` 刷新契约。

测试使用本地 HTTP 夹具，不需要生产 API Key；夹具通过不代表所有上游平台均兼容。已有验证记录和未完成项目见 [验证说明](docs/VALIDATION.md)，版本相关结果见对应发布说明。

## 文档导航

- [Docker 与宝塔部署](docs/DOCKER.md)：镜像构建、HTTPS、SSE 反代、数据卷和容器恢复。
- [Linux 部署](deploy/README.md)：完整包运行和 systemd 服务配置。
- [升级说明](docs/UPDATES.md)：在线升级、旧版本迁移与回滚约束。
- [更新日志](docs/CHANGELOG.md) / [v1.4.2 发布说明](docs/releases/v1.4.2.md)：版本变化、兼容范围和已知限制。
- [发布流程](docs/RELEASE.md)：安装包结构、版本契约和附件校验。
- [验证记录](docs/VALIDATION.md) / [历史接口实测](docs/LIVE_TESTS.md)：测试范围、结果与限制。
- [OpenAPI 契约](docs/openapi.json) / [配置示例](config.example.yaml)：接口与运行配置参考。

## 致谢

本项目独立实现，产品与技术研究参考了 `WizisCool/model-status`、`prehisle/relay-pulse`、`yigehaozi/relay-status-monitor` 和 `LibreChat`。第三方依赖的许可证以各自声明为准。
