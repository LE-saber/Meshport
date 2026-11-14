/* Meshport client UI — talks to the local agent API (/local/*).
 * Everything that comes from peers or the server is rendered with
 * textContent (never innerHTML), so message content cannot inject markup. */
'use strict';

// ---------- tiny DOM helper ----------
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'style') el.style.cssText = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else el.setAttribute(k, v === true ? '' : v);
    }
  }
  for (const k of kids.flat(Infinity)) {
    if (k === null || k === undefined || k === false) continue;
    el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
  return el;
}
const $ = (s, r = document) => r.querySelector(s);
const ICON = {
  send: 'M4 12l16-8-6 16-2-7z',
  clip: 'M8 12l6-6a3 3 0 0 1 4 4l-8 8a5 5 0 0 1-7-7l7-7',
  back: 'M15 5l-7 7 7 7',
  link: 'M10 14a4 4 0 0 0 6 0l3-3a4 4 0 0 0-6-6l-1 1M14 10a4 4 0 0 0-6 0l-3 3a4 4 0 0 0 6 6l1-1',
  file: 'M6 3h8l4 4v14H6zM14 3v4h4',
  refresh: 'M20 11a8 8 0 1 0-2 6M20 5v6h-6',
  plus: 'M12 5v14M5 12h14',
  x: 'M6 6l12 12M18 6L6 18',
  trash: 'M5 7h14M10 11v6M14 11v6M7 7l1 13h8l1-13M9 7V4h6v3',
};
function icon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  const p = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  p.setAttribute('d', ICON[name] || '');
  svg.append(p);
  return svg;
}

// ---------- formatting ----------
const fmtBytes = (n) => {
  if (!n && n !== 0) return '';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 1 : 0)) + ' ' + u[i];
};
const pad = (n) => String(n).padStart(2, '0');
const fmtTime = (ms) => { const d = new Date(ms); return pad(d.getHours()) + ':' + pad(d.getMinutes()); };
const fmtDay = (ms) => {
  const d = new Date(ms), t = new Date();
  if (d.toDateString() === t.toDateString()) return mpText('今天');
  const y = new Date(t); y.setDate(t.getDate() - 1);
  if (d.toDateString() === y.toDateString()) return mpText('昨天');
  return d.getFullYear() + '/' + pad(d.getMonth() + 1) + '/' + pad(d.getDate());
};
const fmtShort = (ms) => {
  const d = new Date(ms), t = new Date();
  return d.toDateString() === t.toDateString() ? fmtTime(ms) : (d.getMonth() + 1) + '/' + d.getDate();
};
const fmtAgo = (v) => {
  if (!v) return mpText('從未上線');
  const t = typeof v === 'number' ? v : Date.parse(v);
  if (!t) return '';
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return mpText('剛剛');
  if (s < 3600) return Math.floor(s / 60) + mpText(' 分鐘前');
  if (s < 86400) return Math.floor(s / 3600) + mpText(' 小時前');
  return Math.floor(s / 86400) + mpText(' 天前');
};
const COLORS = ['#2f6fed', '#1a9c5b', '#c2410c', '#8a4fd8', '#0e8a9a', '#b8327a', '#5a6b8c', '#a1620b'];
const colorFor = (s) => { let x = 0; for (const c of s || '') x = (x * 31 + c.charCodeAt(0)) >>> 0; return COLORS[x % COLORS.length]; };
const initials = (n) => { n = (n || '?').trim(); const w = n.split(/\s+/); return (w.length > 1 ? w[0][0] + w[1][0] : n.slice(0, 2)).toUpperCase(); };

const MODE_LABEL = { auto: mpText('P2P 優先'), p2p: mpText('僅 P2P'), relay: mpText('僅中繼') };
const MODE_DESC = {
  auto: mpText('先嘗試 P2P（可經 TURN），失敗時改由伺服器中繼。'),
  p2p: mpText('只用直接連線，不經 TURN 或伺服器；對方離線或無法直連時訊息會留在本機等待。'),
  relay: mpText('一律經伺服器中繼（端到端加密），對方離線時由伺服器暫存。'),
};
const STATUS_LABEL = { pending: mpText('等待中'), sending: mpText('傳送中'), sent: mpText('已送出'), queued: mpText('伺服器暫存'), delivered: mpText('已送達'), read: mpText('已讀'), failed: mpText('失敗'), received: '', seen: '' };
const PATH_TAG = { p2p: ['P2P', 'ok'], turn: ['TURN', 'turn'], relay: [mpText('中繼'), 'accent'] };
const LEVEL_RANK = { guest: 1, member: 2, admin: 3 };

// ---------- API ----------
async function api(method, path, body) {
  const opt = { method, headers: { 'X-Requested-With': 'meshport' }, credentials: 'same-origin' };
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const r = await fetch(path, opt);
  let data = null;
  try { data = await r.json(); } catch (_) { /* empty */ }
  if (r.status === 401) { showUnauthorized(); throw new Error('unauthorized'); }
  if (!r.ok) throw new Error((data && data.error) || ('HTTP ' + r.status));
  return data;
}
const dev = (method, path, body) => api(method, '/local/api/' + path, body);

function toast(msg, bad) {
  const t = h('div', { class: 'toast' + (bad ? ' bad' : '') }, msg);
  $('#toasts').append(t);
  setTimeout(() => t.remove(), bad ? 6000 : 3500);
}
async function guard(fn, okMsg) {
  try { const r = await fn(); if (okMsg) toast(okMsg); return r; } catch (e) { if (e.message !== 'unauthorized') toast(e.message, true); return null; }
}

// ---------- state ----------
const S = {
  state: null,
  peers: new Map(),
  convs: {},
  msgs: new Map(),       // peer -> [Message]
  xfers: new Map(),      // id -> Transfer
  frpc: null,
  view: 'chat',
  peer: null,            // open conversation
  search: '',
};

function showUnauthorized() {
  $('#boot').hidden = true;
  $('#app').hidden = true;
  const s = $('#setup');
  s.hidden = false;
  s.replaceChildren(h('div', { class: 'setup-card' },
    h('h1', null, h('img', { src: 'icon.svg', alt: '' }), 'Meshport'),
    h('p', { class: 'muted' }, mpText('此頁面需要由 Meshport 應用程式開啟（本機存取權杖無效或已遺失）。請從系統匣或應用程式圖示重新開啟。'))));
}

function applyTheme() {
  const t = S.state && S.state.settings && S.state.settings.theme;
  if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
}

// ---------- events ----------
let es = null;
function connectEvents() {
  if (es) es.close();
  es = new EventSource('/local/events');
  es.onmessage = (ev) => {
    let e; try { e = JSON.parse(ev.data); } catch (_) { return; }
    onEvent(e.type, e.data);
  };
  es.onerror = () => { /* EventSource reconnects by itself */ };
}

