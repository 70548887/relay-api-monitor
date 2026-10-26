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

推送形如 `v1.2.0` 的 Tag 后，`.github/workflows/release.yml` 会：

1. 使用 Go 1.26.10 和 Node.js 22 构建嵌入式前端。
2. 执行 Go 测试、`go vet`、前端构建和类型检查。
3. 生成 Windows amd64、Linux amd64、Linux arm64 的服务端和 launcher。
4. 生成 `manifest.json` 和 `SHA256SUMS`。
5. 创建 GitHub Release。

Release 资源只包含可执行文件、校验文件和构建元数据，不包含 `data/`、数据库、`master.key`、日志或本地配置。

## 升级状态

后台状态会按 `checking`、`downloading`、`ready`、`draining`、`backing_up`、`starting`、`validating`、`succeeded` 或 `failed` 展示。升级前会停止任务领取并排空当前请求；健康检查失败时启动器恢复旧程序和升级前快照。

升级失败后先查看页面中的错误码和操作历史。Windows 文件被占用、Release 资产不兼容、Manifest 校验失败和数据库版本不兼容都会阻止替换。
