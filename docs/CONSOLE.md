# 服务器管理控制台

控制台位于 `internal/console/web`，通过 Go embed 打包进 `meshportd`。原生 JavaScript 调用同源 `/api/v1`，使用 HttpOnly Cookie 登录，并通过 WebSocket 接收实时事件；运行时不需要 CDN 或额外的 Node 服务。

## 功能与授权

八个页面覆盖概要、设备管理、通信管理、FRP、权限审批、安全、监控日志和系统 API。设备、映射、授权、用户、备份、Webhook 和设置来自服务器；无数据时显示空状态。

控制台账号角色和设备等级相互独立。`operator` 或 `admin` 账号可以在“权限与审批 → 直接授予等级”为 active 设备授权。新设备若为 pending，应先在设备详情中核准。设备等级分为 guest、member、admin；member 可发布 FRP 服务，admin 级设备可替其他设备申请等级，最终由控制台操作员审批。

没有 active 设备时，直接授予入口明确提示先注册并核准设备。授权通过后设备会收到 `level.changed`，后端立即重新计算其能力，撤销时恢复先前等级。

通信状态使用 `up/nego/idle/fail/closed`，路径使用 `p2p/relay/nego/fail/idle`。TURN 和服务器中继均属于 relay，可由每行的 via 说明区别；“包括已结束”可查看结束的会话。审计日志支持按操作者、操作者 ID、目标 ID 和目标类型搜索。数据库完整性检查展示接口返回的 integrity 结果。

## 构建与验证

```bash
make console
go test ./internal/api
make release VERSION=1.0.0
```

`make console` 检查内嵌脚本语法并运行前端回归测试。Go API 测试覆盖静态页面、安全头、审计搜索、通信筛选、授权通知及设备能力变化；真实 FRP 端到端测试需配置 `MESHPORT_FRPS` 和 `MESHPORT_FRPC`。

## 部署与升级

配置 `web_dir: ""` 使用内嵌控制台，`web_dir: off` 禁用页面。非空目录仍会优先覆盖内嵌页面，适用于自定义构建。旧版 `web/console` 和生成工具留作历史参考，不再作为发行版默认页面或当前测试对象。

从外置页面升级时，先备份二进制、配置和数据库，再原子替换 `meshportd`，清空旧的 web_dir 并重启。不要重新初始化数据库。回滚时恢复原二进制及配置。

私有部署保持 `listen: 127.0.0.1:18080`，不配置公开 HTTP 反向代理。已有 SSH 一键访问脚本继续可用，详见 [PRIVATE_DEPLOY.md](PRIVATE_DEPLOY.md)。