function onEvent(type, d) {
  switch (type) {
    case 'state': {
      const prev = S.state && S.state.status;
      S.state = d;
      applyTheme();
      if (d.status === 'setup' || d.status === 'pending' || (d.status === 'disabled' && !d.device.id)) { renderSetup(); return; }
      if ($('#app').hidden) { showApp(); return; }
      if (prev !== d.status && d.status === 'online') loadPeers();
      renderStatus();
      if (S.view === 'settings') render();
      break;
    }
    case 'peers':
      setPeers(d);
      if (S.view === 'chat') renderConvList(), renderThreadHead();
      else if (S.view === 'devices') render();
      break;
    case 'presence': {
      const p = S.peers.get(d.device_id);
      if (p) { p.online = d.online; p.last_seen_at = d.last_seen * 1000; if (!d.online) p.link = null; }
      if (S.view === 'chat') { renderConvList(); if (S.peer === d.device_id) renderThreadHead(); }
      else if (S.view === 'devices') render();
      break;
    }
    case 'link': {
      const p = S.peers.get(d.peer);
      if (p) p.link = d.link;
      if (S.view === 'chat' && S.peer === d.peer) renderThreadHead();
      break;
    }
    case 'message': upsertMsg(d); break;
    case 'read': if (S.convs[d.peer]) S.convs[d.peer].unread = 0; renderConvList(); break;
    case 'transfer': S.xfers.set(d.id, d); onTransfer(d); break;
    case 'frpc': S.frpc = d; if (S.view === 'frp') renderFRPCStatus(); break;
    case 'level': toast(mpText('裝置等級已變更為 ') + d.level); break;
    case 'grant': toast(mpText('等級申請狀態：') + d.status + '（' + d.level + '）'); if (S.view === 'devices') render(); break;
  }
}

function setPeers(list) {
  const prev = S.peers;
  S.peers = new Map();
  for (const p of list || []) { if (!p.link && prev.get(p.id)) p.link = prev.get(p.id).link; S.peers.set(p.id, p); }
}

const counted = new Set(); // incoming ids already counted as unread
function upsertMsg(m) {
  const list = S.msgs.get(m.peer);
  if (list) {
    const i = list.findIndex((x) => x.id === m.id);
    if (i >= 0) list[i] = m; else list.push(m);
  }
  const c = S.convs[m.peer] || (S.convs[m.peer] = { peer: m.peer, unread: 0 });
  if (!c.last || c.last.ts <= m.ts || c.last.id === m.id) c.last = m;
  if (!m.out && m.status === 'received' && !counted.has(m.id)) {
    counted.add(m.id);
    if (S.view === 'chat' && S.peer === m.peer && document.visibilityState === 'visible') markRead(m.peer);
    else c.unread = (c.unread || 0) + 1;
  }
  updateUnread();
  if (S.view === 'chat') {
    renderConvList();
    if (S.peer === m.peer) renderMessages(true);
  }
}

function onTransfer(t) {
  if (S.view === 'files') { render(); return; }
  if (S.view === 'chat' && S.peer === t.peer) {
    const bar = document.querySelector('[data-xfer="' + CSS.escape(t.id) + '"]');
    if (bar && t.state === 'running') {
      bar.querySelector('i').style.width = (t.size ? (100 * t.done / t.size) : 0).toFixed(1) + '%';
      const lbl = bar.parentElement.querySelector('.xfer-label');
      if (lbl) lbl.textContent = fmtBytes(t.done) + ' / ' + fmtBytes(t.size) + (t.rate_bps ? ' · ' + fmtBytes(t.rate_bps) + '/s' : '');
    } else renderMessages(false);
  }
  if (t.state === 'failed') toast(mpText('檔案「') + t.name + mpText('」傳輸失敗：') + (t.error || ''), true);
}

function updateUnread() {
  let n = 0;
  for (const c of Object.values(S.convs)) n += c.unread || 0;
  const b = $('#unread');
  b.hidden = n === 0;
  b.textContent = n > 99 ? '99+' : n;
  document.title = n ? '(' + n + ') Meshport' : 'Meshport';
}

async function markRead(peer) {
  const c = S.convs[peer];
  if (c) c.unread = 0;
  updateUnread();
  try { await api('POST', '/local/read', { peer }); } catch (_) { /* ignore */ }
}

async function loadPeers(refresh) {
  const r = await guard(() => api(refresh ? 'POST' : 'GET', refresh ? '/local/peers/refresh' : '/local/peers'));
  if (!r) return;
  setPeers(r.items);
  S.convs = r.convs || {};
  updateUnread();
  if (S.view === 'chat') { renderConvList(); renderThreadHead(); } else if (S.view === 'devices') render();
}

// ---------- boot ----------
async function boot() {
  try {
    S.state = await api('GET', '/local/state');
  } catch (e) {
    if (e.message !== 'unauthorized') { $('#boot').hidden = true; toast(mpText('無法連線到本機服務：') + e.message, true); }
    return;
  }
  applyTheme();
  if (S.state.settings.language && S.state.settings.language !== mpLanguage()) {
    try { localStorage.setItem('meshport.language', S.state.settings.language); location.reload(); return; } catch (_) { }
  }
  mpApplyLanguage();
  connectEvents();
  if (S.state.status === 'setup' || S.state.status === 'pending' || (S.state.status === 'disabled' && !S.state.device.id)) renderSetup();
  else showApp();
}

function showApp() {
  $('#boot').hidden = true;
  $('#setup').hidden = true;
  $('#app').hidden = false;
  for (const b of document.querySelectorAll('.navbtn')) b.onclick = () => go(b.dataset.view);
  const hash = location.hash.slice(1);
  go(['chat', 'files', 'frp', 'devices', 'settings'].includes(hash) ? hash : 'chat');
  loadPeers();
  api('GET', '/local/transfers').then((r) => { for (const t of r.items) S.xfers.set(t.id, t); }).catch(() => {});
  renderStatus();
}

function go(view) {
  S.view = view;
  history.replaceState(null, '', '#' + view);
  for (const b of document.querySelectorAll('.navbtn')) b.classList.toggle('on', b.dataset.view === view);
  render();
}

function render() {
  const v = $('#view');
  const r = { chat: renderChat, files: renderFiles, frp: renderFRP, devices: renderDevices, settings: renderSettings }[S.view];
  v.replaceChildren(r());
}

function statusInfo() {
  const st = S.state || {};
  return {
    online: ['ok', mpText('已連線')], connecting: ['warn', mpText('連線中…')], offline: ['bad', mpText('離線')],
    pending: ['warn', mpText('等待核准')], disabled: ['bad', mpText('已停用')], setup: ['', mpText('未設定')],
  }[st.status] || ['', st.status || ''];
}

function renderStatus() {
  const [cls, label] = statusInfo();
  const st = S.state || {};
  $('#statusPill').replaceChildren(
    h('div', { class: 'pill', title: st.error || '' }, h('i', { class: 'dot ' + cls }), label),
    h('div', { class: 'small muted', style: 'margin-top:4px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap' }, (st.device && st.device.name) || ''),
    st.error ? h('div', { class: 'small err', style: 'margin-top:2px' }, st.error) : null,
  );
}

