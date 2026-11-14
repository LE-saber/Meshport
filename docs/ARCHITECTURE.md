# Meshport 伺服器端架構

## 1. 現況與範圍

- 後端的資料模型、欄位與動作，是照 Claude Design 畫布上的介面 Demo（手機 6 頁、桌面 5 頁、伺服器控制台 8 頁）整理出來的。控制台已實作在 `internal/console/web` 並接上本 API（見 §7），用戶端見 [CLIENT.md](CLIENT.md)。
- 這一階段完成的是**伺服器端**：
  - 統一身分、裝置、權限、API。
  - 訊息中繼、P2P 信令、TURN 憑證。
  - FRP 集中控管。
  - 安全、監控、系統管理。
- 也附上給桌面與行動用戶端使用的 Go SDK（`pkg/client`、`pkg/proto`）和測試用 CLI（`meshctl`）。

## 2. 元件選型

| 需求 | 採用 | 理由 / 捨棄的方案 |
|---|---|---|
| 伺服器主程式 | **Go 單一執行檔 `meshportd`** | 與 frp 同語言、單檔部署、Ubuntu 用 systemd 管理即可。不需要 Docker 或 K8s。 |
| 資料庫 | **SQLite（modernc，純 Go，WAL）** | 私有平台規模足夠，備份就是 `VACUUM INTO` 一個檔案。不用另外維運 PostgreSQL 或 Redis。日後要換 PostgreSQL，store 層已經集中，好改。 |
| 即時通道 | **WebSocket（coder/websocket）** | 上線狀態、離線佇列、回條、信令、遠端指令共用一條連線。 |
| P2P 傳輸 | **WebRTC DataChannel（用戶端：pion / libwebrtc）** | Android 和桌面都有成熟實作，ICE 能處理大多數 NAT。伺服器只負責轉送信令，不重寫底層協定。 |
| NAT 穿透 / 中繼 | **coturn + TURN REST 臨時憑證** | 業界標準。`meshportd` 用共用密鑰簽發 12 小時有效的帳密，不需要在 coturn 建帳號。 |
| 伺服器中繼（僅中繼 / P2P 失敗回退） | **meshportd 內建 store-and-forward** | 對方離線時暫存，上線後送達，並有已送達／已讀回條。內容一律是用戶端端到端加密後的密文，伺服器看不到明文。 |
| 內網穿透 | **frp（fatedier/frp v0.72）** | 由 meshportd 擔任 **frps server plugin**，每次 frpc 登入、建立代理、心跳都要經過資料庫授權。本機 frps 由 meshportd 監管（設定產生、啟停、崩潰重啟）。frpc 設定由伺服器產生後下發。 |
| 參考但未直接採用 | Tinode / OpenIM | 完整 IM 系統太重，帳號模型也和「裝置等級＋審批」不合。只參考了離線訊息與回條的設計。 |
| | Iroh | QUIC P2P 很好，但 Android 的 Rust 綁定和 coturn 生態比較不成熟。協定層（`signal` 的 payload 不透明）保留了日後換成 Iroh 的可能。 |
| | frp-panel | 它是把 frp 嵌進自己的程式，耦合 frp 的內部 API。這裡改用官方 plugin 協定，frp 升級比較不會壞。 |
| | MeshCentral | 參考了裝置註冊、群組、遠端指令的概念。 |

不使用虛擬網卡：P2P 走 WebRTC DataChannel（應用層），FRP 走 frpc 的應用層代理。兩者都不需要 TUN/TAP，也不需要 root。

## 3. 元件圖

```
              ┌──────────────── Ubuntu 伺服器 ────────────────────────────┐
 瀏覽器控制台 ─┤ HTTPS ─► meshportd                                         │
 (8 頁)       │          ├─ REST /api/v1/...（console / device / token）    │
 桌面/手機 ───┤ WSS  ──► ├─ Hub：presence · relay · signal · telemetry · cmd │
 用戶端        │          ├─ Policy（等級 / 角色 / 可見範圍）                  │
              │          ├─ FRP Manager ── 監管 ─► frps (本機) ◄─ frpc        │
              │          │      ▲ plugin (Login/NewProxy/Ping/...)  127.0.0.1 │
              │          │      └── 遠端 frps 節點經 https://…/internal/…    │
              │          ├─ Monitor（/proc 每 2 秒）· Jobs · Alerts · Webhooks│
              │          └─ SQLite (WAL)  /var/lib/meshport/meshport.db       │
              │  coturn :3478 ◄── WebRTC ICE（TURN 臨時憑證由 meshportd 簽發）│
              └──────────────────────────────────────────────────────────────┘
```

## 4. 三種傳送模式

