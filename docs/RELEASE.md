# 发布规范

版本以根目录 `VERSION` 的 `MAJOR.MINOR.PATCH` 为准：新增向后兼容功能递增 MINOR，缺陷修复递增 PATCH，不兼容的对外接口变更递增 MAJOR。稳定版 Tag 必须严格等于 `v` 加该版本，例如 `v1.4.2`；预发布不可标记为稳定最新版。本次标题为 `Release v1.4.2 - 修复 HTML 依赖漏洞与 Docker 数据恢复`，中文说明位于 `docs/releases/v1.4.2.md`，按新增、优化、修复、升级兼容、验证和已知限制组织。

## 构建与资源

工作流从 `go.mod` 读取 Go 基线，使用 Node.js 22、`npm ci` 和锁定的模块版本。前端测试只运行一次；Go 测试设置明确超时并执行 `go vet`。Windows 与 Linux amd64 的真实二进制升级恢复及容器构建/运行检查通过后才进入发布任务；Linux arm64 为交叉编译产物，未在 arm64 主机验证运行。发布 CI 在 Linux 临时启用 CGO 执行并发 race 测试；发布二进制始终 `CGO_ENABLED=0`。

运行 `python scripts/release.py --tag v1.4.2` 会一次性取得完整 commit 与 UTC 构建时间，并将相同的 `version`、`commit`、`buildTime` 注入网站程序和启动器。`scripts/build.ps1` 同样从 VERSION 读取版本，将元数据保存到 `bin/BUILD_METADATA.json`；本地 `package.ps1` 复用该元数据，防止压缩包宣称另一构建。

稳定 Release 固定包含 11 个附件：Windows amd64、Linux amd64、Linux arm64 各自的服务端与启动器，共 6 个裸可执行文件；Windows 完整 ZIP、两个 Linux 完整 tar.gz；`manifest.json` 和 `SHA256SUMS`。完整包包含 `bin/relaylab(.exe)`、`bin/relaylab-launcher(.exe)`、启动停止及备份脚本、VERSION、示例 YAML、README、升级/发布文档、部署示例和第三方声明。Linux 通过 `scripts/start.sh` 前台启动，长期运行使用包内 systemd 示例。Linux 完整包还包含可直接利用 `bin/` 的运行时 Dockerfile、Compose 和容器配置；源码仓库的根 Dockerfile 使用多阶段源码构建。两套配方不混用，完整包不要求缺失的 Go/React 源码。Windows 包附部署文档，运行 Docker 应使用 Linux 对应架构包。

Manifest 必须包含 `version`、`commit`、`build_time`、`launcher_protocol: 2`、`usage_guard: 1`、`database_schema: 3`、`min_database_schema: 1`，以及 9 个程序/压缩包的 `name/os/arch/size/sha256`。首次启动支持 schema 1／2 自动迁移为 3；不允许将已迁移的数据交给仅支持 schema 1／2 的旧程序。SHA256SUMS 覆盖这 9 个文件及 Manifest，路径只使用文件名；下载后在同一目录执行 `sha256sum -c SHA256SUMS`。校验 SHA-256 可以发现损坏，不替代 Release 来源认证。

`usage_guard: 1` 表示服务端与启动器共同使用数据库全生命周期占用锁及子进程登记。协议保持 2；新增字段也会使旧版严格 Manifest 解析器拒绝安装，防止只更新应用后继续使用旧启动器。v1.2.2 及更早版本迁移到本版必须先部署一次包含两份程序的完整安装包。

发布和源码打包都使用允许列表，不包含运行数据、数据库、master.key、日志、真实 config.yaml、填写后的环境文件或凭据。源码包额外包含 `.github` 工作流与安全 deploy 示例。

## 草稿发布与重试

`scripts/publish_release.py --tag v1.4.2` 要求 Tag 已存在且与 VERSION 一致。脚本使用中文 `--notes-file` 创建草稿，上传全部附件，再重新下载验证文件名、大小和 SHA-256，最后才发布并设为 latest。失败时草稿保留，不会以不完整附件成为最新正式版。

同一 Tag 的草稿可重试，允许替换草稿附件。已经发布的 Release 与 Tag 均不可覆写；修复必须递增 VERSION 并使用新 Tag。当前工作区修改工作流不改变旧 Tag 指向的旧工作流。

## 本地验证

```powershell
python -m unittest discover -s scripts -p test_release.py -v
powershell -ExecutionPolicy Bypass -File .\scripts\test-packaging.ps1
$env:RELAYLAB_UPGRADE_INTEGRATION='1'
go test ./cmd/relaylab -run TestActualBinaryUpgradeAndRecovery -count=1 -timeout=360s
```
