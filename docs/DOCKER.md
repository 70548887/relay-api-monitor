# Docker 与宝塔部署

支持 Linux amd64、Linux arm64 的 Docker 部署。一个容器内由 `relaylab-launcher` 管理网站和 Worker，包含编译后的前端；运行时不需要安装 Go 或 Node.js。

容器默认以 UID/GID `10001:10001` 运行，网站监听容器内 `0.0.0.0:42783`。Compose 将宿主端口默认绑定到 `127.0.0.1:42783`，适合由同一服务器上的宝塔 Nginx 反向代理。数据、加密主密钥、任务及启动器状态统一保存到命名卷 `relaylab_relaylab-data`。

## 选择安装方式

### 使用 Linux 完整发布包

从 GitHub Release 下载与服务器架构相符的完整包：

- x86_64 服务器：`relaylab-linux-amd64-1.4.1.tar.gz`。
- aarch64 服务器：`relaylab-linux-arm64-1.4.1.tar.gz`。

解压到独立目录，例如 `/www/wwwroot/relaylab`。进入包含 `compose.yaml`、`Dockerfile` 和 `bin/` 的目录，保留完整目录结构。Linux 完整包的 Dockerfile 直接使用已发布的二进制，不重新编译源码；不要使用 Windows 包构建 Linux 镜像。

```sh
cd /www/wwwroot/relaylab
docker compose build --pull
```

### 从源码构建

切换到正式版本对应的 Tag，再在仓库根目录构建。根 Dockerfile 会在临时构建阶段安装前端依赖、构建 React，并按 `go.mod` 指定的工具链构建 Go 主程序和启动器。

```sh
git clone --branch v1.4.1 --depth 1 https://github.com/70548887/relay-api-monitor.git
cd relay-api-monitor
docker compose build --pull
```

两种方式默认都生成本机镜像 `relay-api-monitor:1.4.1`。本版没有要求从 Docker Hub 或 GHCR 拉取同名预制镜像；下载完整包后在宝塔服务器上构建即可。

## 初始化管理员并启动

首次部署应先在服务器终端执行初始化，再启动服务。命令会交互读取密码，不将密码写入 Compose、镜像或命令参数；请使用至少 12 字节的密码。

```sh
docker compose run --rm --no-deps \
  --entrypoint /usr/local/bin/relaylab relaylab \
  init-admin --config /etc/relaylab/config.yaml --username admin

docker compose up -d --no-build
docker compose ps
docker compose logs --tail 100 relaylab
```

没有固定默认密码。初始化必须在首次部署或明确维护时进行，不作为修改已有管理员密码的方式。网站启动后从 `/admin/login` 登录。Docker 桥接网络转发到容器的请求不一定被应用识别为本机请求，因此首次初始化采用上述终端命令，不依赖公网 `/admin/setup` 页面。

容器启动后应为 `healthy`，后台运行概览应显示 Worker 在线。没有启用真实上游模型前，启动本身不会自动产生模型调用。

## 宝塔 Docker 配置

1. 在宝塔安装 Docker 管理器，确认服务器支持 `docker compose`。
2. 上传并完整解压相应 Linux 发布包，或通过终端拉取正式 Tag 源码。
3. 在该项目目录执行 `docker compose build --pull` 和首次管理员初始化命令。
4. 在宝塔的 Compose/编排功能中选择这个项目目录的 `compose.yaml`，启动项目。编排的工作目录必须与 Dockerfile、`bin/` 或源码所在目录一致；不要只粘贴 YAML 而遗漏构建目录。
5. 网站或反向代理目标设为 `http://127.0.0.1:42783`，启用域名和 HTTPS。
6. 配置对外地址后重新创建容器，再登录管理后台填写上游接口与密钥。

宝塔不同版本的菜单名称可能不同，实际执行的仍是本目录的 Compose 配置。若使用的宝塔编排界面不能指定构建目录，直接在宝塔终端运行上述 Compose 命令即可。

### HTTPS 域名与 CSRF 校验

在项目目录建立私有 `.env`，填写浏览器实际访问的完整 origin，不包含路径、查询参数或末尾业务路由：

```dotenv
RELAYLAB_PUBLIC_URL=https://relay.example.com
```

有非默认对外端口时也要填写，例如 `https://relay.example.com:8443`。该值用于匹配原始浏览器 Origin，并使会话 Cookie 使用 Secure 标记；应用不靠任意 `X-Forwarded-Proto` 请求头决定可信对外地址。

```sh
docker compose up -d --no-build --force-recreate
```

Nginx 反代配置示例，在宝塔网站对应的 location 中使用：

```nginx
location / {
    proxy_pass http://127.0.0.1:42783;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_request_buffering off;
    proxy_cache off;
    proxy_read_timeout 660s;
    proxy_send_timeout 660s;
}
```

