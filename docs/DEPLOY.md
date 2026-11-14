# 部署（Ubuntu 22.04 / 24.04）

## 1. 準備

- 一台有公網 IP 的 Ubuntu 伺服器，並把一個網域（例如 `mesh.example.com`）指向它。
- 如果要讓 http/https 映射使用子網域，再加一筆泛網域紀錄 `*.apps.example.com`，指向同一台伺服器。
- 在開發機上建置發行包（需要 Go 1.26 以上）：

  ```bash
  make test          # 單元與整合測試
  make e2e           # 用真實 frps/frpc 跑端到端測試（選用）
  make release VERSION=1.0.0
  # → release/meshport-server-1.0.0-linux-amd64.tar.gz（含 bin/、deploy/、docs/）
  ```

## 2. 一鍵安裝

```bash
scp release/meshport-server-1.0.0-linux-amd64.tar.gz root@server:
ssh root@server
tar xzf meshport-server-1.0.0-linux-amd64.tar.gz && cd meshport-server-1.0.0-linux-amd64
./deploy/install.sh --domain mesh.example.com --public-ip 203.0.113.10 \
                    --tls acme --with-turn --subdomain-host apps.example.com
```

安裝腳本依序完成以下工作：

1. 建立系統帳號 `meshport`，以及 `/opt/meshport`、`/etc/meshport`、`/var/lib/meshport` 三個目錄。
2. 從 GitHub 下載 frps，有發布 checksum 時一併驗證。
3. 執行 `meshportd init`，產生設定檔、各項隨機密鑰、第一個管理員（密碼只印一次），以及本機的 frps 節點 `main`。
4. 指定 `--with-turn` 時，安裝並設定 coturn（使用 TURN REST 共用密鑰）。
5. 安裝 systemd 服務 `meshportd` 並啟動。frps 由 meshportd 監管，不需要另外的 unit。

TLS 有兩種選法：

- `--tls acme`：meshportd 自己用 Let's Encrypt 取得憑證，需要開放 80 和 443。
- 預設 `--tls proxy`：meshportd 監聽 `127.0.0.1:8080`，前面放 Caddy 或 nginx。可直接參考 `deploy/Caddyfile`，WebSocket 不需要額外設定。

## 3. 防火牆

| 埠 | 用途 |
|---|---|
| 443/tcp（acme 另需 80/tcp） | 控制台、API、WebSocket |
| 7000/tcp | frps，供 frpc 連線 |
| 8081/tcp、8444/tcp | frps 的 vhost HTTP/HTTPS（可在控制台改成 80/443，但要先把該埠空出來） |
| 20000–29999 tcp/udp | tcp/udp 映射使用的遠端埠（節點的 `allow_ports`） |
| 3478 tcp/udp、5349/tcp、49160–49200/udp | coturn |

```bash
ufw allow 443/tcp && ufw allow 80/tcp && ufw allow 7000/tcp && ufw allow 8081,8444/tcp
ufw allow 20000:29999/tcp && ufw allow 20000:29999/udp
ufw allow 3478 && ufw allow 5349/tcp && ufw allow 49160:49200/udp
```

## 4. 第一次使用

1. 開啟 `https://mesh.example.com/api/docs` 確認服務正常。
2. 用 init 時印出的管理員帳密登入。之後請到安全中心啟用 MFA，並修改密碼。
3. 到「裝置管理 → 註冊新裝置」建立註冊碼，或呼叫 `POST /api/v1/admin/enroll-tokens`。
4. 在任何一台機器上用 `meshctl` 驗證：

   ```bash
   meshctl register -server https://mesh.example.com -name test-box -enroll mpe_xxx
   meshctl peers
   meshctl listen                    # 另開一個終端執行
   meshctl send <device-id> hello
   meshctl api POST /api/v1/me/mappings '{"name":"ssh","node":"main","proto":"tcp","local_port":22,"remote_port":20022}'
   meshctl frpc > frpc.toml && frpc -c frpc.toml
   ```

   member 等級才能建立映射。

## 5. 多台 frps 節點

1. 在控制台新增節點：`POST /admin/frp/nodes`，`managed:false`，`dashboard_addr` 填 meshportd 連得到的位址（例如走內網或 WireGuard 的 `10.8.0.2:7500`）。
2. 回應內附的 `frps_toml` 就是遠端節點要用的 frps 設定。其中 plugin 位址是 `https://mesh.example.com/internal/frps-plugin/<node>/<token>`。
3. 在遠端主機用該設定啟動 frps。官方 release 附的 `frps.service` 可以直接使用。

## 6. 維運

| 項目 | 方式 |
|---|---|
| 日誌 | `journalctl -u meshportd -f`。控制台的「監控與日誌」也能查詢 |
| 備份 | 每天 03:00 UTC 自動執行，保留 14 份，都可在系統設定調整。也可在控制台「立即備份」，或執行 `meshportd backup -config /etc/meshport/meshport.yaml`。備份是單一 SQLite 檔，另附一份設定檔副本 |
| 還原 | `systemctl stop meshportd`，把備份檔覆蓋到 `/var/lib/meshport/meshport.db`，刪除 `meshport.db-wal` 和 `meshport.db-shm`，再 `systemctl start meshportd` |
| 忘記密碼 | `meshportd admin -config /etc/meshport/meshport.yaml reset-password admin` |
| 關閉 MFA | 加上 `disable-mfa admin`（同上指令） |
| 升級 | 換掉 `/opt/meshport/bin/meshportd`，再 `systemctl restart meshportd`。資料庫 migration 會自動執行 |
| 控制台線上升級 | 需在設定裡填 `update.manifest_url` 和 `update.public_key`（Ed25519）。manifest 格式為 `{version, url, sha256, signature=sign(version + ":" + sha256), notes}` |
| 監控 | Prometheus 抓取 `/metrics`，需要帶 read scope 的 API 金鑰 |

## 7. 設定檔重點（`/etc/meshport/meshport.yaml`）

```yaml
listen: 127.0.0.1:8080          # acme 模式為 :443
internal_listen: 127.0.0.1:7401 # 本機 frps 呼叫的 plugin 埠，不要對外開放
public_url: https://mesh.example.com
data_dir: /var/lib/meshport
trusted_proxies: [127.0.0.1/32, ::1/128]   # 反向代理的位址，用來取得真實 client IP
tls: {mode: off}                # off | acme | file
turn:
  urls: [stun:mesh.example.com:3478, turn:mesh.example.com:3478?transport=udp, turn:mesh.example.com:3478?transport=tcp]
  secret: <與 coturn static-auth-secret 相同>
  ttl: 12h
  health_addr: mesh.example.com:3478
frp:
  auth_token: <frps auth.token>
  binary: /opt/meshport/bin/frps
web_dir: ""                     # 放正式控制台前端的建置輸出，會以 SPA 方式提供
```

其餘可在執行期調整的設定都在控制台「系統與 API → 設定」，例如開放註冊、核准方式、可見範圍、中繼開關、保存天數、備份排程、主題顏色。對應的 API 是 `PATCH /api/v1/admin/system/settings`。
