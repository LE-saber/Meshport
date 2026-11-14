package store

// migrations are applied in order; PRAGMA user_version records progress.
// Never edit an applied migration — append a new one.
var migrations = []string{
	// 1: initial schema
	`
CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name  TEXT NOT NULL DEFAULT '',
  email         TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'user',      -- admin | operator | viewer | user
  mfa_secret    TEXT NOT NULL DEFAULT '',
  mfa_enabled   INTEGER NOT NULL DEFAULT 0,
  disabled      INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  last_login_at INTEGER NOT NULL DEFAULT 0,
  mfa_reminded_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE groups (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);

CREATE TABLE devices (
  id              TEXT PRIMARY KEY,
  did             TEXT NOT NULL UNIQUE,            -- human-readable MP-XXXX-XXXX
  name            TEXT NOT NULL,
  os              TEXT NOT NULL DEFAULT '',
  kind            TEXT NOT NULL DEFAULT 'laptop',  -- laptop | phone | tablet | nas | server | monitor | chip
  user_id         TEXT REFERENCES users(id) ON DELETE SET NULL,
  group_id        TEXT REFERENCES groups(id) ON DELETE SET NULL,
  level           TEXT NOT NULL DEFAULT 'guest',   -- admin | member | guest
  status          TEXT NOT NULL DEFAULT 'pending', -- pending | active | disabled | banned
  ban_reason      TEXT NOT NULL DEFAULT '',
  pubkey          TEXT NOT NULL,                   -- base64 ed25519 identity key
  box_pubkey      TEXT NOT NULL DEFAULT '',        -- base64 x25519 key for end-to-end encryption
  fingerprint     TEXT NOT NULL,
  key_created_at  INTEGER NOT NULL,
  key_rotate_required INTEGER NOT NULL DEFAULT 0,
  frp_secret      TEXT NOT NULL DEFAULT '',        -- per-device credential presented by frpc metas
  last_ip         TEXT NOT NULL DEFAULT '',
  lan_ip          TEXT NOT NULL DEFAULT '',
  nat_type        TEXT NOT NULL DEFAULT '',
  client_version  TEXT NOT NULL DEFAULT '',
  last_seen_at    INTEGER NOT NULL DEFAULT 0,
  registered_at   INTEGER NOT NULL
);
CREATE INDEX devices_user ON devices(user_id);

CREATE TABLE device_tokens (
  id           TEXT PRIMARY KEY,
  device_id    TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  token_hash   TEXT NOT NULL UNIQUE,
  ip           TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL
);

CREATE TABLE device_challenges (
  nonce      TEXT PRIMARY KEY,
  device_id  TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE web_sessions (
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash  TEXT NOT NULL UNIQUE,
  ip          TEXT NOT NULL DEFAULT '',
  user_agent  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE enroll_tokens (
  id          TEXT PRIMARY KEY,
  token_hash  TEXT NOT NULL UNIQUE,
  note        TEXT NOT NULL DEFAULT '',
  user_id     TEXT REFERENCES users(id) ON DELETE CASCADE,
  group_id    TEXT REFERENCES groups(id) ON DELETE SET NULL,
  level       TEXT NOT NULL DEFAULT 'guest',
  auto_approve INTEGER NOT NULL DEFAULT 1,
  uses_left   INTEGER NOT NULL DEFAULT 1,
  created_by  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
);

CREATE TABLE grants (
  id             TEXT PRIMARY KEY,
  requester_id   TEXT REFERENCES devices(id) ON DELETE SET NULL,  -- device that asked (NULL = admin)
  target_id      TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  level          TEXT NOT NULL,
  prev_level     TEXT NOT NULL DEFAULT '',
  reason         TEXT NOT NULL DEFAULT '',
  duration       TEXT NOT NULL DEFAULT 'perm',   -- 24h | 7d | 30d | 90d | perm
  status         TEXT NOT NULL DEFAULT 'pending', -- pending | approved | rejected | revoked | expired
  created_at     INTEGER NOT NULL,
  decided_at     INTEGER NOT NULL DEFAULT 0,
  decided_by     TEXT NOT NULL DEFAULT '',
  decision_note  TEXT NOT NULL DEFAULT '',
  expires_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX grants_status ON grants(status);

CREATE TABLE messages (
  id           TEXT PRIMARY KEY,
  client_id    TEXT NOT NULL DEFAULT '',
  from_id      TEXT NOT NULL,
  to_id        TEXT NOT NULL,
  mode         TEXT NOT NULL,          -- relay | auto  (p2p messages never touch the server)
  content_type TEXT NOT NULL DEFAULT 'text',
  body         TEXT NOT NULL,          -- base64 end-to-end ciphertext, opaque to the server
  size         INTEGER NOT NULL DEFAULT 0,
  status       TEXT NOT NULL,          -- queued | delivered | read | failed
  created_at   INTEGER NOT NULL,
  delivered_at INTEGER NOT NULL DEFAULT 0,
  read_at      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX messages_to_status ON messages(to_id, status);
CREATE INDEX messages_created ON messages(created_at);

CREATE TABLE files (
  id          TEXT PRIMARY KEY,
  from_id     TEXT NOT NULL,
  to_id       TEXT NOT NULL,
  name        TEXT NOT NULL,
  size        INTEGER NOT NULL,
  sha256      TEXT NOT NULL DEFAULT '',
  received    INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL DEFAULT 'uploading', -- uploading | ready | downloaded | expired
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
);

CREATE TABLE comm_sessions (
  id           TEXT PRIMARY KEY,
  a_id         TEXT NOT NULL,
  b_id         TEXT NOT NULL,
  mode         TEXT NOT NULL,       -- p2p | relay | auto  (what users chose)
  path         TEXT NOT NULL,       -- p2p | relay | nego | fail | idle  (what actually happens)
  via          TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL,       -- up | nego | fail | idle | closed
  latency_ms   INTEGER,
  rate_bps     INTEGER NOT NULL DEFAULT 0,
  msgs_sent    INTEGER NOT NULL DEFAULT 0,
  msgs_delivered INTEGER NOT NULL DEFAULT 0,
  msgs_read    INTEGER NOT NULL DEFAULT 0,
  msgs_failed  INTEGER NOT NULL DEFAULT 0,
  file         TEXT NOT NULL DEFAULT 'null',
  p2p_attempted INTEGER NOT NULL DEFAULT 0,
  p2p_ok       INTEGER NOT NULL DEFAULT 0,
  fail_reason  TEXT NOT NULL DEFAULT '',
  started_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  ended_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX comm_sessions_pair ON comm_sessions(a_id, b_id);
CREATE INDEX comm_sessions_updated ON comm_sessions(updated_at);

CREATE TABLE failures (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT NOT NULL,      -- p2p | relay | frp | auth | ws
  a_id       TEXT NOT NULL DEFAULT '',
  b_id       TEXT NOT NULL DEFAULT '',
  reason     TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX failures_created ON failures(created_at);

CREATE TABLE frps_nodes (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  region          TEXT NOT NULL DEFAULT '',
  public_addr     TEXT NOT NULL,          -- IP or hostname frpc dials
  bind_port       INTEGER NOT NULL DEFAULT 7000,
  vhost_http_port INTEGER NOT NULL DEFAULT 0,
  vhost_https_port INTEGER NOT NULL DEFAULT 0,
  subdomain_host  TEXT NOT NULL DEFAULT '',
  allow_ports     TEXT NOT NULL DEFAULT '',  -- e.g. "6000-7000,9987"
  dashboard_addr  TEXT NOT NULL DEFAULT '',  -- host:port of webServer
  dashboard_user  TEXT NOT NULL DEFAULT '',
  dashboard_pass  TEXT NOT NULL DEFAULT '',
  plugin_token    TEXT NOT NULL,
  managed         INTEGER NOT NULL DEFAULT 0, -- meshportd supervises the frps process
  enabled         INTEGER NOT NULL DEFAULT 1,
  status          TEXT NOT NULL DEFAULT 'unknown',
  version         TEXT NOT NULL DEFAULT '',
  last_error      TEXT NOT NULL DEFAULT '',
  last_seen_at    INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL
);

CREATE TABLE mappings (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  device_id     TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  node_id       TEXT NOT NULL REFERENCES frps_nodes(id) ON DELETE CASCADE,
  proto         TEXT NOT NULL,         -- tcp | udp | http | https | stcp | xtcp
  local_ip      TEXT NOT NULL DEFAULT '127.0.0.1',
  local_port    INTEGER NOT NULL,
  remote_port   INTEGER NOT NULL DEFAULT 0,
  domains       TEXT NOT NULL DEFAULT '[]',
  subdomain     TEXT NOT NULL DEFAULT '',
  secret_key    TEXT NOT NULL DEFAULT '',
  use_encryption INTEGER NOT NULL DEFAULT 1,
  use_compression INTEGER NOT NULL DEFAULT 0,
  bandwidth_limit TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  status        TEXT NOT NULL DEFAULT 'pending', -- pending | online | offline | error | stopped
  last_error    TEXT NOT NULL DEFAULT '',
  conns         INTEGER NOT NULL DEFAULT 0,
  traffic_in    INTEGER NOT NULL DEFAULT 0,
  traffic_out   INTEGER NOT NULL DEFAULT 0,
  rate_in       INTEGER NOT NULL DEFAULT 0,
  rate_out      INTEGER NOT NULL DEFAULT 0,
  created_by    TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE(device_id, name)
);

CREATE TABLE frpc_sessions (
  run_id      TEXT PRIMARY KEY,
  node_id     TEXT NOT NULL,
  device_id   TEXT NOT NULL,
  version     TEXT NOT NULL DEFAULT '',
  client_addr TEXT NOT NULL DEFAULT '',
  hostname    TEXT NOT NULL DEFAULT '',
  os          TEXT NOT NULL DEFAULT '',
  arch        TEXT NOT NULL DEFAULT '',
  kick        INTEGER NOT NULL DEFAULT 0,
  login_at    INTEGER NOT NULL,
  last_ping_at INTEGER NOT NULL
);

CREATE TABLE login_records (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT NOT NULL,            -- user | device | api | frpc
  subject_id  TEXT NOT NULL DEFAULT '',
  subject     TEXT NOT NULL DEFAULT '',
  ip          TEXT NOT NULL DEFAULT '',
  user_agent  TEXT NOT NULL DEFAULT '',
  success     INTEGER NOT NULL,
  method      TEXT NOT NULL DEFAULT '',
  reason      TEXT NOT NULL DEFAULT '',
  abnormal    INTEGER NOT NULL DEFAULT 0,
  abnormal_reason TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE INDEX login_records_created ON login_records(created_at);
CREATE INDEX login_records_ip ON login_records(ip, created_at);

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL DEFAULT 'token',  -- token (personal) | key (service integration)
  name         TEXT NOT NULL,
  prefix       TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  user_id      TEXT REFERENCES users(id) ON DELETE CASCADE,
  scopes       TEXT NOT NULL DEFAULT '[]',
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  last_used_ip TEXT NOT NULL DEFAULT '',
  revoked_at   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE acl_rules (
  id         TEXT PRIMARY KEY,
  action     TEXT NOT NULL,     -- allow | deny
  cidr       TEXT NOT NULL,
  note       TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE(action, cidr)
);

CREATE TABLE alerts (
  id          TEXT PRIMARY KEY,
  severity    TEXT NOT NULL,     -- info | warn | crit
  category    TEXT NOT NULL,     -- security | device | frp | system | relay
  title       TEXT NOT NULL,
  detail      TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL DEFAULT '',
  dedup_key   TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'open',  -- open | acked | resolved
  count       INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  resolved_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX alerts_status ON alerts(status, updated_at);

CREATE TABLE audit_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_type  TEXT NOT NULL,
  actor_id    TEXT NOT NULL DEFAULT '',
  actor       TEXT NOT NULL DEFAULT '',
  action      TEXT NOT NULL,
  target_type TEXT NOT NULL DEFAULT '',
  target_id   TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '{}',
  ip          TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE INDEX audit_created ON audit_log(created_at);

CREATE TABLE logs (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  ts      INTEGER NOT NULL,
  level   TEXT NOT NULL,
  source  TEXT NOT NULL DEFAULT '',
  msg     TEXT NOT NULL
);
CREATE INDEX logs_ts ON logs(ts);

CREATE TABLE metrics (
  ts    INTEGER NOT NULL,
  key   TEXT NOT NULL,
  value REAL NOT NULL,
  PRIMARY KEY (key, ts)
) WITHOUT ROWID;

CREATE TABLE webhooks (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL,
  events        TEXT NOT NULL DEFAULT '["*"]',
  secret        TEXT NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    INTEGER NOT NULL,
  last_status   INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT NOT NULL DEFAULT '',
  last_delivery_at INTEGER NOT NULL DEFAULT 0,
  fail_count    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE backups (
  id         TEXT PRIMARY KEY,
  file       TEXT NOT NULL,
  size       INTEGER NOT NULL DEFAULT 0,
  sha256     TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL,     -- manual | scheduled | pre-update
  status     TEXT NOT NULL,     -- running | ok | failed
  error      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`,
	`ALTER TABLE enroll_tokens ADD COLUMN tls_required INTEGER NOT NULL DEFAULT 0;`,
}
