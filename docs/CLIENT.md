# 用戶端（Windows / Linux 桌面、Android）

三個平台共用同一個 Go 核心 `internal/agent`。它負責身分、連線、傳訊、檔案、內嵌 frpc，並在本機提供 Web UI。各平台的殼只負責顯示這個 UI，以及提供通知、開機自啟、開啟檔案等系統功能。

| 平台 | 殼 | 視窗 | 背景執行 |
|---|---|---|---|
| Windows 10/11（amd64、arm64） | `cmd/meshport`（純 Go，無 CGO） | Edge 的 `--app` 視窗（系統內建）；找不到時用預設瀏覽器 | 系統匣圖示；設定中可開啟「登入時自動啟動」（寫入 HKCU Run） |
| Linux（amd64、arm64） | 同上 | Chromium / Chrome / Edge / Brave 的 `--app` 視窗；都沒有時用 `xdg-open` | 系統匣（StatusNotifierItem，KDE 原生支援；GNOME 需要 AppIndicator 擴充）；也可 `--headless` 搭配 systemd user service |
| Android 7.0+（arm64、armv7、x86_64） | `android/`（Kotlin） | WebView | 前景服務（`specialUse`）；可設定開機自動啟動 |

## 1. 架構

```
┌──────────── 用戶端程序 ────────────────────────────────────────┐
│  Web UI（internal/agent/web，嵌入二進位檔）                     │
│     │  HTTP + SSE，只聽 127.0.0.1，需要本機權杖                 │
│  本機 API（localapi.go）                                        │
│     │                                                          │
│  Agent 核心                                                     │
│   ├─ conn.go       登入（Ed25519 挑戰）、WebSocket、重連、遠端指令 │
│   ├─ messaging.go  三種傳送模式、回條、重送                       │
│   ├─ p2p.go        WebRTC（pion）：信令、glare、路徑判斷、回報     │
│   ├─ files.go      P2P 資料通道傳檔、伺服器中繼可續傳上傳/下載      │
│   ├─ e2e.go        NaCl box 訊息加密、XChaCha20-Poly1305 檔案加密  │
│   ├─ frpc.go       內嵌 frp v0.72 用戶端函式庫，熱重載             │
│   └─ store.go      狀態檔、聊天紀錄（JSONL）、傳輸、日誌、事件       │
└────────────────────────────────────────────────────────────────┘
          │ REST / WebSocket（pkg/client）     │ frp 協定
          ▼                                    ▼
      meshportd                              frps 節點
```

Android 透過 gomobile 把 `mobile/meshcore` 編成 AAR，由前景服務呼叫 `Meshcore.start(...)` 取得 UI 網址，再交給 WebView 顯示。

## 2. 註冊與身分

- 第一次啟動時產生兩把金鑰：Ed25519 身分金鑰（登入簽章），以及 X25519 端到端加密金鑰。兩把私鑰都只存在本機的 `state.json`（權限 0600），不會送出。
- 註冊方式有兩種：
  - 管理員產生的註冊碼（`mpe_…`）。
  - 帳號密碼（+ MFA）自助註冊，需要伺服器開放。
- 若需要審批，UI 會顯示「等待核准」和金鑰指紋，並每 15 秒重試一次。核准後自動上線。
- 上線後用 `PATCH /me/device` 回報 E2E 公鑰、版本、作業系統、LAN IP。
- 「輪替身分金鑰」會用新舊兩把金鑰共同簽章。管理員下達 `rotate-key` 指令時也會自動執行。
- 「移除這台裝置的註冊」會刪除本機金鑰與全部聊天紀錄。

## 3. 傳送模式

可在設定中選預設模式，也可以在每個對話的標題列個別指定。

| 模式 | 文字 | 檔案 | 對方離線或無法直連時 |
|---|---|---|---|
| P2P 優先（auto） | 已有直連就走直連，否則最多花 8 秒協商（可經 TURN），失敗改走伺服器中繼 | 同左；P2P 傳送途中失敗會從頭改走中繼 | 伺服器暫存（顯示「伺服器暫存」） |
| 僅 P2P（p2p） | 只用不經 TURN 的直接連線；若現有連線經過 TURN，會重新協商 | 只走資料通道 | 訊息留在本機（「等待中」）。對方上線或直連建立後自動重送，也可手動按「重試」 |
| 僅中繼（relay） | WebSocket 中繼（`msg.send`） | `/files` 可續傳上傳 | 伺服器暫存 |

