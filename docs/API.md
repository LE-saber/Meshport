# Meshport API 文件

- 完整端點清單：伺服器上的 `GET /api/docs`（HTML），或 `GET /api/v1/openapi.json`（OpenAPI 3.1）。repo 內也有一份 [`docs/openapi.json`](openapi.json)，目前 131 個操作。
- 本文件說明通則、認證流程、WebSocket 協定，以及用戶端整合步驟。

## 1. 通則

- Base URL：`https://<domain>`，所有 REST 路徑都以 `/api/v1` 開頭。
- 請求與回應都是 JSON（UTF-8）。時間格式為 RFC 3339 UTC；尚無值時為 `null`。
- 清單回應的格式是 `{"items": [...]}`，部分端點會另外附上 `counts`、`summary` 等欄位。
- 錯誤格式為 `{"error": {"code": "forbidden", "message": "..."}}`，HTTP 狀態碼含義如下：

| 狀態碼 | 含義 |
|---|---|
| 400 | 參數錯誤 |
| 401 | 未認證 |
| 403 | 無權限 |
| 404 | 找不到 |
| 409 | 狀態衝突 |
| 422 | 語意錯誤 |
| 429 | 暫時封鎖 |

- 認證方式是 `Authorization: Bearer <token>`，權杖前綴代表種類：

| 前綴 | 種類 | 取得方式 | 預設效期 |
|---|---|---|---|
| `mps_` | 控制台 session | `POST /auth/login`，同時設定 HttpOnly cookie | 12h |
| `mpd_` | 裝置權杖 | 裝置挑戰登入 | 24h |
| `mpt_` | 個人 API 權杖 | 安全中心 | 可設定 |
| `mpk_` | 服務 API 金鑰 | 系統與 API | 可設定 |
| `mpe_` | 裝置註冊碼 | 裝置管理 → 註冊新裝置 | 預設 24h |

- 用 cookie 認證的寫入請求必須帶 `X-Requested-With` 標頭（防 CSRF）。
- 瀏覽器的 WebSocket 無法設定標頭，可以改用 `?access_token=`。

## 2. 控制台登入

```http
POST /api/v1/auth/login
{"username":"admin","password":"…","totp":"123456"}
→ 200 {"token":"mps_…","expires_at":"…","user":{…},"mfa_setup_required":false}
```

- 帳號已啟用 MFA 但沒有帶 `totp` 時，回 `401 mfa_required`。
- 系統設定「強制 MFA」後，尚未啟用 MFA 的帳號呼叫 `/admin/*` 會收到 `403 mfa_required`；`/auth/mfa/setup` 和 `/auth/mfa/enable` 仍然可以使用。

## 3. 裝置註冊與登入

裝置產生 Ed25519 金鑰對，私鑰不離開裝置。公鑰和簽章都使用標準 base64 編碼。

**註冊**：`POST /api/v1/devices/register`

```json
{
  "name": "Pixel 9", "os": "Android 15", "kind": "phone",
  "pubkey": "<base64 ed25519 public key>",
  "box_pubkey": "<base64 x25519 key for E2E>",
  "ts": 1794446990,
  "sig": "<sign(priv, \"meshport-register:\" + pubkey + \":\" + ts)>",
  "enroll_token": "mpe_…"
}
```

- 用註冊碼註冊時，會依註冊碼的設定決定是否自動核准，以及給予的等級和群組。
- 也可以不用註冊碼，改帶 `username` + `password`（+ `totp`）自助註冊。前提是系統設定 `open_registration=true`。核准方式依 `registration_approval`：`manual` 時裝置先進入 `pending`。
- 回應 `201 {"device": {...}, "status": "active" | "pending"}`。

**登入**（挑戰／回應）：

```http
POST /api/v1/devices/auth/challenge  {"device_id":"dev_…"}
→ {"nonce":"…","message":"meshport-login:dev_…:<nonce>"}
POST /api/v1/devices/auth/login      {"device_id":"dev_…","nonce":"…","signature":"<sign(priv, message)>"}
→ {"token":"mpd_…","expires_at":"…","device":{…},"key_rotate_required":false,"capabilities":["message","file",…]}
```

- 裝置還在 `pending` 時回 `403 device_pending`，被停用或封鎖時回 `403 device_disabled` / `403 device_banned`。
- 權杖過期後重新做一次挑戰登入即可。

**輪替金鑰**：`POST /api/v1/me/device/rotate-key {new_pubkey, old_sig, new_sig}`。新舊兩把金鑰都要對 `meshport-rotate:<device_id>:<new_pubkey>` 簽章。管理員也可以要求裝置輪替：伺服器會推送 `cmd` 指令，名稱為 `rotate-key`。

