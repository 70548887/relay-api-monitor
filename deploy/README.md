# Linux 部署

完整 tar.gz 解压后保持 `bin/`、`scripts/`、`docs/`、`deploy/` 目录结构。前台运行可执行 `sh scripts/start.sh`，退出时向启动器发送 SIGTERM，由启动器排空并关闭网站与 Worker。

长期运行建议将完整目录部署到 `/opt/relaylab`，创建运行用户 `relaylab` 并赋予该用户 bin 和数据目录写入权限。复制根目录 config.example.yaml 为 config.yaml，配置实际地址和数据目录。可选将 deploy/relaylab.env.example 复制为 `/opt/relaylab/relaylab.env` 填写环境变量，勿提交填写后的文件。

将 `deploy/relaylab-launcher.service` 安装到 `/etc/systemd/system/relaylab-launcher.service` 后执行：

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now relaylab-launcher
sudo systemctl status relaylab-launcher
sudo systemctl stop relaylab-launcher
```

默认控制地址 `127.0.0.1:0` 由系统分配，实际值写入 data/launcher.json，不占用前端开发端口 42784。使用自定义数据路径时同时调整 WorkingDirectory、EnvironmentFile、ExecStart 和 ReadWritePaths；程序更新需要 bin 目录可写。

Linux amd64 的真实升级测试由发布 CI 执行；Linux arm64 目前仅交叉编译，尚未进行 arm64 主机运行验证。
