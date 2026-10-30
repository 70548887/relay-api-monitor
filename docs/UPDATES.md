# 在线升级

Relay Lab 的管理后台提供“在线升级”页面，用于读取 `70548887/relay-api-monitor` 的 GitHub Release，选择当前系统和架构匹配的安装包，下载并校验 SHA-256，再由启动器排空请求后安装。

## 配置

复制根目录的 `config.example.yaml` 为程序目录下的 `config.yaml`。公开配置只保留占位符，Token 和代理凭据建议使用环境变量：

```powershell
$env:RELAYLAB_UPDATE_GITHUB_TOKEN = '仅授予 Release 读取权限的 Token'
$env:RELAYLAB_UPDATE_PROXY = ''
```

默认开启版本检查：启动时检查一次，此后每 6 小时检查一次。后台顶部常驻显示实际运行版本，发现新版本时显示提示并链接到升级页。页面刷新读取服务端状态，不会触发 GitHub 查询。

`update.enabled: false` 关闭检查与升级；`update.check_interval: 0` 表示只在后台手动检查。已有配置中的明确关闭设置和 `0` 间隔继续生效，不会被新默认值覆盖。

`update.install_mode` 支持 `binary`（默认）与 `container`。原生部署可通过启动器安装；Docker 部署检查版本后，通过重建镜像与容器安装更新，应用拒绝下载、安装和回滚。环境变量对应 `RELAYLAB_UPDATE_INSTALL_MODE`。

`auto_download: true` 在原生部署中只下载和校验，不自动安装；安装动作始终由管理员在后台确认，容器模式跳过自动下载。相对的 `backup_dir` 按配置文件所在目录解析。

不要把 `config.yaml`、Token、代理用户名或代理密码提交到 Git。仓库已经忽略 `config.yaml`、`.env` 和密钥文件。

## 发布

根目录 VERSION 为唯一版本来源。v1.4.0 使用 Schema 3，支持从 Schema 1／2 向前迁移。v1.3.1 → v1.3.2 的真实 GitHub 升级和兼容回滚有历史验证；它不代替本版 Schema 3 跨版本验证。推送与 VERSION 严格一致的 Tag 后，`.github/workflows/release.yml` 会：

1. 读取 go.mod 的 Go 工具链基线，使用 Node.js 22 和锁定依赖。
2. 执行 Go 测试、`go vet`、前端构建和类型检查。
3. 生成 Windows amd64、Linux amd64、Linux arm64 的服务端和 launcher。
4. 生成 Windows 完整 ZIP、Linux amd64 / arm64 完整 tar.gz、协议 2、`usage_guard: 1` 的 `manifest.json` 和只含文件名的 `SHA256SUMS`。
5. 先创建草稿，上传全部资源并重新下载校验，最后发布正式版；已发布 Release 不可覆写。

Release 包含 6 个裸可执行文件、3 个完整安装包、Manifest 和 SHA256SUMS。完整安装包包含启动器、程序、脚本、示例配置与部署文档，不包含 `data/`、数据库、`master.key`、日志或本地配置。详细发布契约见 [RELEASE.md](RELEASE.md)。

## 升级到 v1.3.1 的一次完整迁移

v1.2.2 及更早版本均需先停止网站和 Worker，完整备份数据库与 master.key，再手动部署一次 v1.3.1 完整包，同时更新服务端、启动器和脚本。保留原 data 与 config.yaml，启动后确认当前版本为 1.3.1、原数据仍存在、Worker 在线。此后再使用在线升级；只替换 relaylab.exe 不会更新旧启动器。

本版启动器协议仍为 2，Manifest 新增必填 `usage_guard: 1`。旧版严格解析 Manifest 的更新器会拒绝此字段，避免把新服务端安装到缺少占用保护的旧启动器之下。完整安装包中的网站与启动器必须一起部署。占用保护涵盖数据库从打开到关闭的完整生命周期，并保留子进程登记，覆盖进程首次报告前和数据库关闭后仍存活的时段。

默认启动器控制端口是 `127.0.0.1:0`，实际地址由系统分配并记录于数据目录 launcher.json；开发前端仍使用 42784。显式命令行参数优先于环境变量，环境变量优先于 YAML，脚本不传入默认 Address/DataDir 覆盖用户配置。

## 升级状态

后台状态会按 `checking`、`downloading`、`ready`、`draining`、`backing_up`、`starting`、`validating`、`succeeded` 或 `failed` 展示。升级前会停止任务领取并排空当前请求；健康检查失败时启动器恢复旧程序和升级前快照。

升级失败后先查看页面中的错误码和操作历史。Windows 文件被占用、Release 资产不兼容、Manifest 校验失败和数据库版本不兼容都会阻止替换。

检查、下载、安装接口的受理响应不代表最终成功。页面持续轮询状态和同一快照中的最新历史，异步检查或下载完成后无需手动刷新历史。服务重启断线后自动重连；只有目标 commit 的网站与 Worker 健康验证完成，才展示成功并刷新界面。下载要求兼容且有更新，安装还要求 ready 与协议 2 启动器。`auto_restart: false` 显示“等待手动重启”，当前服务继续运行。

正式发布流程包含 Windows 与 Linux amd64 的真实程序升级/恢复验证；本地候选的实际验证结果见对应版本说明，不能据此宣称发布 CI 已通过。Linux arm64 仅交叉编译，未在 arm64 主机执行运行验证。

## 实际网络限制

v1.3.1 及更早更新器的 HTTP 下载总超时为 2 分钟；v1.3.2 的附件下载改为独立 15 分钟上限，元信息仍为 2 分钟。若线路过慢而无法在时限内下载完整程序，操作会明确失败、删除未完成临时文件并保留旧服务；需使用更快的 GitHub 线路或稍后重试。真实验证同时覆盖成功更新和慢网超时，未把受理或部分下载记为安装成功。

## Schema 3 用户私有历史

v1.4.0 的用户保存连接与私有测试历史使用 Schema 3；v1.3.2 及以前的相应正式版为 Schema 2。程序元信息与 Manifest 均为 database_schema=3、min_database_schema=1。升级时保留旧快照；旧程序不支持当前 Schema 时禁止仅回滚二进制。私有连接密文与 master.key 一并备份恢复，恢复期间验证每条用户连接的解密能力。


### 从 v1.3.x 升级到 v1.4.0

已完整部署协议 2、usage_guard 1 的 v1.3.x 实例可在后台检查 v1.4.0；先备份数据库和 master.key，再下载校验并确认安装。升级会自动迁移到 Schema 3，保留原账号、分组、任务和作品。启动器协议保持 2。新安装应选择当前系统与架构对应的完整安装包。

**Schema 3 数据不可仅替换为 v1.3.x 程序回滚。** 更新失败时启动器可恢复本次升级前的程序、数据库与主密钥；升级成功后若明确需要恢复旧快照，须另行停止服务并执行备份恢复，该操作会丢弃快照之后的数据。正常回滚只允许选择支持当前 Schema 的程序。


## v1.4.1 本地候选

本地修复候选保持Schema 3、launcher协议2及usage_guard 1。增加独立流式接收限制、失败指标和定时状态显示；已有计划开启状态和公开开关保留。开启新的作品计划将立即到期，可能产生一次作品调用；重复保存相同配置不额外产生调用。正式发布信息以GitHub Release为准。