Go 用戶端可直接使用 `pkg/client`：

```go
c := client.New("https://mesh.example.com")
c.Register(ctx, client.RegisterRequest{Name: "MacBook", EnrollToken: tok})
c.Login(ctx)
conn, _ := c.Connect(ctx)          // WebSocket
conn.SendMessage(ctx, peerID, proto.ModeAuto, ciphertext)
```

## 4. 裝置 WebSocket（`GET /api/v1/ws`）

### 4.1 框架格式

- 每個框架的格式是 `{"t": type, "id": 可選請求 ID, "d": payload}`。
- 帶 `id` 的請求，伺服器會回一個同 `id` 的 `ack` 或 `error`（`{"code","message"}`）。
- 伺服器每 25 秒送一次 WebSocket ping。用戶端也可以送 `ping` 框架。

### 4.2 伺服器送出

| t | payload | 說明 |
|---|---|---|
| `hello` | `{device_id, did, level, server_time, queued, heartbeat_s, caps}` | 連線後的第一個框架 |
| `msg.new` | `{id, from, to, mode, ct, body, ts}` | 中繼訊息。連線時會先補送所有離線暫存 |
| `msg.receipt` | `{ids, kind: delivered\|read, by}` | 對方的回條 |
| `presence` | `{device_id, online, last_seen}` | 可見裝置上線或下線 |
| `signal` | `{from, sid, kind, mode, data}` | P2P 信令 |
| `file.offer` | `{file_id, from, name, size, sha256}` | 中繼檔案已可下載 |
| `cmd` | `{cmd_id, name, args}` | 遠端管理指令：`reload-frpc`、`rotate-key`、`refresh-peers`、`disconnect`、`collect-logs`、`restart-client`、`ping` |
| `frp.config` | `{version}` | 映射有變更，請重新抓 `/me/frpc` 並熱重載 frpc |
| `level.changed` | `{level, reason}` | 等級變更 |
| `grant.update` | `{grant_id, status, target, level}` | 等級申請狀態變更 |
| `session.close` | `{sid, reason}` | 管理員中斷了這個工作階段 |
| `kick` | `{reason}` | 裝置被停用、封鎖、刪除，或被新連線取代，連線隨後關閉 |

### 4.3 裝置送出

| t | payload | 說明 |
|---|---|---|
| `msg.send` | `{to, mode: relay\|auto, cid, ct, body, sid?}` | 回 `ack {cid, id, status: relayed\|queued}`。`body` 是 base64 的端到端密文，最大 256 KiB；更大的內容請走 `/files`。`mode=p2p` 會被拒絕（`mode_violation`） |
| `msg.delivered` / `msg.read` | `{ids}` | 收件端回報 |
| `signal` | `{to, sid, kind: offer\|answer\|candidate\|bye\|…, mode, data}` | 轉送給對方。對方離線時回 `peer_offline` |
| `session.report` | `{sid, peer, mode, path, via, state, lat_ms, rate_bps, sent, delivered, read, failed, file?, reason?}` | 傳輸狀態有變化時送出，活躍期間每 ≤5 秒送一次 |
| `presence.set` | `{lan_ip, nat_type}` | 提供 LAN 位址與 NAT 類型提示 |
| `cmd.result` | `{cmd_id, ok, output}` | 回覆遠端指令 |

- `path` 的值：`p2p` / `relay` / `nego` / `fail` / `idle`。
- `state` 的值：`up` / `nego` / `fail` / `idle` / `closed`。

## 5. P2P 流程（WebRTC）

1. 取得 ICE 伺服器：`GET /rtc/ice-servers` 會回傳 STUN 位址，以及帶 12 小時臨時帳密的 TURN 位址。
2. 依使用者選擇的模式設定 WebRTC：
   - `p2p`：移除 TURN 伺服器。
   - `auto`：保留 TURN。
   - 即時串流若要強制經 TURN，設 `iceTransportPolicy=relay`。
3. A 送出 `signal{kind:"offer", sid, mode, data: SDP}`。B 收到後回 `answer`，雙方再互送 `candidate`。
4. 連線建立後送 `session.report{path:"p2p", state:"up", via:"UDP hole punch · 192.168.1.x"}`。
5. 失敗時：
   - `auto`：送 `session.report{path:"relay"}`，之後改用 `msg.send`。
   - `p2p`：送 `session.report{state:"fail", reason}`，訊息留在本機，直到直連成功。
6. DataChannel 上的訊息和檔案仍需用 `box_pubkey` 做端到端加密，伺服器完全不經手。

