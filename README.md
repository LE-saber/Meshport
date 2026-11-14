# Meshport

Meshport 是私有通訊平台，包含：

- **伺服器端** `meshportd`：統一的身分、裝置、權限和 API。
- **用戶端**：Windows、Linux 桌面版 `meshport`，以及 Android App。三者共用同一個 Go 核心，提供端到端加密的 P2P／中繼傳訊、檔案傳輸，以及內嵌 frpc。用戶端說明見 [docs/CLIENT.md](docs/CLIENT.md)。

| 目錄 | 內容 |
|---|---|
| `cmd/meshportd`、`internal/{api,hub,store,frp,…}` | 伺服器端 |
| `internal/agent` | 用戶端核心，以及嵌入的 Web UI（`internal/agent/web`） |
| `cmd/meshport` | 桌面端（Windows/Linux，無 CGO） |
| `mobile/meshcore`、`android/` | Android：gomobile 綁定與 Kotlin App |
| `pkg/client`、`pkg/proto` | Go SDK 與 WebSocket 協定 |
| `.github/workflows` | CI，以及打 tag 自動發布伺服器、桌面、Android |

以下是伺服器端的功能：

| 功能 | 內容 |
|---|---|
| P2P 通訊 | 文字與檔案。三種模式：僅 P2P、僅中繼、P2P 優先。伺服器負責 WebRTC 信令、TURN 臨時憑證，以及 store-and-forward 中繼（含回條與可續傳檔案） |
| FRP 管理 | meshportd 擔任 frps plugin，集中授權每個 frpc 和每個代理；本機 frps 由它監管；frpc 設定由伺服器產生下發；可在控制台新增、修改、啟停、刪除映射，或全部停止 |
| 裝置管理 | Ed25519 金鑰註冊（註冊碼或帳密 + 審批）、上線狀態、自動發現（可見範圍：全部／群組／使用者）、群組、遠端指令 |
| 權限系統 | 裝置等級 admin / member / guest；admin 等級裝置可替其他裝置申請等級，經審批後生效，可設時效，到期自動恢復；控制台角色 admin / operator / viewer |
| 安全 | bcrypt + TOTP MFA、暴力破解封鎖、異常登入告警、金鑰輪替、API 權杖與金鑰、允許／拒絕清單、裝置封鎖、全量稽核、Webhook 簽章 |
| 監控與系統 | 每 2 秒的即時資料流、時序資料（1h / 24h / 7d）、P2P 成功率、連線失敗、服務日誌、告警、備份、Webhook、線上更新、設定 |
| 統一 API | 131 個 REST 操作與裝置 WebSocket，附 OpenAPI 3.1 和 `/api/docs` |

文件：

- [用戶端（桌面、Android、建置發布）](docs/CLIENT.md)
- [架構與選型](docs/ARCHITECTURE.md)
- [部署](docs/DEPLOY.md)
- [云端构建与发行](docs/RELEASE.md)
- [服务器管理控制台](docs/CONSOLE.md)
- [API](docs/API.md)
- [openapi.json](docs/openapi.json)

## 快速開始（本機）

```bash
make build
./bin/meshportd init -config ./dev/meshport.yaml -data-dir ./dev/data \
    -public-url http://127.0.0.1:8080 -listen 127.0.0.1:8080
./bin/meshportd serve -config ./dev/meshport.yaml
# 用 init 印出的 admin 密碼登入；API 文件：http://127.0.0.1:8080/api/docs
```

要啟用本機 frps，`init` 時加上 `-frps-public-addr 127.0.0.1 -frps-binary /path/to/frps`。可用 `make frp-bins` 從原始碼建置 frps 和 frpc。

## 測試

```bash
make test   # go vet + 單元測試 + API 整合測試
make race   # race detector
make e2e    # 用真實 frps/frpc v0.72.0 跑端到端測試
```

| 範圍 | 內容 |
|---|---|
| 單元測試 | TOTP（RFC 6238 測試向量）、TURN REST 憑證、Ed25519 簽章、權限規則、frps/frpc 設定產生 |
| 整合測試 | 啟動完整伺服器，以控制台和裝置雙方的身分操作：登入、MFA、暴力破解封鎖；註冊、審批、停用、封鎖；中繼、離線佇列、回條；信令、工作階段、遠端指令；可見範圍；等級申請、核准、撤銷、到期；FRP 映射與 plugin 授權；ACL；可續傳檔案；Webhook 簽章；備份；設定；OpenAPI |
| 端到端測試 | meshportd 監管真實 frps，frpc 使用伺服器下發的設定。驗證 TCP/HTTP 轉發、未登記的代理被拒、控制台停止後 frps 端強制下線（用戶端不重載也會生效）、重新啟動、frps 重啟 |

用戶端核心另有測試：加解密、本機 API 權限，以及在程序內啟動真實伺服器、讓兩個 agent 互傳 P2P 與中繼的文字和檔案。設定 `MESHPORT_FRPS` 時，還會用內嵌 frpc 搭配真實 frps 轉發流量。詳見 [docs/CLIENT.md §11](docs/CLIENT.md)。

## 發布

推送 `v*` tag 後，GitHub Actions 會建置並發布以下檔案（見 [docs/CLIENT.md §10](docs/CLIENT.md)）：

| 種類 | 檔名 |
|---|---|
| 伺服器 | `meshport-server-<v>-linux-{amd64,arm64}.tar.gz` |
| 桌面 | `meshport-desktop-<v>-{linux,windows}-{amd64,arm64}` |
| Android | `meshport-android-<v>-*.apk` |

```bash
git tag v1.1.0 && git push origin v1.1.0
```

## 目前狀態與下一步

1. **控制台前端**：`web/console` 保留原設計 Demo 的八頁佈局，已接入真實 API、Cookie 登入和 `/admin/stream` 即時資料。`make console` 產生入口頁；服務端發行包包含控制台，安裝程式會設定 `web_dir`。詳見 [控制台說明](docs/CONSOLE.md)。
2. **用戶端實機驗證**：以下都還沒在實機上測過，請在發布前實際試用：
   - Windows 與 Linux 桌面的視窗、系統匣、通知。
   - Android App。
   - 跨 NAT 打洞與 TURN。