- 每個 P2P 連線都會向伺服器回報 `session.report`：狀態變化時送一次，連線期間每 5 秒送一次。內容包括路徑、經由、延遲、速率，以及傳輸中的檔案進度。
- 路徑判斷：
  - 雙方都是 host 候選：顯示「LAN direct」。
  - 有 srflx/prflx 候選：顯示「UDP hole punch」。
  - 有 relay 候選：顯示「TURN relay」。這種情況回報的 `path` 是 `relay`。
- 協商衝突（glare）：雙方同時送出 offer 時，裝置 ID 較大的一方保留自己的 offer。
- 連線協商逾時 25 秒，閒置 10 分鐘後自動關閉。

## 4. 端到端加密

- **訊息**：NaCl box（X25519 + XSalsa20-Poly1305），使用寄件者的私鑰和收件者的 `box_pubkey`。box 本身會驗證寄件者金鑰，被竄改或來源不符的訊息會被丟棄。
- **信封內容**：`{v, k: text|file|ack|read|accept|reject, mid, ts, text?, file?, ids?}`。回條和檔案接受通知同樣經過加密。
- **檔案**：每個檔案使用一把隨機 32 位元組金鑰，以 XChaCha20-Poly1305 按 32 KiB 分塊加密。
  - 第 i 塊的 nonce 是 16 位元組前綴加上 8 位元組的 i。
  - 最後一塊用附加資料（AD）標記，因此截斷、重排、竄改都會被發現。
  - 收件端在加密內容之外，還會比對明文的 SHA-256。
  - 檔案金鑰放在加密信封裡。中繼上傳時的檔名是不透明的 `<tid>.bin`。
- 伺服器和 TURN 只會看到密文。

## 5. 檔案傳輸

- **P2P**：
  1. 寄件端在控制通道送出 `file` 信封。
  2. 收件端回 `accept`。
  3. 寄件端開一條 `f:<tid>` 資料通道，送出加密分塊（有背壓控制：緩衝超過 4 MiB 就暫停），最後送 `end`。
  4. 收件端驗證後回 `ack`。
- **中繼**：
  1. 先把檔案加密成暫存檔。
  2. 用 `POST /files` 建立上傳，再以 `PATCH`（每次 4 MiB）上傳，失敗時查詢 `Upload-Offset` 從斷點續傳。
  3. 上傳完成後，經中繼送出 `file` 信封。
  4. 收件端用 Range 請求下載，中斷時從最後一個完整的分塊續傳。下載完成後刪除伺服器上的檔案。
- 收到的檔案存放位置：
  - 桌面：`~/Downloads/Meshport`，可在設定中更改。
  - Android：App 專屬的外部儲存空間。按「儲存」會複製到系統「下載/Meshport」。

## 6. FRP（內嵌 frpc）

- 核心直接使用 frp v0.72 的用戶端函式庫，不需要另外安裝 frpc。每個有啟用映射的 frps 節點各跑一個服務。
- 設定來自 `GET /me/frpc`（伺服器產生）。以下情況會重新抓取：
  - 收到 `frp.config` 推播。
  - 收到 `reload-frpc` 指令。
  - 按下「重新載入」。
- 只有代理清單變了，就用 `UpdateConfigSource` 熱重載。伺服器位址或權杖變了，就重啟該節點的服務。
- 「映射」頁可以新增、編輯、啟停、刪除自己的映射（member 以上等級），並顯示每個代理的即時狀態和對外位址。
- frp 日誌寫在資料夾中的 `frpc.log`。不使用虛擬網卡（VirtualNet 沒有啟用）。

## 7. 本機 API 與 UI 安全

- 只監聽 loopback。桌面預設 `127.0.0.1:47100`，被占用時改用隨機埠；Android 用隨機埠。
- **Host 檢查**：Host 標頭不是 `127.0.0.1`、`localhost` 或 `::1` 時一律回 403，防止 DNS rebinding。
- **本機權杖**：每次安裝產生一個（`ui-token`，0600）。驗證方式：
  - 瀏覽器透過 HttpOnly + SameSite=Strict 的 cookie。
  - CLI 透過 `X-Meshport-Token` 標頭。
- 桌面開啟視窗時使用**一次性登入碼**（2 分鐘內有效），權杖不會出現在行程參數中。
- 寫入請求必須帶 `X-Requested-With`（防 CSRF）。CSP 限制只能載入同源資源。
- 對方傳來的文字一律以 textContent 呈現，不會被當成 HTML 執行。
- `/local/api/*` 只轉送白名單中的裝置 API：`me/mappings`、`frp/nodes`、`me/grants`、`grants`、`me/device`、`files`。

## 8. 桌面端使用

```bash
meshport                       # 系統匣 + 視窗（重複執行只會把現有視窗叫出來）
meshport --minimized           # 只放系統匣（開機自啟用）
meshport --headless            # 無視窗、無系統匣（伺服器）
meshport setup --server https://mesh.example.com --enroll mpe_xxx --name build-01
meshport status
meshport open
```