## 6. 中繼檔案（可續傳）

```http
POST  /api/v1/files {to, name, size, sha256}        → {id, upload_url, offset:0, chunk_max}
PATCH /api/v1/files/{id}  Upload-Offset: 0          <raw bytes ≤ 16 MiB>  → {offset, complete}
HEAD/GET /api/v1/files/{id}                          → Upload-Offset（斷線續傳時查詢）
GET   /api/v1/files/{id}/content                     （收件端或寄件端，支援 Range）
```

- 傳完後伺服器會驗證 SHA-256，不符時回 `422`，並從 0 重新開始。
- 驗證通過後推送 `file.offer` 給收件端。
- 檔案保存 `file_retention_hours`（預設 72 小時），大小上限 `relay_max_file_mb`。

## 7. FRP（用戶端）

- 可用的節點：`GET /frp/nodes`。
- 建立映射：`POST /me/mappings`，需要 member 以上等級。範例：

  ```json
  {"name":"ssh-mac","node":"tpe","proto":"tcp","local_port":22,"remote_port":6022}
  ```

- 各協定需要的欄位：
  - `http` / `https`：`domains` 或 `subdomain`。
  - `stcp` / `xtcp`：`secret_key`（省略時自動產生）。
- 驗證規則：
  - 遠端埠必須在節點的 `allow_ports` 範圍內，同一節點上不可重複。
  - 網域在同一節點上不可重複。
- 回應中的 `public_addr` 就是對外位址，例如 `203.0.113.10:6022` 或 `https://media.apps.example.com`。
- `GET /me/frpc`：每個節點一份 `frpc.toml`，內含 `metadatas.device_id` 與 `metadatas.frp_secret`。用戶端以內嵌的 frpc 函式庫或子行程執行即可；收到 `frp.config` 時重新抓取並重載。
- 被停用或刪除的代理不會出現在設定裡。即使用戶端沒有重載，frps 也會在下一次心跳（≤10 秒）時強制它重連，重連後該代理會被拒絕註冊。

## 8. 權限申請（用戶端）

- `POST /grants {target, level, reason, duration}`：只有 admin 等級的裝置能呼叫，申請的等級不能高於自己。
- `GET /me/grants`：查看我送出的申請，以及別人替我送出的申請。
- 狀態變化會用 `grant.update` 推送；核准後目標裝置還會收到 `level.changed`。

## 9. 控制台

各頁對應的端點見 [ARCHITECTURE.md §7](ARCHITECTURE.md)。重點：

- 即時資料：`WS /api/v1/admin/stream?events=metrics.tick,device.*,session.update,alert.*,log`。事件格式是 `{type, time, data}`。
  - `metrics.tick` 每 2 秒一次，內容包含 cpu、mem、disk、net_rx_bps、net_tx_bps、devices_online、sessions_p2p、sessions_relay、p2p_bps、relay_bps、frp_bps、p2p_success。
- 圖表：`GET /admin/metrics?range=live|1h|24h|7d&keys=cpu,mem&points=120`。
- 裝置詳情抽屜：`GET /admin/devices/{id}`，回傳裝置、即時連線、工作階段、映射、申請、最近登入、金鑰年齡。
- 新增映射：`POST /admin/frp/mappings {device, node, proto, …}`，回應內含 `public_addr`，可直接用在表單的即時預覽。
- 遠端指令：`POST /admin/devices/{id}/command {name:"reload-frpc", timeout_s:15}`，會等待裝置的 `cmd.result` 後才回應。
- Webhook：
  - 標頭 `X-Meshport-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>." + body)>`。
  - Body 格式為 `{id, type, time, data}`；失敗會重試 3 次。
- Prometheus：`GET /metrics`，需要帶 read scope 的 API 權杖。

## 10. 錯誤碼

| code | 說明 |
|---|---|
| `unauthorized` / `invalid_credentials` / `invalid_signature` / `invalid_challenge` | 認證失敗 |
| `mfa_required` / `invalid_mfa` | 需要 MFA，或驗證碼錯誤 |
| `too_many_attempts` | 暫時封鎖（429） |
| `device_pending` / `device_disabled` / `device_banned` | 裝置狀態不允許此操作 |
| `forbidden` | 角色、等級或可見範圍不允許 |
| `mode_violation` | 用伺服器中繼送出了僅 P2P 的內容 |
| `relay_disabled` | 管理員關閉了伺服器中繼 |
| `peer_offline` | 信令目標離線 |
| `offset_mismatch` / `checksum_mismatch` | 檔案上傳的位移或雜湊不符 |
| `conflict` | 重複或狀態衝突 |