| 使用者選擇 | 用戶端行為 | 伺服器行為 |
|---|---|---|
| 僅 P2P (`p2p`) | 用 `/rtc/ice-servers` 的 STUN，**拿掉 TURN**，透過 `signal` 交換 SDP/ICE | 只轉送信令。如果 `msg.send` 帶 `mode=p2p`，回 `mode_violation`，保證內容不經伺服器 |
| P2P 優先 (`auto`) | 先試 WebRTC（含 TURN），失敗就改用 `msg.send` | 轉送信令。回退後走 store-and-forward |
| 僅中繼 (`relay`) | 一律走 `msg.send` 和 `/files`。即時串流可用 `iceTransportPolicy=relay` | 暫存、轉送、回條，檔案走可續傳上傳 |

用戶端每 ≤5 秒送一次 `session.report`。伺服器把它當成「通訊管理」頁的資料來源，包含實際路徑、via、延遲、速率、訊息送達數和檔案進度，並據此算出 P2P 成功率和失敗紀錄。

## 5. 權限模型

- **裝置等級**（mesh 上能做什麼）：
  - `admin`：訊息、隧道、管理 frps、替其他裝置申請等級。
  - `member`：訊息、建立自己的 frpc 隧道。
  - `guest`：只能傳訊息和檔案。
- **控制台角色**（人能管什麼）：
  - `admin`：全部。
  - `operator`：日常操作。
  - `viewer`：唯讀。
  - `user`：沒有控制台權限，只能替自己產生 guest 裝置的註冊碼。
- **API 權杖**：scope 是 `read` / `write` / `admin`，分別對應 viewer / operator / admin，而且不會超過擁有者本身的角色。
- **可見範圍** `discovery_scope`：
  - `all`：所有啟用中的裝置互相可見。
  - `group`：只看得到同群組的裝置。
  - `user`：只看得到同一位使用者的裝置。
  - 不論哪種範圍，admin 等級的裝置都看得到所有裝置，也被所有裝置看得到。
- **等級申請**：
  - admin 等級的裝置可以替其他裝置申請等級，但不能超過自己的等級。
  - 審批者核准後才會生效，可設時效（24h / 7d / 30d / 90d / 永久）。
  - 到期或被撤銷時，恢復到申請前的等級；新的核准會取代舊的。
  - 等級一變動，就即時推送給裝置，並重新檢查 FRP 權限。
- **FRP 強制執行**：
  - frpc 的登入、每個代理、每次心跳，都經過 plugin 比對資料庫：裝置狀態、等級、映射是否存在、是否啟用，以及埠、網域、密鑰是否一致。
  - 停止或刪除映射、降級、停用裝置時，會拒絕該裝置的下一次心跳。frpc 因此重連，重連後只有仍被允許的代理能註冊。
  - 這套機制已經用真實的 frps/frpc 端到端測試驗證過（`TestFRPEndToEnd`）。

## 6. 安全

- 裝置身分用 **Ed25519 金鑰對**，私鑰不離開裝置。
  - 註冊時對 `meshport-register:<pubkey>:<ts>` 簽章。
  - 登入走挑戰／回應：`meshport-login:<id>:<nonce>`，nonce 只能用一次，2 分鐘內有效。
  - 輪替金鑰時，新舊兩把金鑰都要簽章。
- 端到端加密：裝置上傳 X25519 `box_pubkey`，由 `/peers` 發給其他裝置。伺服器只存放與轉送密文。
- 控制台登入：
  - bcrypt 加上可選的 TOTP；可以強制所有控制台使用者啟用 MFA。
  - 同一 IP 10 分鐘內失敗 5 次（可設定），封鎖 15 分鐘並發出告警。
  - 從新的位址成功登入時，標記為異常。
- 權杖：
  - 所有權杖只存 SHA-256 雜湊。
  - Cookie session 使用 `SameSite=Strict`，加上 `X-Requested-With` 防 CSRF。
- 允許／拒絕清單：
  - deny 封鎖所有流量，包含 API、裝置、frpc 登入，以及連到已發布服務的訪客（NewUserConn）。
  - allow 只限制控制台與 API 權杖的來源，不影響手機等裝置。
- 稽核：每一個變更動作都寫入 `audit_log`，並推送到 Webhook。Webhook 以 HMAC-SHA256 簽章。
- 線上更新：manifest 要用 Ed25519 簽章，並比對 SHA-256。安裝前會自動備份，裝完由 systemd 重啟。

## 7. 控制台 8 頁與 API 對照

控制台在 `internal/console/web`：無框架、無外部相依的單頁應用，以 `go:embed` 編進 meshportd，在 `/` 提供（`web_dir` 可改用其他建置或設為 `off` 停用）。

介面沿用已定稿的控制台設計稿（Claude Design 上的 `Admin.dc.html`），不另外重畫：

