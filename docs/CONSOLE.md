# Server administration console

The live console is located in `internal/console/web` and embedded in the `meshportd` binary using Go's embed facility. It uses the restored `admin.tpl.html` design template, `dc.js` renderer, and `app.js` for API-backed data. The console uses same-origin `/api/v1` requests, HttpOnly cookies, and WebSocket updates. It requires no runtime CDN or Node.js service.

English is the default language for new deployments. Traditional Chinese, Simplified Chinese, and Japanese remain selectable. A user's locally saved language is respected; the server's language setting applies when no local preference exists.

## Capabilities and authorization

The eight console sections are Overview, Devices, Communications, FRP, Access & Approvals, Security, Monitoring & Logs, and System & API. Devices, mappings, grants, users, backups, webhooks, and settings are loaded from the server; missing data is shown as empty states.

Console roles and device access levels are independent. Console operators and administrators may directly grant access levels to active devices. Pending devices must first be approved. Device levels are `guest`, `member`, and `admin`; members may publish FRP services, while admin-level devices may request grants for other devices subject to operator approval. Approved grants trigger `level.changed` and immediate capability re-evaluation. Revocations restore the preceding level.

Communication session states are `up / nego / idle / fail / closed`; transport paths are `p2p / relay / nego / fail / idle`. TURN and server relay are both categorized as relay, with the `via` field identifying the actual transport. The Security section supports audit CSV export, the audit API supports `q` filtering, and the database endpoint supports `?check=1` integrity verification. The restored interface does not have dedicated controls for the last two API features.

## Build and validation

```bash
make console
go test ./internal/api
make release VERSION=1.0.0
```

`make console` checks embedded JavaScript syntax and console regression tests. API integration tests cover static pages, caching, security headers, audit filtering, session filtering, grants, and device capability changes. Real FRP end-to-end tests require `MESHPORT_FRPS` and `MESHPORT_FRPC`. Embedded assets use content ETags and `Cache-Control: no-cache` to avoid inconsistent versions after upgrades.

## Deployment and upgrades

Set `web_dir: ""` to use the embedded console; set `web_dir: off` to disable the UI. A nonempty custom directory overrides the embedded assets. The older `web/console` design bundle is retained as a reference, **not** as the default production UI.

Before migrating from an external console, back up the binary, configuration, and database; replace `meshportd`, clear the obsolete `web_dir`, and restart. Do not reinitialize the database. The private deployment binds administration to `127.0.0.1:18080`; device traffic uses a separate, allowlisted gateway. See [Private deployment](PRIVATE_DEPLOY.md).