保留原始 `Origin`，不要把它清空或重写以绕过校验。`Host $http_host` 保留实际域名及端口。SSE 流式输出需要关闭反代缓冲，Nginx 的等待时间应覆盖最长 600 秒生成请求。

默认端口仅向宿主回环地址开放，不需要把 42783 暴露到公网。若宝塔 Nginx 自身也运行在其他容器内，`127.0.0.1` 指向的是该 Nginx 容器；应把两个服务加入同一个专用 Docker 网络，并使用 `http://relaylab:42783` 作为代理目标，而不是改写 Origin。

应用默认不信任转发头中的客户端 IP，因此经反代访问的用户可能共用代理来源 IP 的请求额度。多人使用在线测试时，可按实际需求在后台调整额度，并在 Nginx 按真实客户端 IP 设置独立限流；不要为了绕过额度而信任任意来源的转发头。

### 可选端口和 DNS 配置

```dotenv
RELAYLAB_PUBLIC_URL=https://relay.example.com
RELAYLAB_PORT=42783
# 仅在需要其他本机网卡访问时调整；默认 127.0.0.1。
RELAYLAB_BIND_IP=127.0.0.1
# 可选：为上游 API 域名解析配置受信的 DoH 地址。
RELAYLAB_UPSTREAM_DNS_OVER_HTTPS=
```

`.env` 只保存本机部署设置，不进入发布包。网站内保存的上游 Key 位于加密数据库中，`master.key` 必须与数据库一同保留。

## 数据目录与权限

Compose 默认使用命名卷，初始目录由镜像以 UID 10001 创建，无需给宿主整个项目目录开放写权限。容器中：

```text
/var/lib/relaylab/data/       数据库、master.key、运行状态
/var/lib/relaylab/backups/    一致性备份 ZIP
/etc/relaylab/config.yaml    镜像内固定配置，只读
```

正式配置文件不能放进尚未初始化的数据目录，否则新数据目录校验会拒绝启动。根文件系统是只读的，只有持久卷和 `/tmp` 可写。

需要改成宿主目录挂载时，先创建专属目录并赋予容器用户写权限，再将 Compose 中的卷替换为 `/opt/relaylab-runtime:/var/lib/relaylab`：

```sh
sudo install -d -m 700 -o 10001 -g 10001 /opt/relaylab-runtime
```

不要让多个独立容器同时管理同一个数据卷。启动器只在容器内部回环地址开放控制接口，控制 Token 不需要映射或填入宝塔界面。

## 备份、升级与回滚

容器部署默认关闭应用内二进制自更新。升级方式是构建新版本镜像并重新创建容器，数据卷继续沿用。容器中网站与 Worker 必须由同一个 launcher 管理。

在线创建一致性备份：

```sh
docker compose exec relaylab relaylab backup \
  --config /etc/relaylab/config.yaml \
  --output /var/lib/relaylab/backups/before-update.zip
```

将备份另存到服务器其他安全位置：

```sh
docker compose cp relaylab:/var/lib/relaylab/backups/before-update.zip ./before-update.zip
```

源码方式先切换到目标 Tag；完整包方式解压新版本，并保持 Compose 项目名称 `relaylab` 和命名卷名称一致。在 `.env` 更新 `RELAYLAB_VERSION` 后执行：

```sh
docker compose stop
docker compose build --pull
docker compose up -d --no-build
docker compose ps
docker compose logs --tail 100 relaylab
```

`stop_grace_period` 为 12 分钟，给运行中的请求和任务排空时间。`docker compose down` 通常保留命名卷；`docker compose down -v` 会删除业务数据卷，不用于常规升级。

程序回滚应检查数据库 Schema 兼容范围。本版使用 Schema 3；恢复旧备份是单独的数据回退操作，会回退备份之后的数据。

需要恢复备份时，先停止主容器，并把备份放到单独输入目录，示例文件权限须让 UID 10001 可读：

```sh
docker compose stop
docker compose run --rm --no-deps \
  --volume /opt/relaylab-restore:/restore-input:ro \
  --entrypoint /usr/local/bin/relaylab relaylab \
  restore --config /etc/relaylab/config.yaml --input /restore-input/before-update.zip
docker compose up -d --no-build
```

恢复命令会校验目标目录状态、数据库版本和主密钥。先阅读 `docs/UPDATES.md` 的恢复约束，不对正在运行的数据卷直接覆盖数据库文件。

## 验证范围

仓库的 `Docker verification` 工作流分别构建 `linux/amd64` 与 `linux/arm64`；arm64 运行验证使用 QEMU。它会分别检查源码镜像与 Linux 完整包镜像的非 root 启动、网站/Worker 就绪、管理员登录、只读根文件系统、重启持久化和备份恢复，不发送真实上游请求。

本地 Windows 开发机没有 Docker 引擎时，不能把源码编译或 Compose 静态检查称为容器运行验证。正式发布的容器验收结果以对应 GitHub Actions 运行记录为准；QEMU 结果也不替代所有 ARM 实机和宝塔版本的部署验证。