// ---------- setup ----------
function renderSetup() {
  $('#boot').hidden = true;
  $('#app').hidden = true;
  const s = $('#setup');
  s.hidden = false;
  const st = S.state;
  if (st.status === 'pending') {
    s.replaceChildren(h('div', { class: 'setup-card' },
      h('h1', null, h('img', { src: 'icon.svg', alt: '' }), mpText('等待核准')),
      h('p', { class: 'muted' }, mpText('已向 '), h('b', null, st.server), mpText(' 送出註冊。管理員在控制台核准後，這台裝置會自動上線。')),
      h('dl', { class: 'kv' }, h('dt', null, mpText('裝置名稱')), h('dd', null, st.device.name), h('dt', null, mpText('裝置 ID')), h('dd', { class: 'mono' }, st.device.id),
        h('dt', null, mpText('金鑰指紋')), h('dd', { class: 'mono' }, st.device.fingerprint)),
      h('p', { class: 'small muted' }, mpText('請管理員比對上面的金鑰指紋，確認是這台裝置。')),
      h('div', { class: 'row' }, h('div', { class: 'spinner', style: 'width:18px;height:18px;border-width:2px' }), h('span', { class: 'muted grow' }, mpText('每 15 秒自動檢查')),
        h('button', { class: 'btn', onclick: () => guard(() => api('POST', '/local/reconnect')) }, mpText('立即檢查')),
        h('button', { class: 'btn danger', onclick: resetDevice }, mpText('取消並重設')))));
    return;
  }
  let method = 'enroll';
  const f = {
    server: h('input', { class: 'input', placeholder: 'mesh.example.com', value: st.server || '', autocomplete: 'url' }),
    name: h('input', { class: 'input', placeholder: mpText('例如：辦公室筆電'), value: st.device.name || defaultName() }),
    enroll: h('input', { class: 'input mono', placeholder: 'mpjoin1.… / mpe_…', autocomplete: 'off' }),
    user: h('input', { class: 'input', autocomplete: 'username' }),
    pass: h('input', { class: 'input', type: 'password', autocomplete: 'current-password' }),
    totp: h('input', { class: 'input', inputmode: 'numeric', placeholder: mpText('若有啟用') }),
  };
  const err = h('div', { class: 'err small' }, st.status === 'disabled' ? (st.error || '') : '');
  const enrollBox = h('div', { class: 'grid' }, h('label', { class: 'field' }, h('span', null, mpText('註冊碼')), f.enroll,
    h('span', { class: 'small' }, mpText('由管理員在「裝置管理 → 註冊新裝置」產生。'))), MeshportEnrollment.bind(f, err));
  const userBox = h('div', { hidden: true, style: 'display:grid;gap:12px' },
    h('label', { class: 'field' }, h('span', null, mpText('帳號')), f.user),
    h('label', { class: 'field' }, h('span', null, mpText('密碼')), f.pass),
    h('label', { class: 'field' }, h('span', null, mpText('MFA 驗證碼')), f.totp),
    h('span', { class: 'small muted' }, mpText('需伺服器開放自助註冊；可能需要管理員核准。')));
  const seg = h('div', { class: 'seg' },
    h('button', { class: 'on', type: 'button', onclick: (e) => pick(e, 'enroll') }, mpText('註冊碼')),
    h('button', { type: 'button', onclick: (e) => pick(e, 'user') }, mpText('帳號密碼')));
  function pick(e, m) {
    method = m;
    for (const b of seg.children) b.classList.toggle('on', b === e.currentTarget);
    enrollBox.hidden = m !== 'enroll';
    userBox.hidden = m !== 'user';
  }
  const submit = h('button', { class: 'btn primary', type: 'submit' }, mpText('註冊這台裝置'));
  const form = h('form', { class: 'setup-card', onsubmit: async (e) => {
    e.preventDefault();
    err.textContent = '';
    submit.disabled = true;
    submit.textContent = mpText('註冊中…');
    const body = { server: f.server.value, name: f.name.value };
    if (method === 'enroll') body.enroll_token = f.enroll.value.trim();
    else Object.assign(body, { username: f.user.value, password: f.pass.value, totp: f.totp.value });
    try {
      S.state = await api('POST', '/local/setup', body);
      if (S.state.status === 'pending') renderSetup(); else showApp();
    } catch (ex) {
      err.textContent = ex.message;
    } finally {
      submit.disabled = false;
      submit.textContent = mpText('註冊這台裝置');
    }
  } },
  h('h1', null, h('img', { src: 'icon.svg', alt: '' }), mpText('連線到 Meshport')),
  mpLanguagePicker(),
  h('p', { class: 'muted', style: 'margin:0' }, mpText('這台裝置會產生自己的 Ed25519 身分金鑰和端到端加密金鑰，私鑰不會離開裝置。')),
  h('label', { class: 'field' }, h('span', null, mpText('伺服器位址')), f.server),
  h('label', { class: 'field' }, h('span', null, mpText('裝置名稱')), f.name),
  seg, enrollBox, userBox, err, submit);
  s.replaceChildren(form);
}

function defaultName() {
  const p = (S.state && S.state.platform) || '';
  return { windows: mpText('Windows 電腦'), linux: mpText('Linux 電腦'), android: mpText('Android 手機') }[p] || '';
}

async function resetDevice() {
  if (!confirm(mpText('確定要重設？這會刪除本機的身分金鑰與所有聊天紀錄，之後需要重新註冊。'))) return;
  S.state = await guard(() => api('POST', '/local/reset'));
  S.msgs.clear(); S.convs = {}; S.peers.clear(); S.peer = null;
  if (S.state) renderSetup();
}

// ---------- chat ----------
let chatEls = null;
function renderChat() {
  const search = h('input', { class: 'input', placeholder: mpText('搜尋裝置'), value: S.search, oninput: (e) => { S.search = e.target.value; renderConvList(); } });
  const list = h('div', { class: 'conv-list' });
  const thread = h('section', { class: 'thread' });
  const root = h('div', { class: 'chat' + (S.peer ? ' has-peer' : '') },
    h('aside', { class: 'convs' },
      h('div', { class: 'convs-head' }, h('h1', null, mpText('訊息')),
        h('button', { class: 'icon-btn', title: mpText('重新整理'), onclick: () => loadPeers(true) }, icon('refresh'))),
      h('div', { class: 'convs-search' }, search), list),
    thread);
  chatEls = { root, list, thread };
  renderConvList();
  renderThread();
  return root;
}

