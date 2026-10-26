# 在线升级

Relay Lab 的管理后台提供“在线升级”页面，用于读取 `70548887/relay-api-monitor` 的 GitHub Release，选择当前系统和架构匹配的安装包，下载并校验 SHA-256，再由启动器排空请求后安装。

## 配置

复制根目录的 `config.example.yaml` 为程序目录下的 `config.yaml`。公开配置只保留占位符，Token 和代理凭据建议使用环境变量：

```powershell
$env:RELAYLAB_UPDATE_GITHUB_TOKEN = '仅授予 Release 读取权限的 Token'
$env:RELAYLAB_UPDATE_PROXY = ''
```

`update.check_interval: 0` 表示只在后台手动检查。`auto_download: true` 只下载和校验，不自动安装；安装动作始终由管理员在后台确认。相对的 `backup_dir` 按配置文件所在目录解析。

不要把 `config.yaml`、Token、代理用户名或代理密码提交到 Git。仓库已经忽略 `config.yaml`、`.env` 和密钥文件。

## 发布

根目录 VERSION 为唯一版本来源。推送与其严格一致的 Tag（当前为 `v1.2.2`）后，`.github/workflows/release.yml` 会：

1. 读取 go.mod 的 Go 工具链基线，使用 Node.js 22 和锁定依赖。
2. 执行 Go 测试、`go vet`、前端构建和类型检查。
3. 生成 Windows amd64、Linux amd64、Linux arm64 的服务端和 launcher。
4. 生成 Windows 完整 ZIP、Linux amd64 / arm64 完整 tar.gz、协议 2 `manifest.json` 和只含文件名的 `SHA256SUMS`。
5. 先创建草稿，上传全部资源并重新下载校验，最后发布正式版；已发布 Release 不可覆写。

Release 包含 6 个裸可执行文件、3 个完整安装包、Manifest 和 SHA256SUMS。完整安装包包含启动器、程序、脚本、示例配置与部署文档，不包含 `data/`、数据库、`master.key`、日志或本地配置。详细发布契约见 [RELEASE.md](RELEASE.md)。

## v1.2.0 / v1.2.1 一次迁移

旧启动器的升级交接不可靠，先停止网站和 Worker，完整备份数据库与 master.key，再手动部署一次 v1.2.2 完整包，同时更新服务端、启动器和脚本。保留原 data 与 config.yaml，启动后确认页面中的启动器协议为 2、原数据仍存在、Worker 在线。此后才使用在线升级；只替换 relaylab.exe 不会修复旧启动器。

默认启动器控制端口是 `127.0.0.1:0`，实际地址由系统分配并记录于数据目录 launcher.json；开发前端仍使用 42784。显式命令行参数优先于环境变量，环境变量优先于 YAML，脚本不传入默认 Address/DataDir 覆盖用户配置。

## 升级状态

后台状态会按 `checking`、`downloading`、`ready`、`draining`、`backing_up`、`starting`、`validating`、`succeeded` 或 `failed` 展示。升级前会停止任务领取并排空当前请求；健康检查失败时启动器恢复旧程序和升级前快照。

升级失败后先查看页面中的错误码和操作历史。Windows 文件被占用、Release 资产不兼容、Manifest 校验失败和数据库版本不兼容都会阻止替换。

检查、下载、安装接口的受理响应不代表最终成功。页面持续轮询，服务重启断线后自动重连；只有目标 commit 的网站与 Worker 健康验证完成，才展示成功并刷新界面。下载要求兼容且有更新，安装还要求 ready 与协议 2 启动器。`auto_restart: false` 显示“等待手动重启”，当前服务继续运行。

Windows 与 Linux amd64 的真实程序升级/恢复由发布 CI 验证；Linux arm64 仅交叉编译，未在 arm64 主机执行运行验证。