| 檔案 | 內容 |
|---|---|
| `admin.tpl.html` | 設計稿的版面標記原樣保留；只把示範數值換成資料綁定，並補上真實 API 需要的少數按鈕與通用表單視窗 |
| `theme.js` | 設計稿的配色、5 種主題色、圖示與繁中／日文／英文字串，逐字複製 |
| `strings.js` | 設計稿沒有的介面字串（登入、表單、設定），同樣有三種語言 |
| `dc.js` | 約 200 行的模板執行器：`{{綁定}}`、`<sc-for>`、`<sc-if>`，以差異更新 DOM，所以每 2 秒的即時刷新不會打斷輸入 |
| `app.js` | 對應 API、即時串流與各項操作，產生模板需要的資料 |

模板裡的樣式寫在 `data-s`、事件寫在 `data-on-*`，由 `dc.js` 透過 CSSOM 套用，因此仍符合禁止 inline style／script 的 CSP。所有主控台檔案都以 `Cache-Control: no-cache` 加內容 ETag 回應，升級後不會出現新模板配舊腳本。

- 登入後以 HttpOnly、SameSite=Strict 的 session cookie 呼叫 `/api/v1`，所有寫入都帶 `X-Requested-With`。
- 回應標頭有嚴格的 CSP（只允許同源腳本與樣式）、`X-Frame-Options: DENY`；伺服器回來的文字一律以文字節點插入，不會被當成 HTML。
- 即時資料走 `WS /admin/stream`：`metrics.tick` 直接更新數字與圖表，其他事件會觸發目前頁面重新載入。
- 依角色顯示操作：檢視者唯讀，操作員可以操作裝置、映射、審批，管理員才看得到節點、使用者、ACL、系統設定等。
- 要求 MFA 時，未啟用的使用者登入後會先進入設定流程。註冊連結 `/enroll#<註冊碼>` 是不需登入的說明頁。


| 頁面 | 主要端點 |
|---|---|
| 概要 | `GET /admin/overview`，`WS /admin/stream`（`metrics.tick` 每 2 秒） |
| 裝置管理 | `/admin/devices`（篩選、詳情、PATCH、approve / disable / ban / delete / command）、`/admin/groups`、`/admin/enroll-tokens` |
| 通訊管理 | `/admin/sessions`、`POST /admin/sessions/{id}/disconnect` |
| FRP 管理 | `/admin/frp/nodes`、`/admin/frp/clients`、`/admin/frp/mappings`（CRUD、start / stop）、`/admin/frp/stop-all` |
| 權限與審批 | `/admin/grants`（approve / reject / revoke / 直接授予）、`/admin/levels`、`/admin/users` |
| 安全中心 | `/admin/security/logins`、`keys`、`mfa`、`tokens`、`acl`、`bans`、`/admin/alerts` |
| 監控與日誌 | `/admin/metrics?range=`、`/admin/metrics/p2p`、`/admin/failures`、`/admin/logs`、`/admin/audit` |
| 系統與 API | `/admin/system/status`、`versions`、`db`、`backups`、`webhooks`、`api-keys`、`update`、`settings`、`endpoints`、`/api/docs` |

手機和桌面用戶端的「訊息 / 隧道 / 伺服器 / 裝置 / 設定」對應 `/peers`、`/ws`、`/messages`、`/files`、`/rtc/ice-servers`、`/me/mappings`、`/me/frpc`、`/frp/nodes`、`/me/grants`、`/grants`。「伺服器」分頁中 admin 等級裝置可以做的停止或恢復操作，直接使用 `/admin/frp/*`（tag 為 frp、非 admin-only 的端點允許 admin 等級裝置呼叫）。

## 8. 程式結構

```
cmd/meshportd     伺服器（init / serve / admin / backup / openapi）
cmd/meshctl       裝置端測試 CLI
internal/config   YAML 設定
internal/store    SQLite schema、migration、模型與查詢
internal/auth     密碼、TOTP、權杖、Ed25519、TURN 憑證、Webhook 簽章
internal/policy   所有權限判斷（API、Hub、frps plugin 共用）
internal/hub      裝置 WebSocket：presence、relay、signal、telemetry、cmd
internal/frp      frps plugin、frpc/frps 設定產生、儀表板輪詢、行程監管
internal/monitor  /proc 取樣、時序資料（記憶體 1h，SQLite 每分鐘平均 8 天）
internal/events   事件匯流排、日誌接收器
internal/api      REST 路由、控制台與裝置處理器、背景工作、OpenAPI
pkg/proto         WebSocket 協定與簽章訊息格式（給用戶端用）
pkg/client        Go SDK（桌面端可直接用；Android 可用 gomobile）
deploy/           install.sh、systemd、coturn、Caddy
```