function sortedPeers() {
  const q = S.search.trim().toLowerCase();
  const arr = [...S.peers.values()].filter((p) => !q || (p.name + ' ' + p.did + ' ' + (p.user || '')).toLowerCase().includes(q));
  arr.sort((a, b) => {
    const la = S.convs[a.id] && S.convs[a.id].last, lb = S.convs[b.id] && S.convs[b.id].last;
    if (la || lb) return (lb ? lb.ts : 0) - (la ? la.ts : 0);
    if (a.online !== b.online) return a.online ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  return arr;
}

function avatar(p, size) {
  const a = h('div', { class: 'avatar', style: 'background:' + colorFor(p.id) + (size ? ';width:' + size + 'px;height:' + size + 'px' : '') }, initials(p.name));
  a.append(h('i', { class: 'dot ' + (p.online ? 'ok' : '') }));
  return a;
}

function lastPreview(m) {
  if (!m) return '';
  const pre = m.out ? mpText('你：') : '';
  return pre + (m.kind === 'file' ? '📎 ' + (m.file ? m.file.name : mpText('檔案')) : m.text);
}

function renderConvList() {
  if (!chatEls) return;
  const items = sortedPeers();
  if (!items.length) {
    chatEls.list.replaceChildren(h('div', { class: 'empty' }, S.state.status === 'online' ? mpText('沒有可見的裝置。管理員可以在控制台調整可見範圍或群組。') : mpText('尚未連線到伺服器')));
    return;
  }
  chatEls.list.replaceChildren(...items.map((p) => {
    const c = S.convs[p.id] || {};
    return h('div', { class: 'conv' + (S.peer === p.id ? ' on' : ''), onclick: () => openPeer(p.id) },
      avatar(p),
      h('div', { class: 'conv-main' },
        h('div', { class: 'conv-top' }, h('span', { class: 'conv-name' }, p.name), c.last ? h('span', { class: 'conv-time' }, fmtShort(c.last.ts)) : null),
        h('div', { class: 'conv-last' }, h('span', { class: 'grow' }, c.last ? lastPreview(c.last) : (p.online ? mpText('線上') : fmtAgo(p.last_seen_at))),
          c.unread ? h('b', { class: 'badge' }, c.unread) : null)));
  }));
}

async function openPeer(id) {
  S.peer = id;
  if (chatEls) chatEls.root.classList.add('has-peer');
  renderConvList();
  renderThread();
  if (!S.msgs.has(id)) {
    const r = await guard(() => api('GET', '/local/messages?peer=' + encodeURIComponent(id) + '&limit=200'));
    if (r) { S.msgs.set(id, r.items); if (S.peer === id) renderMessages(true); }
  }
  markRead(id);
  const p = S.peers.get(id);
  const mode = peerMode(id);
  if (p && p.online && mode !== 'relay' && (!p.link || p.link.state !== 'up')) {
    api('POST', '/local/peers/' + encodeURIComponent(id) + '/connect').catch(() => {});
  }
}

function peerMode(id) {
  const s = S.state.settings;
  return (s.peer_modes && s.peer_modes[id]) || s.mode || 'auto';
}

function linkText(p) {
  if (!p.online) return [h('span', null, mpText('離線 · ') + fmtAgo(p.last_seen_at))];
  const out = [h('i', { class: 'dot ok' }), h('span', null, mpText('線上'))];
  const l = p.link;
  const mode = peerMode(p.id);
  if (mode === 'relay') out.push(h('span', { class: 'tag accent' }, mpText('伺服器中繼')));
  else if (l && l.state === 'up') {
    out.push(h('span', { class: 'tag ' + (l.path === 'turn' ? 'turn' : 'ok') }, l.path === 'turn' ? mpText('TURN 中繼') : mpText('P2P 直連')));
    if (l.via) out.push(h('span', { class: 'hide-sm' }, l.via));
    if (l.lat_ms != null) out.push(h('span', null, l.lat_ms + ' ms'));
  } else if (l && l.state === 'nego') out.push(h('span', { class: 'tag warn' }, mpText('協商中…')));
  else out.push(h('span', { class: 'tag' }, mode === 'p2p' ? mpText('尚未直連') : mpText('待連線（必要時走中繼）')));
  return out;
}

function renderThread() {
  if (!chatEls) return;
  const t = chatEls.thread;
  const p = S.peer && S.peers.get(S.peer);
  if (!p) {
    t.replaceChildren(h('div', { class: 'empty', style: 'margin:auto' }, mpText('選擇左側的裝置開始傳訊。訊息與檔案皆為端到端加密。')));
    return;
  }
  const head = h('header', { class: 'thread-head' });
  const note = h('div', { class: 'mode-note' });
  const msgs = h('div', { class: 'msgs' });
  const ta = h('textarea', { class: 'input', rows: 1, placeholder: mpText('輸入訊息，Enter 傳送，Shift+Enter 換行') });
  const fileIn = h('input', { type: 'file', multiple: true, hidden: true, onchange: (e) => { uploadFiles(p.id, e.target.files); e.target.value = ''; } });
  const send = async () => {
    const text = ta.value.trim();
    if (!text) return;
    ta.value = '';
    autosize();
    const m = await guard(() => api('POST', '/local/messages', { peer: p.id, text, mode: peerMode(p.id) }));
    if (m) upsertMsg(m); else ta.value = text;
  };
  function autosize() { ta.style.height = 'auto'; ta.style.height = Math.min(160, ta.scrollHeight) + 'px'; }
  ta.addEventListener('input', autosize);
  ta.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(); } });
  const composer = h('div', { class: 'composer' },
    h('button', { class: 'icon-btn', title: mpText('傳送檔案'), onclick: () => fileIn.click() }, icon('clip')), fileIn, ta,
    h('button', { class: 'btn primary', onclick: send, title: mpText('傳送') }, icon('send')));
  t.replaceChildren(head, note, msgs, composer);
  t.ondragover = (e) => { e.preventDefault(); t.classList.add('drop'); };
  t.ondragleave = () => t.classList.remove('drop');
  t.ondrop = (e) => { e.preventDefault(); t.classList.remove('drop'); if (e.dataTransfer.files.length) uploadFiles(p.id, e.dataTransfer.files); };
  chatEls.head = head; chatEls.msgs = msgs; chatEls.note = note;
  renderThreadHead();
  renderMessages(true);
  setTimeout(() => ta.focus(), 0);
}

function renderThreadHead() {
  if (!chatEls || !chatEls.head) return;
  const p = S.peer && S.peers.get(S.peer);
  if (!p) return;
  const mode = peerMode(p.id);
  const sel = h('select', { class: 'input', style: 'width:auto', title: mpText('傳送模式'), onchange: async (e) => {
    const v = e.target.value;
    const pm = {}; pm[p.id] = v === 'default' ? '' : v;
    const r = await guard(() => api('PATCH', '/local/settings', { peer_modes: pm }));
    if (r) { S.state.settings = r; renderThreadHead(); }
  } },
  h('option', { value: 'default' }, mpText('預設（') + MODE_LABEL[S.state.settings.mode || 'auto'] + '）'),
  ...['auto', 'p2p', 'relay'].map((m) => h('option', { value: m }, MODE_LABEL[m])));
  sel.value = (S.state.settings.peer_modes && S.state.settings.peer_modes[p.id]) || 'default';
  chatEls.head.replaceChildren(
    h('button', { class: 'icon-btn show-sm', onclick: () => { S.peer = null; chatEls.root.classList.remove('has-peer'); renderConvList(); } }, icon('back')),
    avatar(p),
    h('div', { class: 'thread-title' }, h('b', null, p.name), h('div', { class: 'thread-sub' }, linkText(p))),
    p.online && mode !== 'relay' && !(p.link && p.link.state === 'up')
      ? h('button', { class: 'btn sm hide-sm', onclick: () => guard(() => api('POST', '/local/peers/' + encodeURIComponent(p.id) + '/connect')) }, icon('link'), mpText('建立直連')) : null,
    sel);
  chatEls.note.textContent = MODE_LABEL[mode] + '：' + MODE_DESC[mode];
}

function renderMessages(scroll) {
  if (!chatEls || !chatEls.msgs) return;
  const box = chatEls.msgs;
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 80;
  const list = S.msgs.get(S.peer) || [];
  const kids = [];
  let lastDay = '';
  for (const m of list) {
    const d = fmtDay(m.ts);
    if (d !== lastDay) { kids.push(h('div', { class: 'day' }, d)); lastDay = d; }
    kids.push(msgEl(m));
  }
  if (!list.length) kids.push(h('div', { class: 'empty' }, mpText('還沒有訊息')));
  box.replaceChildren(...kids);
  if (scroll || atBottom) box.scrollTop = box.scrollHeight;
}

function msgEl(m) {
  const body = m.kind === 'file' ? fileCard(m) : h('div', { class: 'bubble' }, linkify(m.text || ''));
  const meta = h('div', { class: 'meta' }, fmtTime(m.ts));
  if (m.path && PATH_TAG[m.path]) meta.append(h('span', { class: 'tag ' + PATH_TAG[m.path][1] }, PATH_TAG[m.path][0]));
  if (m.out) {
    if (m.status === 'failed' || m.status === 'pending') {
      meta.append(h('span', { class: 'err', title: m.error || '', onclick: () => guard(() => api('POST', '/local/messages/' + encodeURIComponent(m.id) + '/retry')) },
        (m.status === 'failed' ? mpText('失敗') : mpText('等待中')) + (m.error ? '：' + m.error : '') + mpText(' · 重試')));
    } else meta.append(h('span', null, STATUS_LABEL[m.status] || m.status));
  }
  return h('div', { class: 'msg ' + (m.out ? 'out' : 'in') }, body, meta);
}

