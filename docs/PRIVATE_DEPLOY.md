# 管理入口仅通过 SSH 访问的部署

以下为 Ubuntu 24.04 / amd64 的私有访问部署方案。实际主机名、SSH 账号和凭据应保存在本机配置中，不提交到公共仓库。

| 项目 | 地址或位置 |
|---|---|
| 管理入口与后端监听 | 服务器 `127.0.0.1:18080` |
| 第一版客户端测试入口 | `http://<服务器域名>`，端口 80，仅转发设备接口 |
| frps 内部授权插件 | 服务器 `127.0.0.1:17401` |
| Meshport 独立 frps | `<服务器地址>:7001` |
| Meshport frps dashboard | 服务器 `127.0.0.1:7501` |
| 配置 | `/etc/meshport/meshport.yaml` |
| 数据 | `/var/lib/meshport` |
| 程序 | `/opt/meshport/bin` |
| 服务入口页面 | 内嵌于 `meshportd`，`web_dir: ""` |

后端与管理端口不对公网开放。第一版手机测试采用 `deploy/Caddyfile.client` 的 HTTP 网关：只转发健康检查、注册、设备签名登录、设备 WebSocket、设备自助操作和传输接口，其余路径全部返回 404。控制台静态资源、控制台登录、`/api/v1/admin/*`、内部 FRP 插件和 API 文档均不对公网提供。服务器原有 frps 的 `7000` 与 Meshport 的 `7001` 分开运行。

客户端必须完整填写 `http://<服务器域名>`。只填域名会由当前客户端默认补成 HTTPS；本次 HTTP 测试入口不提供 SSL。注册和登录由 Go 客户端核心发起，Android WebView 仍只访问自己的回环页面，无需放宽 WebView 的网络安全配置。

服务器 `public_url` 设为上述客户端地址，`listen` 仍为 `127.0.0.1:18080`，`tls.mode` 保持 off。管理页面继续使用下面的 SSH 隧道，不能使用公开域名直接打开。

## 设备测试网关

安装官方 Caddy 二进制到 `/opt/meshport/bin/caddy`，将 `deploy/Caddyfile.client` 复制到 `/etc/meshport/Caddyfile.client`，将 `deploy/meshport-gateway.service` 安装到 `/etc/systemd/system/`，并创建由 meshport 用户拥有的 `/var/lib/meshport-gateway`。校验配置后运行：

```bash
/opt/meshport/bin/caddy validate --config /etc/meshport/Caddyfile.client --adapter caddyfile
systemctl daemon-reload
systemctl enable --now meshport-gateway
```

此配置供用户明确选择的明文连接测试使用；后续 HTTPS、证书固定等功能单独升级。不要用转发整个 `/api/v1/*` 的配置替代设备接口白名单。

## Windows 一键访问

先在本机设置 `MESHPORT_SSH_SERVER` 环境变量为实际的 `用户@服务器`，然后双击 `deploy/access/Open-Meshport.cmd`。也可以通过 `-Server` 参数传入。脚本复用本机现有免密 SSH 配置，在后台建立：

```text
浏览器 → 本机 127.0.0.1:18080 → SSH → 服务器 127.0.0.1:18080
```

连通后自动打开 `http://127.0.0.1:18080/`。重复运行会复用隧道；双击 `Close-Meshport.cmd` 关闭由脚本建立的隧道。关闭浏览器不会关闭隧道。

本机端口被其他程序占用时，可以指定其他端口：

```powershell
.\Open-Meshport.ps1 -Server user@your-server -LocalPort 18081
.\Open-Meshport.ps1 -Server user@your-server -LocalPort 18081 -Stop
```

隧道 PID 与日志保存在 `%LOCALAPPDATA%\Meshport\tunnels`，不包含管理密码。脚本不会保存 SSH 密码或私钥。主机密钥发生变化时，SSH 会拒绝连接，需要先核实服务器身份。

也可以直接在终端中运行，保持终端开启：

```powershell
ssh -N -T -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -L 127.0.0.1:18080:127.0.0.1:18080 user@your-server
```

## 页面与管理凭据

服务器入口使用内嵌的管理控制台。先登录再管理设备、FRP、权限、安全和系统；`/api/docs` 保留完整 API 文档。显示的记录与指标来自服务器。旧版若配置了 `web_dir: /opt/meshport/web`，升级时应备份配置并将 web_dir 置空，否则仍会显示旧页面。更新页面需要替换二进制并重启服务。

管理员用户名为 `admin`。首次生成的密码仅保存在服务器 root 可读文件中，可以通过 SSH 读取：

```powershell
ssh user@your-server sudo cat /root/meshport-bootstrap.txt
```

API 登录端点为 `POST /api/v1/auth/login`，请求体包含 `username`、`password` 和可选的 `totp`。登录响应包含会话令牌，也会设置 HttpOnly Cookie。不要将返回令牌或密码提交到仓库。

## 运维

```bash
systemctl status meshportd
journalctl -u meshportd -n 100 --no-pager
systemctl restart meshportd
curl -fsS http://127.0.0.1:18080/healthz
ss -lntp '( sport = :18080 or sport = :17401 or sport = :7501 )'
```

`meshportd` 已配置开机启动，并负责启动 Meshport 自己的 frps。其日志通过 journal 查看。现有数据库和配置应在升级前备份；不要重复初始化覆盖。