- 資料夾：
  - Linux：`~/.config/meshport`。
  - Windows：`%APPDATA%\Meshport`。
  - 可用 `--data` 或 `MESHPORT_DATA` 指定。
- 資料夾內容：
  - `state.json`：設定與金鑰。
  - `messages/*.jsonl`：聊天紀錄。
  - `meshport.log`、`frpc.log`：日誌。
  - `ui-token`：本機權杖。
- Linux 發行包附 `install.sh`，安裝到 `~/.local`，不需要 root。
  - `--service` 會一併啟用 headless 的 systemd user service，搭配 `loginctl enable-linger`，登出後仍繼續執行。
- Windows 發行包是免安裝的 zip。在設定中開啟「登入時自動啟動」即可。

## 9. Android

- 第一次開啟時：
  - 要求通知權限（Android 13+）。
  - 詢問一次是否允許略過電池最佳化，避免連線在休眠時被中斷。
- 前景服務通知會顯示連線狀態，並提供「結束」按鈕。關掉畫面不會中斷連線。
- 新訊息通知只在 App 不在前景時顯示。
- 在 WebView 中可以選檔傳送（多選）、把收到的檔案存到「下載」、用其他 App 開啟檔案。訊息中的外部連結會用系統瀏覽器開啟。

## 10. 建置與發布（GitHub Actions）

| Workflow | 觸發 | 內容 |
|---|---|---|
| `ci.yml` | push 到 main、PR | gofmt、go vet（Linux / Windows / Android 目標）、UI 語法檢查、用真實 frps/frpc 跑完整測試（race），以及桌面端交叉編譯 |
| `release.yml` | 推送 `v*` tag，或手動執行（產生草稿 release） | 伺服器 tar.gz、桌面端 tar.gz/zip、Android APK（universal 與各 ABI），附 SHA256SUMS |

發布新版：

```bash
git tag v1.1.0 && git push origin v1.1.0
```

Android 簽章（建議設定，否則每次建置用的是不同的 debug 金鑰，已安裝的 App 無法直接升級）：

1. 產生 keystore（只需一次，請妥善保存）：

   ```bash
   keytool -genkeypair -v -keystore meshport.jks -alias meshport -keyalg RSA -keysize 4096 -validity 10000
   ```

2. 在 GitHub repo 的 Settings → Secrets and variables → Actions 新增四個 secret：

   | Secret | 內容 |
   |---|---|
   | `ANDROID_KEYSTORE_BASE64` | `base64 -w0 meshport.jks` 的輸出 |
   | `ANDROID_KEYSTORE_PASSWORD` | keystore 密碼 |
   | `ANDROID_KEY_ALIAS` | 金鑰別名（上例為 `meshport`） |
   | `ANDROID_KEY_PASSWORD` | 金鑰密碼 |

Windows 執行檔會由 `go-winres` 嵌入圖示和版本資訊（在 CI 中執行）。程式碼簽章沒有包含在流程內，SmartScreen 第一次執行時可能會跳出警告。

本機建置（選用）：

```bash
make desktop VERSION=1.1.0     # release/meshport-desktop-*
make android VERSION=1.1.0     # 需要 Android SDK + NDK、JDK 17、Gradle 8.9+
```

## 11. 測試

`go test ./internal/agent/` 會在程序內啟動真實的 meshportd，並跑兩個 agent 互相通訊：

- 單元測試：
  - 訊息加解密，包含錯誤金鑰與竄改。
  - 檔案分塊加解密，涵蓋各種大小、任意切分、截斷與重排。
  - 聊天紀錄的持久化與去重。
  - 檔名清理。
  - 本機 API 權限：沒有權杖、外部 Host、一次性登入碼重複使用、CSRF、轉送白名單。
- 整合測試：
  - 中繼文字，含送達與已讀回條。
  - WebRTC P2P 文字（含回覆與已讀）。
  - 中繼檔案（300 KB）。
  - P2P 檔案（2 MB）。
  - 僅 P2P 模式在對方離線時保持等待。
- 設定 `MESHPORT_FRPS` 後，還會用內嵌 frpc 搭配真實 frps 轉發 TCP，並驗證停止、重啟映射時的熱重載。

尚未在實機驗證，需要在發布前實際試用：

- Windows 與 Linux 桌面的視窗、系統匣、通知、自動啟動。
- Android App 整體（AAR 與 APK 只在 CI 建置）。
- 跨 NAT 打洞與 TURN 路徑（測試只涵蓋同一台機器的 host 候選）。
- Android 11 以上的網路介面列舉（pion 已內建 anet 相容處理）。
