# 仅通过 SSH 访问的部署

以下为 Ubuntu 24.04 / amd64 的私有访问部署方案。实际主机名、SSH 账号和凭据应保存在本机配置中，不提交到公共仓库。

| 项目 | 地址或位置 |
|---|---|
| 管理入口、REST API、设备 WebSocket | 服务器 `127.0.0.1:18080` |
| frps 内部授权插件 | 服务器 `127.0.0.1:17401` |
| Meshport 独立 frps | `<服务器地址>:7001` |
| Meshport frps dashboard | 服务器 `127.0.0.1:7501` |
| 配置 | `/etc/meshport/meshport.yaml` |
| 数据 | `/var/lib/meshport` |
| 程序 | `/opt/meshport/bin` |
| 服务入口页面 | `/opt/meshport/web/index.html` |

不配置公开的 HTTP 反向代理，也不开放管理端口。服务器原有的 frps 继续使用 `7000`，与 Meshport 的 frps 分开运行。设备 API 与管理页面共用监听地址，因此客户端接入也需要 SSH 隧道或其他私有通道。

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

服务器入口使用已接入真实 API 的管理控制台。先登录再管理设备、FRP、权限、安全和系统；`/api/docs` 保留完整 API 文档。页面布局、主题和语言选项沿用原设计 Demo，显示的记录与指标来自服务器。

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