function linkify(text) {
  const out = [];
  const re = /https?:\/\/[^\s<>"']+/g;
  let last = 0, mt;
  while ((mt = re.exec(text))) {
    if (mt.index > last) out.push(text.slice(last, mt.index));
    out.push(h('a', { href: mt[0], target: '_blank', rel: 'noopener noreferrer' }, mt[0]));
    last = mt.index + mt[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function fileCard(m) {
  const f = m.file || {};
  const t = m.xfer && S.xfers.get(m.xfer);
  const kids = [h('div', { class: 'fn', title: f.name }, f.name || mpText('檔案'))];
  if (t && (t.state === 'running' || t.state === 'queued')) {
    const pct = t.size ? (100 * t.done / t.size) : 0;
    kids.push(h('div', { class: 'small xfer-label' }, fmtBytes(t.done) + ' / ' + fmtBytes(t.size) + (t.rate_bps ? ' · ' + fmtBytes(t.rate_bps) + '/s' : '')));
    kids.push(h('div', { class: 'progress', 'data-xfer': t.id }, h('i', { style: 'width:' + pct.toFixed(1) + '%' })));
    kids.push(h('div', { class: 'file-actions' }, h('button', { onclick: () => guard(() => api('POST', '/local/transfers/' + encodeURIComponent(t.id) + '/cancel')) }, mpText('取消'))));
  } else {
    kids.push(h('div', { class: 'small' }, fmtBytes(f.size) + (t && t.state === 'failed' ? mpText(' · 失敗：') + (t.error || '') : '')));
    if (m.local) {
      const acts = [h('a', { href: '/local/files/' + encodeURIComponent(m.id) + '/download' }, m.out ? mpText('下載副本') : mpText('儲存'))];
      if (hasCap('open')) {
        acts.push(h('button', { onclick: () => guard(() => api('POST', '/local/files/' + encodeURIComponent(m.id) + '/open')) }, mpText('開啟')));
        if (S.state.platform !== 'android') acts.push(h('button', { onclick: () => guard(() => api('POST', '/local/files/' + encodeURIComponent(m.id) + '/open?folder=1')) }, mpText('顯示於資料夾')));
      }
      kids.push(h('div', { class: 'file-actions' }, acts));
    }
  }
  return h('div', { class: 'bubble' }, h('div', { class: 'filecard' }, h('div', { class: 'fi' }, icon('file')), h('div', { class: 'grow' }, kids)));
}

const hasCap = (c) => (S.state.caps || []).includes(c);

async function uploadFiles(peer, files) {
  if (!files || !files.length) return;
  const fd = new FormData();
  for (const f of files) fd.append('file', f, f.name);
  try {
    const r = await fetch('/local/files?peer=' + encodeURIComponent(peer) + '&mode=' + encodeURIComponent(peerMode(peer)), {
      method: 'POST', body: fd, headers: { 'X-Requested-With': 'meshport' }, credentials: 'same-origin' });
    const d = await r.json();
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status);
    for (const m of d.items) upsertMsg(m);
  } catch (e) { toast(mpText('傳送檔案失敗：') + e.message, true); }
}

// ---------- files ----------
function renderFiles() {
  const list = [...S.xfers.values()].sort((a, b) => b.started - a.started);
  const stateTag = (t) => ({ running: [mpText('傳輸中'), 'accent'], queued: [mpText('排隊中'), 'warn'], done: [mpText('完成'), 'ok'], failed: [mpText('失敗'), 'bad'], canceled: [mpText('已取消'), ''] }[t.state] || [t.state, '']);
  const rows = list.map((t) => {
    const [lbl, cls] = stateTag(t);
    const pct = t.size ? 100 * t.done / t.size : 0;
    return h('tr', null,
      h('td', null, h('div', { style: 'font-weight:600;max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap', title: t.name }, t.name),
        h('div', { class: 'small muted' }, (t.dir === 'send' ? mpText('傳給 ') : mpText('來自 ')) + t.peer_name)),
      h('td', null, fmtBytes(t.size)),
      h('td', { style: 'min-width:160px' }, t.state === 'running'
        ? [h('div', { class: 'progress' }, h('i', { style: 'width:' + pct.toFixed(1) + '%' })), h('div', { class: 'small muted' }, pct.toFixed(0) + '%' + (t.rate_bps ? ' · ' + fmtBytes(t.rate_bps) + '/s' : ''))]
        : h('span', { class: 'tag ' + cls, title: t.error || '' }, lbl + (t.error ? '：' + t.error : ''))),
      h('td', null, t.path && PATH_TAG[t.path] ? h('span', { class: 'tag ' + PATH_TAG[t.path][1] }, PATH_TAG[t.path][0]) : ''),
      h('td', null, t.state === 'running' ? h('button', { class: 'btn sm', onclick: () => guard(() => api('POST', '/local/transfers/' + encodeURIComponent(t.id) + '/cancel')) }, mpText('取消'))
        : (t.state === 'done' && t.local && t.msg_id ? h('a', { class: 'btn sm', href: '/local/files/' + encodeURIComponent(t.msg_id) + '/download' }, mpText('儲存')) : '')));
  });
  return h('div', { class: 'page' },
    h('div', { class: 'page-head' }, h('h1', null, mpText('檔案傳輸')), h('span', { class: 'muted small' }, mpText('下載資料夾：') + S.state.settings.download_dir)),
    h('div', { class: 'card' }, list.length
      ? h('div', { class: 'scroll-x' }, h('table', { class: 'tbl' }, h('thead', null, h('tr', null, h('th', null, mpText('檔案')), h('th', null, mpText('大小')), h('th', null, mpText('狀態')), h('th', null, mpText('路徑')), h('th', null, ''))), h('tbody', null, rows)))
      : h('div', { class: 'empty' }, mpText('目前沒有傳輸。在聊天視窗按迴紋針，或把檔案拖進對話即可傳送。'))));
}

// ---------- FRP ----------
let frpEls = null;
function renderFRP() {
  const status = h('div');
  const maps = h('div', null, h('div', { class: 'empty' }, mpText('載入中…')));
  const level = S.state.device.level;
  frpEls = { status, maps };
  const page = h('div', { class: 'page' },
    h('div', { class: 'page-head' }, h('h1', null, mpText('FRP 映射')),
      h('span', { class: 'grow' }),
      h('label', { class: 'row small muted' }, mpText('內建 frpc'), h('span', { class: 'switch' },
        h('input', { type: 'checkbox', checked: S.state.settings.frpc, onchange: async (e) => {
          const r = await guard(() => api('PATCH', '/local/settings', { frpc: e.target.checked }));
          if (r) S.state.settings = r;
        } }), h('span'))),
      h('button', { class: 'btn', onclick: async () => { const r = await guard(() => api('POST', '/local/frpc/reload'), mpText('已重新載入')); if (r) { S.frpc = r; renderFRPCStatus(); } } }, icon('refresh'), mpText('重新載入'))),
    LEVEL_RANK[level] < 2 ? h('div', { class: 'card' }, mpText('目前等級為 '), h('b', null, level || 'guest'), mpText('，需要 member 以上等級才能建立映射。可以請 admin 等級的裝置替你申請，或請管理員調整。')) : null,
    h('div', { class: 'card' }, h('h2', null, mpText('執行狀態')), status),
    h('div', { class: 'card' }, h('div', { class: 'row', style: 'margin-bottom:10px' }, h('h2', { style: 'margin:0', class: 'grow' }, mpText('我的映射')),
      LEVEL_RANK[level] >= 2 ? h('button', { class: 'btn primary', onclick: () => openMappingForm() }, icon('plus'), mpText('新增映射')) : null), maps));
  api('GET', '/local/frpc').then((r) => { S.frpc = r; renderFRPCStatus(); }).catch(() => {});
  loadMappings();
  return page;
}

function renderFRPCStatus() {
  if (!frpEls) return;
  const st = S.frpc;
  if (!st) return;
  const kids = [];
  if (!st.enabled) kids.push(h('div', { class: 'muted' }, mpText('內建 frpc 已關閉。映射仍會保存在伺服器上，但這台裝置不會發布任何服務。')));
  if (st.error) kids.push(h('div', { class: 'err small' }, st.error));
  if (st.enabled && !st.nodes.length) kids.push(h('div', { class: 'muted' }, mpText('沒有啟用中的映射。')));
  for (const n of st.nodes) {
    kids.push(h('div', { style: 'margin:6px 0 4px;font-weight:600' }, mpText('節點 ') + n.node, n.error ? h('span', { class: 'err small' }, '  ' + n.error) : ''));
    for (const p of n.proxies) {
      const ok = p.phase === 'running';
      kids.push(h('div', { class: 'row small', style: 'padding:3px 0' }, h('i', { class: 'dot ' + (ok ? 'ok' : p.err ? 'bad' : 'warn') }), h('b', null, p.name),
        h('span', { class: 'muted' }, p.type || ''), h('span', { class: 'muted' }, p.phase), p.remote_addr ? h('code', null, p.remote_addr) : null, p.err ? h('span', { class: 'err' }, p.err) : null));
    }
  }
  frpEls.status.replaceChildren(...kids);
}

async function loadMappings() {
  const r = await guard(() => dev('GET', 'me/mappings'));
  if (!r || !frpEls) return;
  if (!r.items.length) { frpEls.maps.replaceChildren(h('div', { class: 'empty' }, mpText('尚未建立映射'))); return; }
  const rows = r.items.map((m) => h('tr', null,
    h('td', null, h('b', null, m.name), h('div', { class: 'small muted' }, m.node)),
    h('td', null, h('span', { class: 'tag' }, m.proto)),
    h('td', null, h('code', null, (m.local_ip || '127.0.0.1') + ':' + m.local_port)),
    h('td', null, m.public_addr ? h('code', null, m.public_addr) : '—'),
    h('td', null, h('span', { class: 'tag ' + (m.status === 'online' ? 'ok' : m.enabled ? 'warn' : '') , title: m.last_error || '' }, m.enabled ? (m.status || '—') : mpText('已停止'))),
    h('td', null, h('div', { class: 'row', style: 'flex-wrap:nowrap' },
      h('button', { class: 'btn sm', onclick: async () => { await guard(() => dev('POST', 'me/mappings/' + m.id + '/' + (m.enabled ? 'stop' : 'start')), m.enabled ? mpText('已停止') : mpText('已啟動')); loadMappings(); } }, m.enabled ? mpText('停止') : mpText('啟動')),
      h('button', { class: 'btn sm', onclick: () => openMappingForm(m) }, mpText('編輯')),
      h('button', { class: 'icon-btn', title: mpText('刪除'), onclick: async () => { if (confirm(mpText('刪除映射「') + m.name + '」？')) { await guard(() => dev('DELETE', 'me/mappings/' + m.id), mpText('已刪除')); loadMappings(); } } }, icon('trash'))))));
  frpEls.maps.replaceChildren(h('div', { class: 'scroll-x' }, h('table', { class: 'tbl' },
    h('thead', null, h('tr', null, ...[mpText('名稱'), mpText('協定'), mpText('本機'), mpText('對外位址'), mpText('狀態'), ''].map((x) => h('th', null, x)))), h('tbody', null, rows))));
}

async function openMappingForm(edit) {
  const nodes = await guard(() => dev('GET', 'frp/nodes'));
  if (!nodes) return;
  if (!nodes.items.length) { toast(mpText('伺服器沒有可用的 frps 節點'), true); return; }
  const f = {
    name: h('input', { class: 'input', value: edit ? edit.name : '', placeholder: 'ssh-laptop' }),
    node: h('select', { class: 'input' }, nodes.items.map((n) => h('option', { value: n.id }, n.name + (n.region ? '（' + n.region + '）' : '') + ' · ' + n.public_addr))),
    proto: h('select', { class: 'input' }, ['tcp', 'udp', 'http', 'https', 'stcp', 'xtcp'].map((p) => h('option', { value: p }, p))),
    lip: h('input', { class: 'input', value: edit ? edit.local_ip : '127.0.0.1' }),
    lport: h('input', { class: 'input', type: 'number', min: 1, max: 65535, value: edit ? edit.local_port : '' }),
    rport: h('input', { class: 'input', type: 'number', min: 1, max: 65535, value: edit && edit.remote_port ? edit.remote_port : '' }),
    sub: h('input', { class: 'input', value: edit ? edit.subdomain : '' }),
    domains: h('input', { class: 'input', value: edit && edit.domains ? edit.domains.join(', ') : '', placeholder: 'a.example.com, b.example.com' }),
    secret: h('input', { class: 'input', value: '', placeholder: edit ? mpText('（不變更）') : mpText('留空自動產生') }),
    enc: h('input', { type: 'checkbox', checked: edit ? edit.use_encryption : true }),
    comp: h('input', { type: 'checkbox', checked: edit ? edit.use_compression : false }),
    bw: h('input', { class: 'input', value: edit ? edit.bandwidth_limit : '', placeholder: mpText('例如 1MB') }),
  };
  if (edit) { f.node.value = edit.node_id; f.proto.value = edit.proto; }
  const nodeInfo = h('div', { class: 'small muted' });
  const portRow = h('label', { class: 'field' }, h('span', null, mpText('遠端埠')), f.rport, nodeInfo);
  const httpRows = h('div', { class: 'grid2' }, h('label', { class: 'field' }, h('span', null, mpText('子網域')), f.sub), h('label', { class: 'field' }, h('span', null, mpText('自訂網域')), f.domains));
  const secretRow = h('label', { class: 'field' }, h('span', null, mpText('共用密鑰')), f.secret);
  const sync = () => {
    const p = f.proto.value;
    const n = nodes.items.find((x) => x.id === f.node.value) || {};
    portRow.hidden = !(p === 'tcp' || p === 'udp');
    httpRows.hidden = !(p === 'http' || p === 'https');
    secretRow.hidden = !(p === 'stcp' || p === 'xtcp');
    nodeInfo.textContent = mpText('允許的埠：') + (n.allow_ports || mpText('不限')) + (n.subdomain_host ? mpText('；子網域後綴：') + n.subdomain_host : '');
  };
  f.proto.onchange = sync; f.node.onchange = sync; sync();
  const err = h('div', { class: 'err small' });
  const dlg = h('dialog', { style: 'border:0;border-radius:14px;padding:0;max-width:560px;width:calc(100% - 32px);background:var(--panel);color:var(--text)' });
  const form = h('form', { class: 'card', style: 'margin:0;display:grid;gap:12px;box-shadow:none', onsubmit: async (e) => {
    e.preventDefault();
    const p = f.proto.value;
    const body = { name: f.name.value.trim(), node: f.node.value, proto: p, local_ip: f.lip.value.trim(), local_port: +f.lport.value,
      use_encryption: f.enc.checked, use_compression: f.comp.checked, bandwidth_limit: f.bw.value.trim() };
    if (p === 'tcp' || p === 'udp') body.remote_port = +f.rport.value;
    if (p === 'http' || p === 'https') { body.subdomain = f.sub.value.trim(); body.domains = f.domains.value.split(',').map((s) => s.trim()).filter(Boolean); }
    if ((p === 'stcp' || p === 'xtcp') && f.secret.value) body.secret_key = f.secret.value;
    try {
      const r = edit ? await dev('PATCH', 'me/mappings/' + edit.id, body) : await dev('POST', 'me/mappings', body);
      dlg.close(); dlg.remove();
      toast((edit ? mpText('已更新') : mpText('已建立')) + (r && r.public_addr ? '：' + r.public_addr : ''));
      loadMappings();
    } catch (ex) { err.textContent = ex.message; }
  } },
  h('h2', null, edit ? mpText('編輯映射') : mpText('新增映射')),
  h('div', { class: 'grid2' }, h('label', { class: 'field' }, h('span', null, mpText('名稱')), f.name), h('label', { class: 'field' }, h('span', null, mpText('協定')), f.proto)),
  h('label', { class: 'field' }, h('span', null, mpText('frps 節點')), f.node),
  h('div', { class: 'grid2' }, h('label', { class: 'field' }, h('span', null, mpText('本機 IP')), f.lip), h('label', { class: 'field' }, h('span', null, mpText('本機埠')), f.lport)),
  portRow, httpRows, secretRow,
  h('div', { class: 'row' }, h('label', { class: 'row' }, f.enc, mpText('加密')), h('label', { class: 'row' }, f.comp, mpText('壓縮')), h('label', { class: 'field grow' }, h('span', null, mpText('頻寬上限')), f.bw)),
  err,
  h('div', { class: 'row', style: 'justify-content:flex-end' }, h('button', { class: 'btn', type: 'button', onclick: () => { dlg.close(); dlg.remove(); } }, mpText('取消')), h('button', { class: 'btn primary', type: 'submit' }, edit ? mpText('儲存') : mpText('建立'))));
  dlg.append(form);
  document.body.append(dlg);
  dlg.showModal();
}

// ---------- devices ----------
function renderDevices() {
  const me = S.state.device;
  const grants = h('div', null, h('div', { class: 'muted' }, mpText('載入中…')));
  const peers = [...S.peers.values()].sort((a, b) => (a.online === b.online ? a.name.localeCompare(b.name) : a.online ? -1 : 1));
  const isAdmin = me.level === 'admin';
  const rows = peers.map((p) => h('div', { class: 'list-item' },
    avatar(p),
    h('div', { class: 'grow' },
      h('div', { class: 'row', style: 'gap:6px' }, h('b', null, p.name), h('span', { class: 'tag' }, p.level), p.group ? h('span', { class: 'tag' }, p.group) : null),
      h('div', { class: 'small muted' }, [p.did, p.os, p.user ? mpText('使用者 ') + p.user : '', p.online ? mpText('線上') : fmtAgo(p.last_seen_at), p.lan_ip ? 'LAN ' + p.lan_ip : ''].filter(Boolean).join(' · ')),
      h('div', { class: 'small muted mono', title: mpText('金鑰指紋') }, p.fingerprint || '')),
    h('div', { class: 'row', style: 'flex-wrap:nowrap' },
      h('button', { class: 'btn sm', onclick: () => { go('chat'); openPeer(p.id); } }, mpText('傳訊')),
      isAdmin ? h('button', { class: 'btn sm', onclick: () => grantForm(p) }, mpText('申請等級')) : null)));
  const page = h('div', { class: 'page' },
    h('div', { class: 'page-head' }, h('h1', null, mpText('裝置')), h('span', { class: 'grow' }), h('button', { class: 'btn', onclick: () => loadPeers(true) }, icon('refresh'), mpText('重新整理'))),
    h('div', { class: 'card' }, h('h2', null, mpText('這台裝置')), h('dl', { class: 'kv' },
      h('dt', null, mpText('名稱')), h('dd', null, me.name), h('dt', null, mpText('裝置 ID')), h('dd', { class: 'mono' }, me.id + (me.did ? '（' + me.did + '）' : '')),
      h('dt', null, mpText('等級')), h('dd', null, h('span', { class: 'tag accent' }, me.level || '—'), isAdmin ? h('span', { class: 'small muted' }, mpText('  可替其他裝置申請等級（需管理員審批）')) : null),
      h('dt', null, mpText('金鑰指紋')), h('dd', { class: 'mono' }, me.fingerprint))),
    h('div', { class: 'card' }, h('h2', null, mpText('可見的裝置（') + peers.length + '）'), peers.length ? h('div', { class: 'list' }, rows) : h('div', { class: 'empty' }, mpText('沒有可見的裝置'))),
    h('div', { class: 'card' }, h('h2', null, mpText('等級申請')), grants));
  dev('GET', 'me/grants').then((r) => {
    if (!r.items.length) { grants.replaceChildren(h('div', { class: 'muted' }, mpText('沒有相關的申請'))); return; }
    const tag = (s) => ({ pending: 'warn', approved: 'ok', rejected: 'bad', expired: '', revoked: 'bad' }[s] || '');
    grants.replaceChildren(h('div', { class: 'scroll-x' }, h('table', { class: 'tbl' },
      h('thead', null, h('tr', null, ...[mpText('目標'), mpText('等級'), mpText('期限'), mpText('申請者'), mpText('狀態'), mpText('時間')].map((x) => h('th', null, x)))),
      h('tbody', null, r.items.map((g) => h('tr', null, h('td', null, g.target), h('td', null, (g.prev_level ? g.prev_level + ' → ' : '') + g.level), h('td', null, g.duration),
        h('td', null, g.requester), h('td', null, h('span', { class: 'tag ' + tag(g.status), title: g.decision_note || '' }, g.status)), h('td', { class: 'small muted' }, g.created_at ? fmtAgo(g.created_at) : '')))))));
  }).catch((e) => grants.replaceChildren(h('div', { class: 'err small' }, e.message)));
  return page;
}

function grantForm(p) {
  const myRank = LEVEL_RANK[S.state.device.level] || 0;
  const level = h('select', { class: 'input' }, ['guest', 'member', 'admin'].filter((l) => LEVEL_RANK[l] <= myRank).map((l) => h('option', { value: l }, l)));
  const dur = h('select', { class: 'input' }, [['1h', '1h'], ['6h', '6h'], ['24h', '24h'], ['7d', '7d'], ['30d', '30d'], ['perm', mpText('永久')]].map(([v, t]) => h('option', { value: v }, t)));
  dur.value = '7d';
  const reason = h('input', { class: 'input', placeholder: mpText('申請原因（管理員審批時會看到）') });
  const err = h('div', { class: 'err small' });
  const dlg = h('dialog', { style: 'border:0;border-radius:14px;padding:0;max-width:440px;width:calc(100% - 32px);background:var(--panel);color:var(--text)' });
  dlg.append(h('form', { class: 'card', style: 'margin:0;display:grid;gap:12px;box-shadow:none', onsubmit: async (e) => {
    e.preventDefault();
    try {
      await dev('POST', 'grants', { target: p.id, level: level.value, duration: dur.value, reason: reason.value });
      dlg.close(); dlg.remove(); toast(mpText('已送出申請，等待管理員審批')); if (S.view === 'devices') render();
    } catch (ex) { err.textContent = ex.message; }
  } },
  h('h2', null, mpText('替「') + p.name + mpText('」申請等級')),
  h('div', { class: 'small muted' }, mpText('目前等級：') + p.level + mpText('。到期後自動恢復原等級。')),
  h('div', { class: 'grid2' }, h('label', { class: 'field' }, h('span', null, mpText('等級')), level), h('label', { class: 'field' }, h('span', null, mpText('期限')), dur)),
  h('label', { class: 'field' }, h('span', null, mpText('原因')), reason), err,
  h('div', { class: 'row', style: 'justify-content:flex-end' }, h('button', { class: 'btn', type: 'button', onclick: () => { dlg.close(); dlg.remove(); } }, mpText('取消')), h('button', { class: 'btn primary', type: 'submit' }, mpText('送出申請')))));
  document.body.append(dlg);
  dlg.showModal();
}

// ---------- settings ----------
function renderSettings() {
  const st = S.state, s = st.settings, d = st.device;
  const save = async (patch, msg) => { const r = await guard(() => api('PATCH', '/local/settings', patch), msg || mpText('已儲存')); if (r) { S.state.settings = r; applyTheme(); } return r; };
  const toggle = (key, label, desc) => h('div', { class: 'row', style: 'padding:8px 0' },
    h('div', { class: 'grow' }, h('div', null, label), desc ? h('div', { class: 'small muted' }, desc) : null),
    h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked: s[key], onchange: (e) => save({ [key]: e.target.checked }) }), h('span')));
  const name = h('input', { class: 'input', value: d.name });
  const dl = h('input', { class: 'input', value: s.download_dir });
  const modeSeg = h('div', { class: 'seg' }, ['auto', 'p2p', 'relay'].map((m) => h('button', { class: s.mode === m ? 'on' : '', onclick: async () => { if (await save({ mode: m })) render(); } }, MODE_LABEL[m])));
  const overrides = Object.entries(s.peer_modes || {});
  const logs = h('pre', { class: 'log', hidden: true });
  const [cls, label] = statusInfo();
  return h('div', { class: 'page' },
    h('div', { class: 'page-head' }, h('h1', null, mpText('設定'))),
    h('div', { class: 'card row' }, h('span', { class: 'grow' }, mpText('語言 / Language')), mpLanguagePicker()),
    h('div', { class: 'card' }, h('h2', null, mpText('連線')),
      h('dl', { class: 'kv' },
        h('dt', null, mpText('狀態')), h('dd', null, h('span', { class: 'pill' }, h('i', { class: 'dot ' + cls }), label), st.error ? h('div', { class: 'small err' }, st.error) : null),
        h('dt', null, mpText('伺服器')), h('dd', null, st.server),
        h('dt', null, mpText('版本')), h('dd', null, st.version + ' · ' + st.platform)),
      h('div', { class: 'row', style: 'margin-top:12px' }, h('button', { class: 'btn', onclick: () => guard(() => api('POST', '/local/reconnect'), mpText('重新連線中')) }, mpText('重新連線')))),
    h('div', { class: 'card' }, h('h2', null, mpText('這台裝置')),
      h('label', { class: 'field' }, h('span', null, mpText('裝置名稱')), h('div', { class: 'row' }, h('div', { class: 'grow' }, name), h('button', { class: 'btn', onclick: () => save({ name: name.value }) }, mpText('儲存')))),
      h('dl', { class: 'kv', style: 'margin-top:12px' },
        h('dt', null, mpText('裝置 ID')), h('dd', { class: 'mono' }, d.id), h('dt', null, mpText('等級')), h('dd', null, d.level),
        h('dt', null, mpText('身分金鑰指紋')), h('dd', { class: 'mono' }, d.fingerprint), h('dt', null, mpText('E2E 公鑰')), h('dd', { class: 'mono small' }, d.box_pubkey))),
    h('div', { class: 'card' }, h('h2', null, mpText('傳輸模式')),
      modeSeg, h('p', { class: 'small muted' }, MODE_DESC[s.mode || 'auto']),
      overrides.length ? h('div', null, h('div', { class: 'small muted', style: 'margin:8px 0 4px' }, mpText('個別裝置的設定')),
        overrides.map(([pid, m]) => h('div', { class: 'row small', style: 'padding:3px 0' }, h('span', { class: 'grow' }, (S.peers.get(pid) || {}).name || pid), h('span', { class: 'tag' }, MODE_LABEL[m]),
          h('button', { class: 'btn sm ghost', onclick: async () => { if (await save({ peer_modes: { [pid]: '' } })) render(); } }, mpText('移除'))))) : null),
    h('div', { class: 'card' }, h('h2', null, mpText('檔案與通知')),
      st.platform !== 'android' ? h('label', { class: 'field' }, h('span', null, mpText('下載資料夾')), h('div', { class: 'row' }, h('div', { class: 'grow' }, dl), h('button', { class: 'btn', onclick: () => save({ download_dir: dl.value }) }, mpText('儲存')))) : h('div', { class: 'small muted' }, mpText('接收的檔案存放在：') + s.download_dir),
      toggle('notify', mpText('系統通知'), mpText('收到訊息或檔案時通知'))),
    h('div', { class: 'card' }, h('h2', null, mpText('系統')),
      toggle('frpc', mpText('內建 frpc'), mpText('依伺服器下發的設定發布這台裝置的映射')),
      hasCap('autostart') ? toggle('autostart', st.platform === 'android' ? mpText('開機後自動啟動') : mpText('登入時自動啟動'), st.platform === 'android' ? mpText('開機後在背景保持連線') : mpText('在背景執行並顯示系統匣圖示')) : null,
      h('div', { class: 'row', style: 'padding:8px 0' }, h('div', { class: 'grow' }, mpText('外觀')),
        h('select', { class: 'input', style: 'width:auto', onchange: (e) => save({ theme: e.target.value }) },
          [['system', mpText('跟隨系統')], ['light', mpText('淺色')], ['dark', mpText('深色')]].map(([v, t]) => h('option', { value: v, selected: (s.theme || 'system') === v }, t))))),
    h('div', { class: 'card' }, h('h2', null, mpText('安全')),
      h('div', { class: 'row' },
        h('button', { class: 'btn', onclick: async () => { if (confirm(mpText('產生新的身分金鑰並向伺服器輪替？'))) { const r = await guard(() => api('POST', '/local/rotate-key'), mpText('金鑰已輪替')); if (r) { S.state = r; render(); } } } }, mpText('輪替身分金鑰')),
        h('button', { class: 'btn danger', onclick: resetDevice }, mpText('移除這台裝置的註冊')))),
    h('div', { class: 'card' }, h('div', { class: 'row' }, h('h2', { class: 'grow', style: 'margin:0' }, mpText('日誌')),
      h('button', { class: 'btn sm', onclick: async () => { const r = await guard(() => api('GET', '/local/logs')); if (r) { logs.hidden = false; logs.textContent = r.lines.join('\n'); logs.scrollTop = logs.scrollHeight; } } }, mpText('顯示最近日誌'))),
    logs));
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && S.view === 'chat' && S.peer) markRead(S.peer);
});
boot();
