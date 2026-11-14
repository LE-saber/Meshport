/* Meshport web console — talks to meshportd's /api/v1 (cookie session).
 * Dependency-free. Anything that comes from the server is inserted with
 * textContent (via h()), never innerHTML. */
'use strict';

// ======================================================================
// DOM helpers
// ======================================================================
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === undefined || v === null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'style') el.style.cssText = v;
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else if (k === 'disabled') el.disabled = !!v;
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
const SVGNS = 'http://www.w3.org/2000/svg';
function s(tag, attrs, ...kids) {
  const el = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) if (v !== undefined && v !== null) el.setAttribute(k, v);
  for (const k of kids.flat()) if (k) el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  return el;
}
const ICON = {
  overview: 'M4 13h6V4H4zM14 20h6V11h-6zM4 20h6v-3H4zM14 7h6V4h-6z',
  devices: 'M4 5h16v10H4zM2 19h20M9 15v4M15 15v4',
  comms: 'M4 5h16v11H8l-4 4z',
  frp: 'M4 7h10M10 3l4 4-4 4M20 17H10M14 13l-4 4 4 4',
  access: 'M12 3l8 4v5c0 5-3.5 8-8 9-4.5-1-8-4-8-9V7z M9 12l2 2 4-4',
  security: 'M6 10V8a6 6 0 0 1 12 0v2M5 10h14v11H5zM12 14v3',
  monitor: 'M3 12h4l3-7 4 14 3-7h4',
  system: 'M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z',
  menu: 'M4 6h16M4 12h16M4 18h16',
  x: 'M6 6l12 12M18 6L6 18',
  plus: 'M12 5v14M5 12h14',
  refresh: 'M20 11a8 8 0 1 0-2 6M20 5v6h-6',
  copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
  user: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 21a8 8 0 0 1 16 0',
  logout: 'M15 4h4v16h-4M10 8l-4 4 4 4M6 12h10',
  sun: 'M12 4V2M12 22v-2M4 12H2M22 12h-2M5 5l1.5 1.5M17.5 17.5L19 19M5 19l1.5-1.5M17.5 6.5L19 5M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z',
  book: 'M4 4h7a3 3 0 0 1 3 3v13a2 2 0 0 0-2-2H4zM20 4h-7a3 3 0 0 0-3 3v13a2 2 0 0 1 2-2h8z',
};
function icon(name) {
  const svg = s('svg', { viewBox: '0 0 24 24', class: 'i', 'aria-hidden': 'true' });
  svg.append(s('path', { d: ICON[name] || '' }));
  return svg;
}

// ======================================================================
// formatting
// ======================================================================
const pad = (n) => String(n).padStart(2, '0');
function toDate(v) {
  if (!v) return null;
  const d = typeof v === 'number' ? new Date(v < 1e12 ? v * 1000 : v) : new Date(v);
  return isNaN(d) || d.getFullYear() < 2000 ? null : d;
}
function fmtDate(v) {
  const d = toDate(v);
  if (!d) return '—';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function fmtAgo(v) {
  const d = toDate(v);
  if (!d) return '—';
  const sec = Math.round((Date.now() - d.getTime()) / 1000);
  if (sec < -60) return fmtDate(v);
  if (sec < 45) return '剛剛';
  if (sec < 3600) return Math.round(sec / 60) + ' 分鐘前';
  if (sec < 86400) return Math.round(sec / 3600) + ' 小時前';
  if (sec < 86400 * 30) return Math.round(sec / 86400) + ' 天前';
  return fmtDate(v);
}
function fmtIn(v) {
  const d = toDate(v);
  if (!d) return '永久';
  const sec = Math.round((d.getTime() - Date.now()) / 1000);
  if (sec <= 0) return '已到期';
  if (sec < 3600) return Math.ceil(sec / 60) + ' 分鐘後到期';
  if (sec < 86400) return Math.round(sec / 3600) + ' 小時後到期';
  return Math.round(sec / 86400) + ' 天後到期';
}
function fmtBytes(n) {
  if (n === null || n === undefined || isNaN(n)) return '—';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  n = Number(n);
  while (Math.abs(n) >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? Math.round(n) : n.toFixed(n < 10 ? 1 : 0)) + ' ' + u[i];
}
const fmtRate = (n) => fmtBytes(n) + '/s';
const fmtPct = (n) => (n === null || n === undefined || isNaN(n)) ? '—' : (Math.round(n * 10) / 10) + '%';
function fmtDur(sec) {
  sec = Math.floor(sec || 0);
  const d = Math.floor(sec / 86400), hh = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
  if (d) return `${d} 天 ${hh} 小時`;
  if (hh) return `${hh} 小時 ${m} 分`;
  return `${m} 分 ${sec % 60} 秒`;
}
const fmtNum = (n) => (n === null || n === undefined) ? '—' : Number(n).toLocaleString('zh-TW');

// labels
const L = {
  status: { active: '啟用', pending: '待審批', disabled: '已停用', banned: '已封鎖' },
  level: { admin: '管理級', member: '成員級', guest: '訪客級' },
  role: { admin: '管理員', operator: '操作員', viewer: '檢視者', user: '一般使用者' },
  mode: { p2p: '僅 P2P', relay: '僅中繼', auto: 'P2P 優先' },
  path: { p2p: 'P2P 直連', relay: '中繼', nego: '協商中', fail: '失敗', idle: '閒置', '': '—' },
  sstate: { up: '已連線', nego: '協商中', idle: '閒置', fail: '失敗', closed: '已結束' },
  mstatus: { online: '在線', offline: '離線', pending: '等待連線', error: '錯誤', stopped: '已停止' },
  nstatus: { online: '在線', offline: '離線', unknown: '未知', crashed: '當機', stopped: '已停止', starting: '啟動中', running: '執行中' },
  gstatus: { pending: '待審批', approved: '已核准', rejected: '已駁回', revoked: '已撤銷', expired: '已到期' },
  sev: { info: '資訊', warn: '警告', crit: '嚴重' },
  astatus: { open: '未處理', acked: '已確認', resolved: '已解決' },
  kind: { laptop: '筆電', desktop: '桌機', phone: '手機', tablet: '平板', server: '伺服器', other: '其他' },
  fkind: { p2p: 'P2P', relay: '中繼', frp: 'FRP', auth: '驗證' },
  cap: { message: '傳訊', file: '傳檔', frpc: '發布服務（frpc）', 'frps.manage': '管理 FRP 伺服器', 'grant.request': '替其他裝置申請等級', 'discover.all': '探索所有裝置' },
  duration: { '24h': '24 小時', '7d': '7 天', '30d': '30 天', '90d': '90 天', perm: '永久' },
  cmd: { ping: '連線測試（ping）', 'refresh-peers': '重新整理裝置清單', 'reload-frpc': '重新載入 frpc', 'rotate-key': '輪替金鑰', 'collect-logs': '收集日誌', disconnect: '中斷所有連線', 'restart-client': '重新啟動用戶端' },
};
const lab = (map, v) => (L[map] && L[map][v]) || v || '—';
function pill(text, tone) { return h('span', { class: 'pill' + (tone ? ' ' + tone : '') }, text); }
const levelPill = (lv) => pill(lab('level', lv), { admin: 'violet', member: 'info', guest: '' }[lv]);
const rolePill = (r) => pill(lab('role', r), { admin: 'violet', operator: 'info', viewer: '', user: '' }[r]);
function deviceStatePill(d) {
  if (d.status === 'banned') return pill('已封鎖', 'bad');
  if (d.status === 'disabled') return pill('已停用', 'warn');
  if (d.status === 'pending') return pill('待審批', 'warn');
  return d.online ? pill('在線', 'ok') : pill('離線');
}
const mapPill = (st) => pill(lab('mstatus', st), { online: 'ok', error: 'bad', pending: 'warn', offline: '', stopped: '' }[st]);
const nodePill = (st) => pill(lab('nstatus', st), { online: 'ok', running: 'ok', offline: 'bad', crashed: 'bad', unknown: '' }[st]);
const grantPill = (st) => pill(lab('gstatus', st), { pending: 'warn', approved: 'ok', rejected: 'bad', revoked: '', expired: '' }[st]);
const sevPill = (sv) => pill(lab('sev', sv), { info: 'info', warn: 'warn', crit: 'bad' }[sv]);

// ======================================================================
// API
// ======================================================================
const ERR = {
  invalid_credentials: '帳號或密碼錯誤',
  invalid_mfa: '驗證碼錯誤',
  mfa_required: '請輸入驗證器 App 上的 6 位數驗證碼',
  too_many_attempts: '嘗試次數過多，請稍後再試',
  unauthorized: '登入已失效，請重新登入',
  timeout: '裝置沒有在時限內回應',
  offline: '裝置目前離線',
};
class APIError extends Error {
  constructor(status, code, message) { super(message); this.status = status; this.code = code; }
}
async function api(method, path, body, opts = {}) {
  const o = { method, credentials: 'same-origin', headers: { 'X-Requested-With': 'meshport-console', Accept: 'application/json' } };
  if (body !== undefined && body !== null) { o.headers['Content-Type'] = 'application/json'; o.body = JSON.stringify(body); }
  let r;
  try { r = await fetch('/api/v1' + path, o); } catch (e) { throw new APIError(0, 'network', '無法連線到伺服器'); }
  let data = null;
  const ct = r.headers.get('Content-Type') || '';
  if (ct.includes('json')) { try { data = await r.json(); } catch (_) { /* empty */ } }
  else if (opts.text) { data = await r.text(); }
  if (!r.ok) {
    const e = (data && data.error) || {};
    const err = new APIError(r.status, e.code || 'http', ERR[e.code] || e.message || ('HTTP ' + r.status));
    if (r.status === 401 && !opts.noAuthRedirect) { S.me = null; showLogin(); }
    if (r.status === 403 && e.code === 'mfa_required' && !opts.noAuthRedirect) { showMFARequired(); }
    throw err;
  }
  return data;
}
const GET = (p, o) => api('GET', p, null, o);
const POST = (p, b) => api('POST', p, b === undefined ? {} : b);
const PATCH = (p, b) => api('PATCH', p, b);
const PUT = (p, b) => api('PUT', p, b);
const DEL = (p) => api('DELETE', p);
function qs(o) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(o || {})) if (v !== '' && v !== undefined && v !== null && v !== false) p.set(k, v === true ? '1' : v);
  const s = p.toString();
  return s ? '?' + s : '';
}

function toast(msg, bad) {
  const t = h('div', { class: 'toast' + (bad ? ' bad' : '') }, msg);
  $('#toasts').append(t);
  setTimeout(() => t.remove(), bad ? 6000 : 3200);
}
async function act(fn, okMsg) {
  try { const r = await fn(); if (okMsg) toast(okMsg); S.reload && S.reload(); return r; }
  catch (e) { if (e.status !== 401) toast(e.message, true); return undefined; }
}
function copy(text) {
  const done = () => toast('已複製');
  if (navigator.clipboard && window.isSecureContext) navigator.clipboard.writeText(text).then(done, () => fallbackCopy(text));
  else fallbackCopy(text);
  function fallbackCopy(t) {
    const ta = h('textarea', { style: 'position:fixed;opacity:0' }); ta.value = t; document.body.append(ta); ta.select();
    try { document.execCommand('copy'); done(); } catch (_) { toast('請手動複製', true); }
    ta.remove();
  }
}

// ======================================================================
// state, roles
// ======================================================================
const S = {
  me: null, role: 'user', settings: {},
  page: 'overview', tab: '', f: {},          // filters per page/tab
  reload: null, live: {}, ring: {}, ws: null, wsOn: false, wsRetry: 0,
  badges: { devices: 0, access: 0, security: 0 }, liveHooks: [], drawer: null,
};
const RANK = { user: 0, viewer: 1, operator: 2, admin: 3 };
const can = (r) => (RANK[S.role] || 0) >= RANK[r];
const filt = (key, def) => (S.f[key] = S.f[key] || Object.assign({}, def));

function applyTheme() {
  const st = S.settings || {};
  let theme = st.theme || 'system';
  try { theme = localStorage.getItem('mp.theme') || theme; } catch (_) { /* ignore */ }
  if (theme === 'light' || theme === 'dark') document.documentElement.dataset.theme = theme;
  else delete document.documentElement.dataset.theme;
  document.documentElement.dataset.accent = st.accent || 'jade';
  document.title = (st.console_name || 'Meshport') + ' 控制台';
  const b = document.querySelector('.nav .brand span');
  if (b) b.textContent = st.console_name || 'Meshport';
}

// ======================================================================
// components
// ======================================================================
function card(title, body, extra, opts = {}) {
  return h('section', { class: 'card' },
    title ? h('header', null, h('h3', null, title), extra ? h('div', { class: 'row right' }, extra) : null) : null,
    h('div', { class: 'body' + (opts.flush ? ' flush' : '') }, body));
}
function statCard(k, v, sub, liveKey, fmt) {
  const vEl = h('div', { class: 'v' }, v);
  if (liveKey) S.liveHooks.push((live) => { if (live[liveKey] !== undefined) vEl.textContent = fmt(live[liveKey], live); });
  return h('div', { class: 'card stat' }, h('div', { class: 'k' }, k), vEl, sub ? h('div', { class: 's' }, sub) : null);
}
function empty(msg) { return h('div', { class: 'empty' }, msg || '沒有資料'); }
function table(cols, rows, opts = {}) {
  if (!rows || rows.length === 0) return empty(opts.empty);
  const visible = cols.filter((c) => !c.hide);
  return h('div', { class: 'tbl-wrap' }, h('table', { class: 'tbl' },
    h('thead', null, h('tr', null, visible.map((c) => h('th', { class: c.cls }, c.t)))),
    h('tbody', null, rows.map((r) => h('tr', { class: opts.onRow ? 'click' : null, onclick: opts.onRow ? (e) => { if (!e.target.closest('button,a,input,select,label')) opts.onRow(r); } : null },
      visible.map((c) => h('td', { class: c.cls }, c.f(r))))))));
}
function nameCell(main, sub) { return h('div', null, h('div', null, main), sub ? h('div', { class: 'sub mono' }, sub) : null); }
function btn(label, onclick, cls = '', attrs = {}) { return h('button', Object.assign({ class: 'btn sm ' + cls, onclick }, attrs), label); }
function seg(options, value, onchange) {
  return h('div', { class: 'seg' }, options.map(([v, t]) => h('button', { class: v === value ? 'on' : '', onclick: () => onchange(v) }, t)));
}
function selectEl(options, value, onchange, cls = 'input sm') {
  const el = h('select', { class: cls, onchange: () => onchange(el.value) }, options.map(([v, t]) => h('option', { value: v }, t)));
  el.value = value || '';
  return el;
}
function searchBox(value, placeholder, onchange) {
  let t;
  const el = h('input', { class: 'input sm', type: 'search', placeholder: placeholder || '搜尋', value: value || '',
    oninput: () => { clearTimeout(t); t = setTimeout(() => onchange(el.value.trim()), 350); } });
  return el;
}
function sw(checked, onchange, disabled) {
  const inp = h('input', { type: 'checkbox', checked, disabled, onchange: () => onchange(inp.checked, inp) });
  return h('label', { class: 'switch' }, inp, h('span'));
}
function secretBox(value) {
  return h('div', { class: 'secret' }, h('code', null, value), h('button', { class: 'icon-btn', title: '複製', onclick: () => copy(value) }, icon('copy')));
}
function meter(label, pct, text) {
  const tone = pct >= 90 ? ' bad' : pct >= 75 ? ' warn' : '';
  return h('div', { class: 'meter' }, h('span', { class: 'muted' }, label),
    h('div', { class: 'bar' + tone }, h('i', { style: `width:${Math.max(0, Math.min(100, pct || 0))}%` })), h('span', { class: 'mono' }, text));
}

// ---------- line chart ----------
const PALETTE = ['var(--accent)', 'var(--info)', 'var(--violet)', 'var(--warn)', 'var(--bad)'];
function lineChart(series, opts = {}) {
  // series: [{name, points:[{t,v}], color}]
  const W = 600, H = 150, P = { l: 46, r: 8, t: 8, b: 20 };
  const svg = s('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none' });
  const all = series.flatMap((x) => x.points || []);
  if (all.length < 2) {
    svg.append(s('text', { x: W / 2, y: H / 2, 'text-anchor': 'middle' }, '資料累積中…'));
    return h('div', null, svg);
  }
  const t0 = Math.min(...all.map((p) => p.t)), t1 = Math.max(...all.map((p) => p.t));
  let vmax = Math.max(...all.map((p) => p.v), opts.min || 0);
  if (opts.max) vmax = Math.max(vmax, opts.max);
  if (vmax <= 0) vmax = 1;
  vmax *= 1.1;
  const X = (t) => P.l + (t1 === t0 ? 0 : (t - t0) / (t1 - t0)) * (W - P.l - P.r);
  const Y = (v) => H - P.b - (v / vmax) * (H - P.t - P.b);
  const fmt = opts.fmt || ((v) => String(Math.round(v)));
  for (let i = 0; i <= 3; i++) {
    const v = vmax * i / 3, y = Y(v);
    svg.append(s('line', { class: 'grid', x1: P.l, x2: W - P.r, y1: y, y2: y }));
    svg.append(s('text', { x: P.l - 6, y: y + 3, 'text-anchor': 'end' }, fmt(v)));
  }
  const tl = (t) => { const d = new Date(t * 1000); return (t1 - t0 > 2 * 86400) ? `${d.getMonth() + 1}/${d.getDate()}` : `${pad(d.getHours())}:${pad(d.getMinutes())}`; };
  svg.append(s('text', { x: P.l, y: H - 5 }, tl(t0)));
  svg.append(s('text', { x: W - P.r, y: H - 5, 'text-anchor': 'end' }, tl(t1)));
  series.forEach((sr, i) => {
    const pts = (sr.points || []).slice().sort((a, b) => a.t - b.t);
    if (!pts.length) return;
    const color = sr.color || PALETTE[i % PALETTE.length];
    const d = pts.map((p, j) => (j ? 'L' : 'M') + X(p.t).toFixed(1) + ' ' + Y(p.v).toFixed(1)).join(' ');
    if (series.length === 1) svg.append(s('path', { class: 'ar', d: d + ` L${X(pts[pts.length - 1].t)} ${H - P.b} L${X(pts[0].t)} ${H - P.b} Z`, fill: color }));
    svg.append(s('path', { class: 'ln', d, stroke: color }));
  });
  const legend = series.length > 1 || opts.legend ? h('div', { class: 'legend' }, series.map((sr, i) => {
    const last = (sr.points || []).slice(-1)[0];
    const sw = h('i'); sw.style.background = sr.color || PALETTE[i % PALETTE.length];
    return h('span', null, sw, sr.name, last ? ' ' + fmt(last.v) : '');
  })) : null;
  return h('div', null, svg, legend);
}

// ---------- dialogs ----------
function overlay(content, onClose) {
  const ov = h('div', { class: 'overlay', onmousedown: (e) => { if (e.target === ov) close(); } }, content);
  function close() { ov.remove(); document.removeEventListener('keydown', esc); onClose && onClose(); }
  function esc(e) { if (e.key === 'Escape') close(); }
  document.addEventListener('keydown', esc);
  document.body.append(ov);
  return close;
}
function confirmBox(title, message, opts = {}) {
  return new Promise((resolve) => {
    let done = false;
    const finish = (v) => { if (!done) { done = true; resolve(v); } close(); };
    const d = h('div', { class: 'dialog' },
      h('header', null, h('h3', null, title)),
      h('div', { class: 'body' }, typeof message === 'string' ? h('div', null, message) : message),
      h('footer', null, h('button', { class: 'btn', onclick: () => finish(false) }, '取消'),
        h('button', { class: 'btn ' + (opts.danger ? 'danger solid' : 'primary'), onclick: () => finish(true) }, opts.label || '確定')));
    const close = overlay(d, () => { if (!done) { done = true; resolve(false); } });
  });
}
function infoBox(title, body, opts = {}) {
  const d = h('div', { class: 'dialog' + (opts.wide ? ' wide' : '') },
    h('header', null, h('h3', null, title), h('button', { class: 'icon-btn right', onclick: () => close() }, icon('x'))),
    h('div', { class: 'body' }, body),
    h('footer', null, h('button', { class: 'btn primary', onclick: () => close() }, opts.label || '完成')));
  const close = overlay(d, opts.onClose);
  return close;
}
function showSecret(title, items, note) {
  infoBox(title, [
    h('div', { class: 'notice' }, note || '這個值只會顯示這一次，請現在複製並妥善保存。'),
    items.map(([label, value]) => h('label', { class: 'field' }, h('span', null, label), secretBox(value))),
  ], { label: '我已保存' });
}
/* formDialog(title, fields, submit) — fields: [{k,label,type,value,options,hint,required,placeholder,min,max,show:(v)=>bool}]
 * types: text number password select check textarea multi(checkbox list) info */
function formDialog(title, fields, submit, opts = {}) {
  const inputs = {};
  const rows = {};
  const errEl = h('div', { class: 'err small' });
  const values = () => {
    const v = {};
    for (const f of fields) {
      if (!f.k) continue;
      const el = inputs[f.k];
      if (f.type === 'check') v[f.k] = el.checked;
      else if (f.type === 'number') v[f.k] = el.value === '' ? null : Number(el.value);
      else if (f.type === 'multi') v[f.k] = [...el.querySelectorAll('input:checked')].map((x) => x.value);
      else v[f.k] = el.value;
    }
    return v;
  };
  const refreshVis = () => { const v = values(); for (const f of fields) if (f.show && rows[f.k || f.label]) rows[f.k || f.label].hidden = !f.show(v); };
  const body = fields.map((f) => {
    let el, row;
    if (f.type === 'info') { row = h('div', { class: f.tone === 'warn' ? 'notice' : 'notice info' }, f.label); rows[f.label] = row; return row; }
    if (f.type === 'select') {
      el = h('select', { class: 'input', onchange: refreshVis }, f.options.map(([v, t]) => h('option', { value: v }, t)));
      el.value = f.value === undefined || f.value === null ? (f.options.length ? f.options[0][0] : '') : f.value;
    } else if (f.type === 'check') {
      el = h('input', { type: 'checkbox', checked: !!f.value, onchange: refreshVis });
      row = h('label', { class: 'check' }, el, h('span', null, f.label), f.hint ? h('span', { class: 'hint' }, '— ' + f.hint) : null);
    } else if (f.type === 'textarea') {
      el = h('textarea', { class: 'input', placeholder: f.placeholder || '' }); el.value = f.value || '';
    } else if (f.type === 'multi') {
      const sel = new Set(f.value || []);
      el = h('div', { class: 'row' }, f.options.map(([v, t]) => h('label', { class: 'check small' }, h('input', { type: 'checkbox', value: v, checked: sel.has(v) }), t)));
    } else {
      el = h('input', { class: 'input', type: f.type || 'text', placeholder: f.placeholder || '', min: f.min, max: f.max, autocomplete: f.type === 'password' ? 'new-password' : 'off', oninput: refreshVis });
      el.value = f.value === undefined || f.value === null ? '' : f.value;
    }
    inputs[f.k] = el;
    row = row || h('label', { class: 'field' }, h('span', null, f.label + (f.required ? ' *' : '')), el, f.hint ? h('span', { class: 'hint' }, f.hint) : null);
    rows[f.k] = row;
    return row;
  });
  const okBtn = h('button', { class: 'btn ' + (opts.danger ? 'danger solid' : 'primary'), type: 'submit' }, opts.submitLabel || '儲存');
  const form = h('form', { class: 'dialog' + (opts.wide ? ' wide' : ''), onsubmit: async (e) => {
    e.preventDefault();
    errEl.textContent = '';
    const v = values();
    for (const f of fields) {
      if (f.required && (!f.show || f.show(v)) && (v[f.k] === '' || v[f.k] === null)) { errEl.textContent = `請填寫「${f.label}」`; return; }
    }
    okBtn.disabled = true;
    try {
      const r = await submit(v);
      if (r !== false) { close(); S.reload && S.reload(); }
    } catch (err) { errEl.textContent = err.message; }
    okBtn.disabled = false;
  } },
  h('header', null, h('h3', null, title), h('button', { class: 'icon-btn right', type: 'button', onclick: () => close() }, icon('x'))),
  h('div', { class: 'body' }, opts.intro || null, body, errEl),
  h('footer', null, h('button', { class: 'btn', type: 'button', onclick: () => close() }, '取消'), okBtn));
  const close = overlay(form);
  refreshVis();
  const first = form.querySelector('input:not([type=checkbox]),select,textarea');
  if (first) setTimeout(() => first.focus(), 30);
}
function openDrawer(title, build) {
  closeDrawer();
  const body = h('div', { class: 'body' }, h('div', { class: 'spinner' }));
  const titleEl = h('h3', null, title);
  const dr = h('aside', { class: 'drawer' }, h('header', null, titleEl, h('button', { class: 'icon-btn right', onclick: closeDrawer }, icon('x'))), body);
  document.body.append(dr);
  const ctx = {
    el: body, setTitle: (t) => { titleEl.textContent = t; },
    reload: async () => {
      try { const nodes = await build(ctx); body.replaceChildren(...[].concat(nodes)); }
      catch (e) { body.replaceChildren(h('div', { class: 'err' }, e.message)); }
    },
  };
  S.drawer = { el: dr, ctx, esc: (e) => { if (e.key === 'Escape' && !$('.overlay')) closeDrawer(); } };
  document.addEventListener('keydown', S.drawer.esc);
  ctx.reload();
  return ctx;
}
function closeDrawer() {
  if (!S.drawer) return;
  document.removeEventListener('keydown', S.drawer.esc);
  S.drawer.el.remove();
  S.drawer = null;
}

// ======================================================================
// authentication screens
// ======================================================================
function authShell(...kids) {
  closeDrawer();
  const name = (S.settings && S.settings.console_name) || 'Meshport';
  $('#app').replaceChildren(h('div', { class: 'auth' }, h('div', { class: 'card' },
    h('div', { class: 'brand' }, h('img', { src: '/icon.svg', alt: '' }), name + ' 控制台'), ...kids)));
}
function showLogin(msg) {
  stopStream();
  let needTOTP = false;
  const user = h('input', { class: 'input', autocomplete: 'username', required: true });
  const pw = h('input', { class: 'input', type: 'password', autocomplete: 'current-password', required: true });
  const totp = h('input', { class: 'input', inputmode: 'numeric', autocomplete: 'one-time-code', maxlength: 8, placeholder: '6 位數驗證碼' });
  const totpRow = h('label', { class: 'field', hidden: true }, h('span', null, '驗證碼'), totp);
  const err = h('div', { class: 'err small' }, msg || '');
  const ok = h('button', { class: 'btn primary', type: 'submit' }, '登入');
  authShell(h('h2', null, '登入'),
    h('form', { class: 'grid', style: 'display:grid;gap:12px', onsubmit: async (e) => {
      e.preventDefault();
      err.textContent = ''; ok.disabled = true;
      try {
        const r = await api('POST', '/auth/login', { username: user.value.trim(), password: pw.value, totp: needTOTP ? totp.value.trim() : '' }, { noAuthRedirect: true });
        pw.value = '';
        await boot(r && r.mfa_setup_required);
      } catch (e2) {
        if (e2.code === 'mfa_required') { needTOTP = true; totpRow.hidden = false; totp.focus(); err.textContent = ''; }
        else err.textContent = e2.message;
      }
      ok.disabled = false;
    } },
    h('label', { class: 'field' }, h('span', null, '帳號'), user),
    h('label', { class: 'field' }, h('span', null, '密碼'), pw),
    totpRow, err, ok),
    h('div', { class: 'hint' }, '第一次部署時的管理員密碼由 meshportd init 印出。'));
  setTimeout(() => user.focus(), 30);
}
function showMFARequired() {
  if ($('#mfa-required')) return;
  stopStream();
  authShell(h('div', { id: 'mfa-required', style: 'display:grid;gap:12px' },
    h('h2', null, '請先啟用兩步驟驗證'),
    h('div', { class: 'muted' }, '系統管理員要求所有控制台使用者啟用 TOTP 兩步驟驗證，完成後才能使用控制台。'),
    mfaSetupPanel(async () => { await boot(); }),
    h('button', { class: 'btn ghost', onclick: logout }, '登出')));
}
function mfaSetupPanel(onDone) {
  const box = h('div', { style: 'display:grid;gap:12px' });
  const start = async () => {
    try {
      const r = await POST('/auth/mfa/setup');
      const code = h('input', { class: 'input', inputmode: 'numeric', maxlength: 8, placeholder: '6 位數驗證碼' });
      const err = h('div', { class: 'err small' });
      box.replaceChildren(
        h('div', { class: 'small' }, '在驗證器 App（Google Authenticator、1Password、Authy…）新增帳戶，手動輸入以下金鑰，或複製 otpauth 連結：'),
        h('label', { class: 'field' }, h('span', null, '金鑰'), secretBox(r.secret)),
        h('label', { class: 'field' }, h('span', null, 'otpauth 連結'), secretBox(r.otpauth_uri)),
        h('form', { class: 'row', onsubmit: async (e) => {
          e.preventDefault(); err.textContent = '';
          try { await POST('/auth/mfa/enable', { code: code.value.trim() }); toast('已啟用兩步驟驗證'); onDone && onDone(); }
          catch (e2) { err.textContent = e2.message; }
        } }, code, h('button', { class: 'btn primary', type: 'submit' }, '驗證並啟用')), err);
      code.focus();
    } catch (e) { box.replaceChildren(h('div', { class: 'err' }, e.message)); }
  };
  box.append(h('button', { class: 'btn primary', onclick: start }, '開始設定'));
  return box;
}
async function logout() {
  try { await api('POST', '/auth/logout', {}, { noAuthRedirect: true }); } catch (_) { /* ignore */ }
  S.me = null;
  showLogin('已登出');
}

// public page for enrollment links (/enroll#<token>)
function showEnroll() {
  const tok = decodeURIComponent(location.hash.slice(1));
  const server = location.origin;
  authShell(h('h2', null, '註冊新裝置'),
    tok ? [
      h('div', { class: 'muted small' }, '在要加入的裝置上安裝 Meshport 用戶端，第一次開啟時選「使用註冊碼」，填入以下資料：'),
      h('label', { class: 'field' }, h('span', null, '伺服器網址'), secretBox(server)),
      h('label', { class: 'field' }, h('span', null, '註冊碼'), secretBox(tok)),
      h('div', { class: 'hint' }, '桌面版也可以在終端機執行：meshport setup --server ' + server + ' --enroll <註冊碼>。註冊碼有使用次數與期限，請勿公開分享。'),
    ] : h('div', { class: 'err' }, '連結缺少註冊碼。'),
    h('a', { class: 'btn', href: '/' }, '前往控制台'));
}

// ======================================================================
// shell, routing, live stream
// ======================================================================
const PAGES = [
  ['overview', '概要', 'overview'],
  ['devices', '裝置管理', 'devices'],
  ['comms', '通訊管理', 'comms'],
  ['frp', 'FRP 管理', 'frp'],
  ['access', '權限與審批', 'access'],
  ['security', '安全中心', 'security'],
  ['monitor', '監控與日誌', 'monitor'],
  ['system', '系統與 API', 'system'],
];
const P = {}; // page implementations, filled below

function parseRoute() {
  const m = location.hash.replace(/^#\/?/, '').split('/');
  const page = PAGES.some((p) => p[0] === m[0]) ? m[0] : 'overview';
  return { page, tab: m[1] || '' };
}
function go(page, tab) { location.hash = '#/' + page + (tab ? '/' + tab : ''); }

let shellEls = null;
function renderShell() {
  const nav = h('nav', { class: 'nav' },
    h('div', { class: 'brand' }, h('img', { src: '/icon.svg', alt: '' }), h('span', null, S.settings.console_name || 'Meshport')),
    PAGES.map(([id, title, ic]) => h('a', { href: '#/' + id, 'data-page': id }, icon(ic), title, h('span', { class: 'count', hidden: true }))),
    h('div', { class: 'foot' },
      h('a', { href: '/api/docs', target: '_blank', rel: 'noopener' }, icon('book'), 'API 文件'),
      h('a', { href: '#', onclick: (e) => { e.preventDefault(); accountDialog(); } }, icon('user'),
        h('span', { class: 'grow' }, S.me.user ? S.me.user.username : '帳戶', h('div', { class: 'sub' }, lab('role', S.role)))),
      h('a', { href: '#', onclick: (e) => { e.preventDefault(); logout(); } }, icon('logout'), '登出')));
  const title = h('h1');
  const liveEl = h('span', { class: 'live', title: '即時資料流' }, h('i'), '即時');
  const top = h('div', { class: 'top' },
    h('button', { class: 'icon-btn menu-btn', onclick: () => shell.classList.toggle('nav-open') }, icon('menu')),
    title, h('span', { class: 'right' }), liveEl,
    h('button', { class: 'icon-btn', title: '重新整理', onclick: () => S.reload && S.reload() }, icon('refresh')));
  const content = h('div', { class: 'content' });
  const shell = h('div', { class: 'shell' }, nav, h('main', { class: 'main' }, top, content));
  $('#app').replaceChildren(shell);
  shellEls = { shell, nav, title, content, liveEl };
  updateBadges();
}
function updateBadges() {
  if (!shellEls) return;
  for (const [id, n] of Object.entries(S.badges)) {
    const el = shellEls.nav.querySelector(`a[data-page="${id}"] .count`);
    if (el) { el.hidden = !n; el.textContent = n > 99 ? '99+' : n; }
  }
  if (shellEls.liveEl) shellEls.liveEl.classList.toggle('on', S.wsOn);
}
async function refreshBadges() {
  try {
    const o = await GET('/admin/overview');
    S.badges.devices = (o.devices && o.devices.pending) || 0;
    S.badges.access = ((o.pending && o.pending.grants) || []).length;
    S.badges.security = (o.alerts || []).filter((a) => a.status === 'open').length;
    updateBadges();
  } catch (_) { /* ignore */ }
}

function route() {
  if (!S.me || !shellEls) return;
  const r = parseRoute();
  S.page = r.page; S.tab = r.tab;
  closeDrawer();
  shellEls.shell.classList.remove('nav-open');
  shellEls.nav.querySelectorAll('a[data-page]').forEach((a) => a.classList.toggle('on', a.dataset.page === r.page));
  const pg = P[r.page];
  shellEls.title.textContent = PAGES.find((p) => p[0] === r.page)[1];
  S.liveHooks = [];
  S.reload = null;
  S.onEvent = null;
  const el = h('div', { class: 'stack' });
  shellEls.content.replaceChildren(el);
  try { pg(el, r.tab); } catch (e) { el.replaceChildren(h('div', { class: 'err' }, e.message)); }
}

/* tabs + data region helper. Returns {data, setReload} */
function pageTabs(el, page, tabs, current) {
  const cur = tabs.some((t) => t[0] === current) ? current : tabs[0][0];
  el.append(h('div', { class: 'tabs' }, tabs.map(([k, t, n]) => h('button', { class: k === cur ? 'on' : '', onclick: () => go(page, k) }, t, n ? h('span', { class: 'n' }, n) : null))));
  return cur;
}
/* region(el, loader): loader(region) returns nodes; S.reload re-runs it. */
function region(el, loader) {
  const box = h('div', { class: 'stack' }, h('div', { class: 'empty' }, h('div', { class: 'spinner', style: 'margin:auto' })));
  el.append(box);
  let seq = 0;
  const run = async () => {
    const my = ++seq;
    try {
      const nodes = await loader();
      if (my !== seq) return;
      const y = window.scrollY;
      box.replaceChildren(...[].concat(nodes).filter(Boolean));
      window.scrollTo(0, y);
    } catch (e) {
      if (my !== seq) return;
      if (e.status !== 401) box.replaceChildren(card(null, h('div', { class: 'err' }, e.message)));
    }
  };
  S.reload = () => { run(); if (S.drawer) S.drawer.ctx.reload(); };
  run();
  return run;
}

// live stream (/admin/stream)
let evTimer = null;
function startStream() {
  stopStream();
  const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
  let ws;
  try { ws = new WebSocket(proto + location.host + '/api/v1/admin/stream'); } catch (_) { return; }
  S.ws = ws;
  ws.onopen = () => { S.wsOn = true; S.wsRetry = 0; updateBadges(); };
  ws.onclose = () => {
    S.wsOn = false; updateBadges();
    if (S.ws !== ws || !S.me) return;
    S.wsRetry = Math.min(S.wsRetry + 1, 6);
    setTimeout(() => { if (S.ws === ws && S.me) startStream(); }, 1000 * Math.pow(2, S.wsRetry - 1));
  };
  ws.onmessage = (m) => {
    let ev;
    try { ev = JSON.parse(m.data); } catch (_) { return; }
    onStreamEvent(ev);
  };
}
function stopStream() {
  if (S.ws) { const w = S.ws; S.ws = null; try { w.close(); } catch (_) { /* ignore */ } }
  S.wsOn = false;
}
const PAGE_EVENTS = {
  overview: ['device.', 'grant.', 'alert.', 'frp.', 'session.'],
  devices: ['device.', 'grant.'],
  comms: ['session.'],
  frp: ['frp.'],
  access: ['grant.', 'device.updated'],
  security: ['alert.', 'security.', 'device.updated'],
  system: ['backup.', 'system.'],
  monitor: ['failure'],
};
function onStreamEvent(ev) {
  const t = ev.type || '';
  if (t === 'metrics.tick') {
    const d = ev.data || {};
    S.live = d;
    const now = Math.floor(Date.now() / 1000);
    for (const [k, v] of Object.entries(d)) {
      const r = (S.ring[k] = S.ring[k] || []);
      r.push({ t: now, v });
      if (r.length > 180) r.shift();
    }
    S.liveHooks.forEach((fn) => { try { fn(d); } catch (_) { /* ignore */ } });
    return;
  }
  if (S.onEvent) { try { S.onEvent(ev); } catch (_) { /* ignore */ } }
  if (/^(device\.|grant\.|alert\.)/.test(t)) refreshBadgesSoon();
  const interest = PAGE_EVENTS[S.page] || [];
  if (interest.some((p) => t.startsWith(p))) {
    clearTimeout(evTimer);
    evTimer = setTimeout(() => { if (!$('.overlay')) S.reload && S.reload(); }, 900);
  }
}
let badgeTimer = null;
function refreshBadgesSoon() { clearTimeout(badgeTimer); badgeTimer = setTimeout(refreshBadges, 700); }

// account dialog
function accountDialog() {
  const u = S.me.user || {};
  const theme = (() => { try { return localStorage.getItem('mp.theme') || ''; } catch (_) { return ''; } })();
  const body = h('div', { style: 'display:grid;gap:14px' },
    h('div', { class: 'kv' }, h('div', null, '帳號'), h('div', null, u.username), h('div', null, '角色'), h('div', null, rolePill(S.role)),
      h('div', null, '兩步驟驗證'), h('div', null, u.mfa_enabled ? pill('已啟用', 'ok') : pill('未啟用', 'warn')),
      h('div', null, '上次登入'), h('div', null, fmtDate(u.last_login_at))),
    h('label', { class: 'field' }, h('span', null, '這個瀏覽器的外觀'), selectEl([['', '跟隨系統設定'], ['system', '跟隨作業系統'], ['light', '淺色'], ['dark', '深色']], theme, (v) => {
      try { if (v) localStorage.setItem('mp.theme', v); else localStorage.removeItem('mp.theme'); } catch (_) { /* ignore */ }
      applyTheme();
    }, 'input')),
    h('div', { class: 'row' },
      h('button', { class: 'btn', onclick: () => { close(); changePasswordDialog(); } }, '變更密碼'),
      u.mfa_enabled
        ? h('button', { class: 'btn danger', onclick: () => { close(); formDialog('停用兩步驟驗證', [{ k: 'code', label: '目前的驗證碼', required: true }], async (v) => { await POST('/auth/mfa/disable', { code: v.code.trim() }); toast('已停用'); await reloadMe(); }, { danger: true, submitLabel: '停用' }); } }, '停用兩步驟驗證')
        : h('button', { class: 'btn primary', onclick: () => { close(); infoBox('啟用兩步驟驗證', mfaSetupPanel(async () => { closeAll(); await reloadMe(); }), { label: '關閉' }); } }, '啟用兩步驟驗證')));
  const close = infoBox('我的帳戶', body, { label: '關閉' });
}
function closeAll() { document.querySelectorAll('.overlay').forEach((o) => o.remove()); }
function changePasswordDialog() {
  formDialog('變更密碼', [
    { k: 'current', label: '目前的密碼', type: 'password', required: true },
    { k: 'new', label: '新密碼', type: 'password', required: true, hint: '至少 10 個字元。變更後其他登入工作階段會被登出。' },
    { k: 'again', label: '再輸入一次新密碼', type: 'password', required: true },
  ], async (v) => {
    if (v.new !== v.again) throw new Error('兩次輸入的新密碼不一致');
    await POST('/auth/password', { current: v.current, new: v.new });
    toast('密碼已變更');
  });
}
async function reloadMe() {
  S.me = await GET('/auth/me');
  S.role = S.me.role;
  S.settings = S.me.settings || {};
}

async function boot(mfaSetup) {
  try { await reloadMe(); }
  catch (e) {
    if (e.status === 401) { showLogin(); return; }
    if (e.status === 0) { authShell(h('div', { class: 'err' }, e.message), h('button', { class: 'btn', onclick: () => boot() }, '重試')); return; }
    throw e;
  }
  applyTheme();
  if (S.me.kind === 'device') { authShell(h('div', null, '裝置權杖無法登入控制台。')); return; }
  if (S.role === 'user') {
    authShell(h('h2', null, '沒有控制台權限'), h('div', { class: 'muted' }, '你的帳號只能用來註冊自己的裝置。需要管理權限請聯絡系統管理員。'),
      h('button', { class: 'btn', onclick: logout }, '登出'));
    return;
  }
  if (mfaSetup) { showMFARequired(); return; }
  renderShell();
  route();
  startStream();
  refreshBadges();
}
window.addEventListener('hashchange', () => { if (location.pathname === '/enroll') return; route(); });
document.addEventListener('visibilitychange', () => { if (!document.hidden && S.me && !S.wsOn && !S.ws) startStream(); });

// ======================================================================
// page: overview
// ======================================================================
P.overview = (el) => {
  region(el, async () => {
    const o = await GET('/admin/overview');
    const d = o.devices || {}, ss = o.sessions || {}, fr = o.frp || {}, bw = o.bandwidth || {}, rs = o.resources || {};
    S.badges.devices = d.pending || 0; S.badges.access = (o.pending.grants || []).length;
    S.badges.security = (o.alerts || []).filter((a) => a.status === 'open').length; updateBadges();

    const bwBox = h('div');
    const seed = (k) => (S.ring[k] && S.ring[k].length > 3) ? S.ring[k] : ((o.series && o.series[k]) || []);
    const drawBW = () => bwBox.replaceChildren(lineChart([
      { name: 'P2P', points: seed('p2p_bps') }, { name: '中繼', points: seed('relay_bps') }, { name: 'FRP', points: seed('frp_bps') },
    ], { fmt: fmtRate, legend: true, min: 1024 }));
    drawBW();
    let tickN = 0;
    S.liveHooks.push(() => { if (++tickN % 2 === 0) drawBW(); });

    const resBox = h('div');
    const drawRes = (r) => resBox.replaceChildren(
      meter('CPU', r.cpu, fmtPct(r.cpu)), meter('記憶體', r.mem, fmtPct(r.mem)), meter('磁碟', r.disk, fmtPct(r.disk)),
      h('div', { class: 'kv small', style: 'margin-top:8px' },
        h('div', null, '負載（1 分）'), h('div', null, r.load1 !== undefined ? r.load1 : '—'),
        h('div', null, '網路'), h('div', null, '↓ ' + fmtRate(r.net_rx_bps) + '　↑ ' + fmtRate(r.net_tx_bps)),
        h('div', null, '執行時間'), h('div', null, fmtDur(o.resources.uptime_s))));
    drawRes(rs);
    S.liveHooks.push((live) => drawRes(Object.assign({}, rs, live)));

    const pd = o.pending.devices || [], pg = o.pending.grants || [];
    const pendingBody = (pd.length + pg.length) === 0 ? empty('沒有待處理的項目') : h('div', { class: 'list' },
      pd.map((x) => h('div', { class: 'item' }, h('div', { class: 'grow' }, h('div', null, '裝置註冊：', h('b', null, x.name)), h('div', { class: 'sub' }, [x.os, x.user, fmtAgo(x.registered_at)].filter(Boolean).join(' · '))),
        can('operator') ? [btn('核准', () => act(() => POST(`/admin/devices/${x.id}/approve`), '已核准'), 'primary'), btn('拒絕', () => rejectDevice(x), 'danger')] : null)),
      pg.map((g) => h('div', { class: 'item' }, h('div', { class: 'grow' }, h('div', null, '等級申請：', h('b', null, g.target), ' → ', levelPill(g.level)),
        h('div', { class: 'sub' }, `${g.requester} 申請 · ${lab('duration', g.duration)} · ${g.reason || '未填理由'}`)),
        can('operator') ? [btn('核准', () => approveGrant(g), 'primary'), btn('駁回', () => rejectGrant(g), 'danger')] : null)));

    const alerts = o.alerts || [];
    return [
      h('div', { class: 'cards' },
        statCard('在線裝置', `${d.online} / ${d.total}`, `離線 ${d.offline} · 待審批 ${d.pending} · 停用 ${d.disabled} · 封鎖 ${d.banned}`, 'devices_online', (v) => `${v} / ${d.total}`),
        statCard('通訊工作階段', fmtNum((ss.p2p || 0) + (ss.relay || 0)), `P2P ${ss.p2p} · 中繼 ${ss.relay} · 協商 ${ss.nego} · 失敗 ${ss.fail}`),
        statCard('FRP 映射', `${fr.active} / ${fr.total}`, `啟用 ${fr.enabled} · 節點在線 ${fr.nodes_online}/${fr.nodes_total}`, 'frp_active', (v) => `${v} / ${fr.total}`),
        statCard('總頻寬', fmtRate((bw.p2p_bps || 0) + (bw.relay_bps || 0) + (bw.frp_bps || 0)), 'P2P + 中繼 + FRP', 'p2p_bps', (v, l) => fmtRate((l.p2p_bps || 0) + (l.relay_bps || 0) + (l.frp_bps || 0)))),
      h('div', { class: 'cols' },
        card('即時頻寬', bwBox),
        card('伺服器資源', resBox)),
      h('div', { class: 'cols' },
        card('待處理', pendingBody, h('a', { class: 'btn sm ghost', href: '#/access' }, '全部審批'), { flush: true }),
        card('告警', alerts.length ? h('div', { class: 'list' }, alerts.slice(0, 8).map((a) => h('div', { class: 'item' }, sevPill(a.severity),
          h('div', { class: 'grow' }, h('div', null, a.title), h('div', { class: 'sub' }, (a.detail || '') + ' · ' + fmtAgo(a.updated_at))),
          a.status === 'open' && can('operator') ? btn('確認', () => act(() => POST(`/admin/alerts/${a.id}/ack`), '已確認')) : pill(lab('astatus', a.status))))) : empty('目前沒有告警'),
        h('a', { class: 'btn sm ghost', href: '#/security/alerts' }, '全部告警'), { flush: true })),
    ];
  });
};

// shared actions
async function rejectDevice(d) {
  if (await confirmBox('拒絕註冊', `要拒絕「${d.name}」的註冊申請嗎？這會刪除這筆註冊。`, { danger: true, label: '拒絕' }))
    act(() => POST(`/admin/devices/${d.id}/reject`), '已拒絕');
}
function approveGrant(g) {
  formDialog(`核准等級申請：${g.target} → ${lab('level', g.level)}`, [
    { k: 'duration', label: '有效期限', type: 'select', value: g.duration || 'perm', options: Object.entries(L.duration), hint: '到期後自動恢復原本的等級' },
    { k: 'note', label: '備註', placeholder: '選填' },
  ], async (v) => { await POST(`/admin/grants/${g.id}/approve`, v); toast('已核准'); }, { submitLabel: '核准', intro: g.reason ? h('div', { class: 'notice info' }, `申請理由：${g.reason}（由 ${g.requester} 提出）`) : null });
}
function rejectGrant(g) {
  formDialog(`駁回等級申請：${g.target}`, [{ k: 'note', label: '駁回原因', placeholder: '選填，會記錄在稽核日誌' }],
    async (v) => { await POST(`/admin/grants/${g.id}/reject`, v); toast('已駁回'); }, { submitLabel: '駁回', danger: true });
}

// ======================================================================
// page: devices
// ======================================================================
let groupCache = null;
async function groups() { const r = await GET('/admin/groups'); groupCache = r.items || []; return groupCache; }
P.devices = (el, tab) => {
  const cur = pageTabs(el, 'devices', [['list', '裝置'], ['groups', '群組'], ['enroll', '註冊碼']], tab);
  if (cur === 'groups') return devicesGroups(el);
  if (cur === 'enroll') return devicesEnroll(el);
  const f = filt('devices', { state: '', q: '', group: '', level: '' });
  const counts = h('span');
  const stateSeg = h('span');
  const drawSeg = (c) => stateSeg.replaceChildren(seg([['', `全部 ${c.all || 0}`], ['online', `在線 ${c.online || 0}`], ['offline', `離線 ${c.offline || 0}`], ['pending', `待審批 ${c.pending || 0}`], ['disabled', `停用 ${c.disabled || 0}`], ['banned', `封鎖 ${c.banned || 0}`]],
    f.state, (v) => { f.state = v; reload(); }));
  drawSeg({});
  const groupSel = selectEl([['', '所有群組']], f.group, (v) => { f.group = v; reload(); });
  groups().then((gs) => { gs.forEach((g) => groupSel.append(h('option', { value: g.name }, g.name))); groupSel.value = f.group; }).catch(() => {});
  el.append(h('div', { class: 'toolbar' }, stateSeg,
    searchBox(f.q, '名稱、裝置 ID、IP', (v) => { f.q = v; reload(); }), groupSel,
    selectEl([['', '所有等級'], ['admin', '管理級'], ['member', '成員級'], ['guest', '訪客級']], f.level, (v) => { f.level = v; reload(); }),
    counts, can('operator') ? h('button', { class: 'btn primary sm right', onclick: () => go('devices', 'enroll') }, icon('plus'), '註冊新裝置') : null));
  const reload = region(el, async () => {
    const r = await GET('/admin/devices' + qs(f));
    drawSeg(r.counts || {});
    return card(null, table([
      { t: '裝置', f: (d) => nameCell(h('span', { class: 'row', style: 'gap:6px' }, h('span', { class: 'dot ' + (d.online ? 'ok' : '') }), d.name), d.did) },
      { t: '類型 / 系統', f: (d) => h('div', null, lab('kind', d.kind), h('div', { class: 'sub' }, d.os || '')) },
      { t: '等級', f: (d) => levelPill(d.level) },
      { t: '狀態', f: (d) => deviceStatePill(d) },
      { t: '群組', f: (d) => d.group || '—' },
      { t: '擁有者', f: (d) => d.user || '—' },
      { t: 'IP', f: (d) => h('div', { class: 'mono' }, d.ip || '—', d.lan_ip ? h('div', { class: 'sub' }, 'LAN ' + d.lan_ip) : null) },
      { t: '最後上線', f: (d) => d.online ? '現在' : fmtAgo(d.last_seen_at) },
      { t: '', cls: 'act', f: (d) => d.status === 'pending' && can('operator') ? [btn('核准', () => act(() => POST(`/admin/devices/${d.id}/approve`), '已核准'), 'primary'), btn('拒絕', () => rejectDevice(d), 'danger')] : null },
    ], r.items, { onRow: (d) => deviceDrawer(d.id), empty: '沒有符合條件的裝置' }), null, { flush: true });
  });
};

function deviceDrawer(id) {
  openDrawer('裝置', async (ctx) => {
    const r = await GET('/admin/devices/' + id);
    const d = r.device;
    ctx.setTitle(d.name);
    const op = can('operator');
    const actions = op ? h('div', { class: 'row' },
      btn('編輯', () => editDevice(d)),
      d.status === 'pending' ? [btn('核准', () => act(() => POST(`/admin/devices/${d.id}/approve`), '已核准'), 'primary'), btn('拒絕', () => rejectDevice(d), 'danger')] : null,
      d.status === 'active' ? btn('停用', async () => { if (await confirmBox('停用裝置', `停用後「${d.name}」會立即斷線且無法登入，可隨時重新啟用。`, { danger: true, label: '停用' })) act(() => POST(`/admin/devices/${d.id}/disable`), '已停用'); }) : null,
      d.status === 'disabled' ? btn('重新啟用', () => act(() => POST(`/admin/devices/${d.id}/enable`), '已啟用')) : null,
      d.status !== 'banned' ? btn('封鎖', () => formDialog(`封鎖「${d.name}」`, [{ k: 'reason', label: '原因', placeholder: '選填' }], async (v) => { await POST(`/admin/devices/${d.id}/ban`, v); toast('已封鎖'); }, { danger: true, submitLabel: '封鎖' }), 'danger')
        : btn('解除封鎖', () => act(() => POST(`/admin/devices/${d.id}/unban`), '已解除封鎖')),
      btn('要求輪替金鑰', async () => { if (await confirmBox('要求輪替金鑰', '裝置下次連線時會產生新的身分金鑰。在線時會立即執行。')) act(() => POST(`/admin/devices/${d.id}/rotate-key`), '已送出要求'); }),
      btn('刪除', async () => { if (await confirmBox('刪除裝置', `刪除「${d.name}」及其映射與紀錄？裝置需要重新註冊才能再加入，這個動作無法復原。`, { danger: true, label: '刪除' })) { await act(() => DEL('/admin/devices/' + d.id), '已刪除'); closeDrawer(); } }, 'danger')) : null;

    const cmdOut = h('pre', { class: 'code', hidden: true });
    const cmdSel = selectEl(Object.entries(L.cmd), 'ping', () => {}, 'input sm');
    const cmdBtn = btn('執行', async () => {
      cmdBtn.disabled = true; cmdOut.hidden = false; cmdOut.textContent = '等待裝置回應…';
      try {
        const res = await POST(`/admin/devices/${d.id}/command`, { name: cmdSel.value, timeout_s: 20 });
        cmdOut.textContent = (res.ok ? '✓ 成功' : '✗ 失敗') + (res.output ? '\n\n' + res.output : '');
      } catch (e) { cmdOut.textContent = '✗ ' + e.message; }
      cmdBtn.disabled = false;
    }, 'primary');

    return [
      h('div', { class: 'row' }, deviceStatePill(d), levelPill(d.level), d.key_rotate_required ? pill('待輪替金鑰', 'warn') : null),
      actions,
      h('section', null, h('h4', null, '基本資料'), h('div', { class: 'kv' },
        h('div', null, '裝置 ID'), h('div', { class: 'mono' }, d.did),
        h('div', null, '類型 / 系統'), h('div', null, `${lab('kind', d.kind)} · ${d.os || '—'}`),
        h('div', null, '用戶端版本'), h('div', null, d.client_version || '—'),
        h('div', null, '擁有者'), h('div', null, d.user || '—'),
        h('div', null, '群組'), h('div', null, d.group || '—'),
        h('div', null, '能力'), h('div', null, (r.capabilities || []).map((c) => lab('cap', c)).join('、') || '—'),
        h('div', null, '公網 IP'), h('div', { class: 'mono' }, d.ip || '—'),
        h('div', null, '區網 IP'), h('div', { class: 'mono' }, d.lan_ip || '—'),
        h('div', null, 'NAT 類型'), h('div', null, d.nat_type || '—'),
        h('div', null, '目前連線'), h('div', null, r.connection ? `${r.connection.ip} · 自 ${fmtAgo(r.connection.since)}` : '離線（最後上線 ' + fmtAgo(d.last_seen_at) + '）'),
        h('div', null, '註冊時間'), h('div', null, fmtDate(d.registered_at)))),
      h('section', null, h('h4', null, '身分金鑰'), h('div', { class: 'kv' },
        h('div', null, '指紋'), h('div', { class: 'mono' }, d.fingerprint),
        h('div', null, '金鑰年齡'), h('div', null, `${r.key_age_days} 天（建立於 ${fmtDate(d.key_created_at)}）`),
        h('div', null, '加密公鑰'), h('div', null, d.box_pubkey ? pill('已上傳', 'ok') : pill('尚未上傳', 'warn')))),
      op && d.online ? h('section', null, h('h4', null, '遠端指令'), h('div', { class: 'row' }, cmdSel, cmdBtn), cmdOut) : null,
      h('section', null, h('h4', null, `FRP 映射（${(r.mappings || []).length}）`), table([
        { t: '名稱', f: (m) => m.name }, { t: '協定', f: (m) => m.proto.toUpperCase() }, { t: '公開位址', f: (m) => h('span', { class: 'mono' }, m.public_addr || '—') }, { t: '狀態', f: (m) => mapPill(m.status) },
      ], r.mappings, { empty: '沒有映射' })),
      h('section', null, h('h4', null, '等級申請'), table([
        { t: '等級', f: (g) => levelPill(g.level) }, { t: '狀態', f: (g) => grantPill(g.status) }, { t: '申請者', f: (g) => g.requester }, { t: '時間', f: (g) => fmtAgo(g.created_at) },
      ], r.grants, { empty: '沒有紀錄' })),
      h('section', null, h('h4', null, '最近登入'), table([
        { t: '時間', f: (x) => fmtDate(x.at) }, { t: 'IP', f: (x) => h('span', { class: 'mono' }, x.ip) }, { t: '結果', f: (x) => x.success ? pill('成功', 'ok') : pill('失敗', 'bad') }, { t: '說明', f: (x) => x.reason || '' },
      ], r.logins, { empty: '沒有紀錄' })),
    ];
  });
}
async function editDevice(d) {
  const gs = groupCache || await groups().catch(() => []);
  let users = [];
  try { users = (await GET('/admin/users')).items || []; } catch (_) { /* viewer */ }
  formDialog(`編輯「${d.name}」`, [
    { k: 'name', label: '名稱', value: d.name, required: true },
    { k: 'kind', label: '類型', type: 'select', value: d.kind, options: Object.entries(L.kind) },
    { k: 'level', label: '等級', type: 'select', value: d.level, options: Object.entries(L.level), hint: '直接變更等級會立即生效並通知裝置；需要時效請到「權限與審批」直接授予。' },
    { k: 'group', label: '群組', type: 'select', value: d.group_id || '', options: [['', '（無）']].concat(gs.map((g) => [g.id, g.name])) },
    { k: 'user', label: '擁有者', type: 'select', value: d.user_id || '', options: [['', '（無）']].concat(users.map((u) => [u.id, u.username])) },
  ], async (v) => { await PATCH('/admin/devices/' + d.id, v); toast('已儲存'); });
}

function devicesGroups(el) {
  el.append(h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, '群組用來整理裝置，並可作為探索範圍（設定 → 探索範圍 = 群組）。'),
    can('operator') ? h('button', { class: 'btn primary sm right', onclick: () => groupDialog() }, icon('plus'), '新增群組') : null));
  region(el, async () => card(null, table([
    { t: '名稱', f: (g) => g.name }, { t: '說明', f: (g) => g.description || '—' }, { t: '裝置數', f: (g) => g.devices }, { t: '建立時間', f: (g) => fmtDate(g.created_at) },
    { t: '', cls: 'act', f: (g) => can('operator') ? [btn('編輯', () => groupDialog(g)), btn('刪除', async () => { if (await confirmBox('刪除群組', `刪除「${g.name}」？群組內的裝置不會被刪除。`, { danger: true, label: '刪除' })) act(() => DEL('/admin/groups/' + g.id), '已刪除'); }, 'danger')] : null },
  ], await groups(), { empty: '還沒有群組' }), null, { flush: true }));
}
function groupDialog(g) {
  formDialog(g ? '編輯群組' : '新增群組', [
    { k: 'name', label: '名稱', value: g && g.name, required: true }, { k: 'description', label: '說明', value: g && g.description },
  ], async (v) => { if (g) await PATCH('/admin/groups/' + g.id, v); else await POST('/admin/groups', v); toast('已儲存'); });
}

function devicesEnroll(el) {
  el.append(h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, '註冊碼讓新裝置不必輸入帳密即可加入，可限制使用次數、期限、預設等級與群組。'),
    h('button', { class: 'btn primary sm right', onclick: enrollDialog }, icon('plus'), '產生註冊碼')));
  region(el, async () => {
    const r = await GET('/admin/enroll-tokens');
    return card(null, table([
      { t: '備註', f: (e) => e.note || '—' }, { t: '等級', f: (e) => levelPill(e.level) }, { t: '群組', f: (e) => e.group || '—' }, { t: '擁有者', f: (e) => e.user || '—' },
      { t: '自動核准', f: (e) => e.auto_approve ? pill('是', 'ok') : pill('否') }, { t: '剩餘次數', f: (e) => e.uses_left },
      { t: '到期', f: (e) => fmtIn(e.expires_at) }, { t: '建立者', f: (e) => e.created_by },
      { t: '', cls: 'act', f: (e) => can('operator') ? btn('刪除', () => act(() => DEL('/admin/enroll-tokens/' + e.id), '已刪除'), 'danger') : null },
    ], r.items, { empty: '沒有有效的註冊碼' }), null, { flush: true });
  });
}
async function enrollDialog() {
  const gs = groupCache || await groups().catch(() => []);
  let users = [];
  try { users = (await GET('/admin/users')).items || []; } catch (_) { /* ignore */ }
  const isUser = S.role === 'user';
  formDialog('產生註冊碼', [
    { k: 'note', label: '備註', placeholder: '例如：王小明的筆電' },
    { k: 'level', label: '預設等級', type: 'select', value: 'guest', options: Object.entries(L.level) },
    { k: 'group', label: '群組', type: 'select', value: '', options: [['', '（無）']].concat(gs.map((g) => [g.id, g.name])) },
    { k: 'user', label: '擁有者', type: 'select', value: '', options: [['', '（我自己）']].concat(users.map((u) => [u.id, u.username])) },
    { k: 'auto_approve', label: '使用後自動核准', type: 'check', value: true },
    { k: 'uses', label: '可使用次數', type: 'number', value: 1, min: 1, max: 1000 },
    { k: 'ttl_hours', label: '有效時間（小時）', type: 'number', value: 24, min: 1, max: 8760 },
  ].filter((x) => !isUser || ['note'].includes(x.k)), async (v) => {
    const r = await POST('/admin/enroll-tokens', v);
    showSecret('註冊碼已產生', [['註冊碼', r.token], ['註冊連結（可傳給對方）', r.enroll_url], ['伺服器網址', location.origin]],
      `到期時間 ${fmtDate(r.expires_at)}。用戶端第一次開啟時選「使用註冊碼」即可。註冊碼只會顯示這一次。`);
  }, { submitLabel: '產生' });
}

// ======================================================================
// page: comms
// ======================================================================
P.comms = (el) => {
  const f = filt('comms', { mode: '', path: '', state: '', q: '', include_closed: false });
  const summary = h('div', { class: 'cards' });
  el.append(summary, h('div', { class: 'toolbar' },
    selectEl([['', '所有模式'], ['auto', 'P2P 優先'], ['p2p', '僅 P2P'], ['relay', '僅中繼']], f.mode, (v) => { f.mode = v; reload(); }),
    selectEl([['', '所有路徑'], ['p2p', 'P2P 直連'], ['relay', '中繼（TURN / 伺服器）'], ['nego', '協商中'], ['fail', '失敗'], ['idle', '閒置']], f.path, (v) => { f.path = v; reload(); }),
    selectEl([['', '所有狀態'], ['up', '已連線'], ['nego', '協商中'], ['idle', '閒置'], ['fail', '失敗']], f.state, (v) => { f.state = v; reload(); }),
    searchBox(f.q, '裝置名稱', (v) => { f.q = v; reload(); }),
    h('label', { class: 'check small' }, sw(f.include_closed, (c) => { f.include_closed = c; reload(); }), '包含已結束')));
  const reload = region(el, async () => {
    const r = await GET('/admin/sessions' + qs(f));
    const items = r.items || [];
    const m = r.relay_messages_24h || {};
    summary.replaceChildren(
      statCard('目前工作階段', items.filter((x) => !x.ended_at).length, `P2P ${items.filter((x) => x.path === 'p2p').length} · 中繼 ${items.filter((x) => x.path === 'relay').length}`),
      statCard('24 小時中繼訊息', fmtNum((m.queued || 0) + (m.delivered || 0) + (m.read || 0)), `排隊 ${m.queued || 0} · 已送達 ${m.delivered || 0} · 已讀 ${m.read || 0}`),
      statCard('即時 P2P 流量', fmtRate(S.live.p2p_bps || 0), '中繼 ' + fmtRate(S.live.relay_bps || 0), 'p2p_bps', (v) => fmtRate(v)));
    return card(null, table([
      { t: '雙方', f: (x) => h('div', null, h('b', null, x.a || x.a_id), ' ↔ ', h('b', null, x.b || x.b_id), h('div', { class: 'sub' }, fmtAgo(x.started_at) + ' 開始')) },
      { t: '模式', f: (x) => pill(lab('mode', x.mode)) },
      { t: '路徑', f: (x) => h('div', null, pill(lab('path', x.path), { p2p: 'ok', turn: 'violet', relay: 'info', nego: 'warn' }[x.path]), x.via ? h('div', { class: 'sub' }, x.via) : null) },
      { t: '狀態', f: (x) => x.ended_at ? pill('已結束') : pill(lab('sstate', x.state), { fail: 'bad', nego: 'warn' }[x.state]) },
      { t: '延遲', f: (x) => x.latency_ms !== null && x.latency_ms !== undefined ? x.latency_ms + ' ms' : '—' },
      { t: '速率', f: (x) => x.rate_bps ? fmtRate(x.rate_bps) : '—' },
      { t: '訊息 送/達/讀/敗', f: (x) => h('span', { class: 'mono' }, `${x.msgs_sent}/${x.msgs_delivered}/${x.msgs_read}/${x.msgs_failed}`) },
      { t: '檔案', f: (x) => x.file ? h('div', { style: 'min-width:120px' }, h('div', { class: 'small' }, x.file.name), h('div', { class: 'bar' }, h('i', { style: `width:${x.file.percent || 0}%` })), h('div', { class: 'sub' }, `${x.file.percent || 0}% · ${fmtRate(x.file.rate_bps)}`)) : '—' },
      { t: '', cls: 'act', f: (x) => !x.ended_at && can('operator') ? btn('中斷', async () => { if (await confirmBox('中斷工作階段', `要求 ${x.a} 與 ${x.b} 關閉這個連線？雙方之後可重新建立。`, { danger: true, label: '中斷' })) act(() => POST(`/admin/sessions/${x.id}/disconnect`), '已送出'); }, 'danger') : (x.fail_reason ? h('span', { class: 'sub' }, x.fail_reason) : null) },
    ], items, { empty: '目前沒有通訊工作階段' }), null, { flush: true });
  });
};

// ======================================================================
// page: FRP
// ======================================================================
const PROTOS = ['tcp', 'udp', 'http', 'https', 'stcp', 'xtcp'];
P.frp = (el, tab) => {
  const cur = pageTabs(el, 'frp', [['mappings', '映射'], ['nodes', '節點（frps）'], ['clients', 'frpc 用戶端']], tab);
  if (cur === 'nodes') return frpNodes(el);
  if (cur === 'clients') return frpClients(el);
  const f = filt('frp', { node: '', proto: '', state: '', q: '' });
  const nodeSel = selectEl([['', '所有節點']], f.node, (v) => { f.node = v; reload(); });
  GET('/admin/frp/nodes').then((r) => { (r.items || []).forEach((n) => nodeSel.append(h('option', { value: n.name }, n.name))); nodeSel.value = f.node; }).catch(() => {});
  el.append(h('div', { class: 'toolbar' }, nodeSel,
    selectEl([['', '所有協定']].concat(PROTOS.map((p) => [p, p.toUpperCase()])), f.proto, (v) => { f.proto = v; reload(); }),
    selectEl([['', '所有狀態']].concat(Object.entries(L.mstatus)), f.state, (v) => { f.state = v; reload(); }),
    searchBox(f.q, '名稱、裝置、網域', (v) => { f.q = v; reload(); }),
    can('operator') ? h('span', { class: 'row right' },
      h('button', { class: 'btn sm danger', onclick: stopAllDialog }, '全部停止'),
      h('button', { class: 'btn primary sm', onclick: () => mappingDialog() }, icon('plus'), '新增映射')) : null));
  const reload = region(el, async () => {
    const r = await GET('/admin/frp/mappings' + qs(f));
    return card(null, table([
      { t: '名稱', f: (m) => nameCell(m.name, m.device) },
      { t: '節點', f: (m) => m.node },
      { t: '協定', f: (m) => pill(m.proto.toUpperCase()) },
      { t: '本機', f: (m) => h('span', { class: 'mono' }, `${m.local_ip}:${m.local_port}`) },
      { t: '公開位址', f: (m) => m.public_addr ? (/^https?:/.test(m.public_addr) ? h('a', { href: m.public_addr, target: '_blank', rel: 'noopener noreferrer', class: 'mono' }, m.public_addr) : h('span', { class: 'mono' }, m.public_addr)) : h('span', { class: 'sub' }, m.proto === 'stcp' || m.proto === 'xtcp' ? '僅限訪客端' : '—') },
      { t: '狀態', f: (m) => h('div', null, mapPill(m.status), m.last_error ? h('div', { class: 'sub err' }, m.last_error) : null) },
      { t: '連線 / 流量', f: (m) => h('div', { class: 'small' }, `${m.conns} 條`, h('div', { class: 'sub' }, `↓${fmtBytes(m.traffic_in)} ↑${fmtBytes(m.traffic_out)}`)) },
      { t: '', cls: 'act', f: (m) => can('operator') ? [
        m.enabled ? btn('停止', () => act(() => POST(`/admin/frp/mappings/${m.id}/stop`), '已停止')) : btn('啟動', () => act(() => POST(`/admin/frp/mappings/${m.id}/start`), '已啟動'), 'primary'),
        btn('編輯', () => mappingDialog(m)),
        btn('刪除', async () => { if (await confirmBox('刪除映射', `刪除「${m.name}」？frps 端會立即拒絕這個代理。`, { danger: true, label: '刪除' })) act(() => DEL('/admin/frp/mappings/' + m.id), '已刪除'); }, 'danger')] : null },
    ], r.items, { empty: '沒有映射' }), null, { flush: true });
  });
};
async function stopAllDialog() {
  let nodes = [];
  try { nodes = (await GET('/admin/frp/nodes')).items || []; } catch (_) { /* ignore */ }
  formDialog('停止所有映射', [
    { type: 'info', tone: 'warn', label: '所有映射會被停用，frps 端會立即中斷對應的代理。之後需要逐一重新啟動。' },
    { k: 'node', label: '範圍', type: 'select', value: '', options: [['', '所有節點']].concat(nodes.map((n) => [n.id, n.name])) },
  ], async (v) => { const r = await POST('/admin/frp/stop-all', v.node ? { node: v.node } : {}); toast(`已停止 ${r && r.stopped !== undefined ? r.stopped : ''} 個映射`); }, { danger: true, submitLabel: '全部停止' });
}
async function mappingDialog(m) {
  const [nr, dr] = await Promise.all([GET('/admin/frp/nodes'), m ? Promise.resolve({ items: [] }) : GET('/admin/devices' + qs({ state: '' }))]);
  const nodes = nr.items || [];
  if (!nodes.length) { toast('請先新增 FRP 節點', true); return; }
  const devs = (dr.items || []).filter((d) => d.status === 'active' && d.level !== 'guest');
  if (!m && !devs.length) { toast('沒有可以發布服務的裝置（需成員級以上且已啟用）', true); return; }
  const isWeb = (v) => v.proto === 'http' || v.proto === 'https';
  const isPort = (v) => v.proto === 'tcp' || v.proto === 'udp';
  const isSecret = (v) => v.proto === 'stcp' || v.proto === 'xtcp';
  const fields = [
    m ? null : { k: 'device', label: '裝置', type: 'select', options: devs.map((d) => [d.id, `${d.name}（${lab('level', d.level)}）`]), required: true },
    { k: 'name', label: '名稱', value: m && m.name, required: true, placeholder: '例如 nas-web', hint: '同一台裝置內不可重複；只能用英數、- 與 _' },
    { k: 'node', label: '節點', type: 'select', value: m && m.node_id, options: nodes.map((n) => [n.id, `${n.name}（${n.public_addr}${n.allow_ports ? '，可用埠 ' + n.allow_ports : ''}）`]) },
    { k: 'proto', label: '協定', type: 'select', value: (m && m.proto) || 'tcp', options: PROTOS.map((p) => [p, p.toUpperCase()]) },
    { k: 'local_ip', label: '本機位址', value: (m && m.local_ip) || '127.0.0.1' },
    { k: 'local_port', label: '本機埠', type: 'number', value: m && m.local_port, required: true, min: 1, max: 65535 },
    { k: 'remote_port', label: '公開埠', type: 'number', value: m && m.remote_port || '', min: 1, max: 65535, show: isPort, hint: '必須在節點允許的範圍內' },
    { k: 'domains', label: '自訂網域', value: m && (m.domains || []).join(', '), show: isWeb, placeholder: 'blog.example.com, www.example.com', hint: '以逗號分隔；或改填子網域' },
    { k: 'subdomain', label: '子網域', value: m && m.subdomain, show: isWeb, hint: '節點需設定子網域主機' },
    { k: 'secret_key', label: '訪客金鑰', value: m && m.secret_key, show: isSecret, hint: '留空會自動產生；訪客端需要同一把金鑰' },
    { k: 'use_encryption', label: '加密傳輸', type: 'check', value: m ? m.use_encryption : true },
    { k: 'use_compression', label: '壓縮', type: 'check', value: m && m.use_compression },
    { k: 'bandwidth_limit', label: '頻寬上限', value: m && m.bandwidth_limit, placeholder: '例如 1MB 或 512KB，留空不限' },
  ].filter(Boolean);
  formDialog(m ? `編輯映射「${m.name}」` : '新增映射', fields, async (v) => {
    const body = { name: v.name.trim(), node: v.node, proto: v.proto, local_ip: v.local_ip.trim() || '127.0.0.1', local_port: v.local_port,
      use_encryption: v.use_encryption, use_compression: v.use_compression, bandwidth_limit: v.bandwidth_limit.trim() };
    if (isPort(v)) body.remote_port = v.remote_port || 0;
    if (isWeb(v)) { body.domains = v.domains.split(/[,\s]+/).map((x) => x.trim()).filter(Boolean); body.subdomain = v.subdomain.trim(); }
    if (isSecret(v)) body.secret_key = v.secret_key.trim();
    if (m) await PATCH('/admin/frp/mappings/' + m.id, body);
    else { body.device = v.device; await POST('/admin/frp/mappings', body); }
    toast('已儲存，裝置會自動重新載入 frpc');
  }, { wide: true });
}
function frpNodes(el) {
  el.append(h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, '每個節點是一台 frps。由 meshportd 監管的節點可以在這裡重新啟動；遠端節點需把下載的設定套用到該台 frps。'),
    can('admin') ? h('button', { class: 'btn primary sm right', onclick: () => nodeDialog() }, icon('plus'), '新增節點') : null));
  region(el, async () => {
    const r = await GET('/admin/frp/nodes');
    return card(null, table([
      { t: '節點', f: (n) => nameCell(n.name, n.region) },
      { t: '公開位址', f: (n) => h('span', { class: 'mono' }, `${n.public_addr}:${n.bind_port}`) },
      { t: '狀態', f: (n) => h('div', null, n.enabled ? nodePill(n.status) : pill('已停用'), n.last_error ? h('div', { class: 'sub err' }, n.last_error) : null) },
      { t: '模式', f: (n) => n.managed ? pill('本機監管', 'accent') : pill('遠端') },
      { t: '版本', f: (n) => n.version || '—' },
      { t: 'frpc', f: (n) => n.frpc_clients },
      { t: '映射 在線/總數', f: (n) => `${n.mappings_online} / ${n.mappings}` },
      { t: '可用埠', f: (n) => h('span', { class: 'mono small' }, n.allow_ports || '不限') },
      { t: '', cls: 'act', f: (n) => [
        n.managed && can('operator') ? btn('重新啟動', async () => { if (await confirmBox('重新啟動 frps', `重新啟動「${n.name}」？所有經過它的連線會短暫中斷。`, { label: '重新啟動' })) act(() => POST(`/admin/frp/nodes/${n.id}/restart`), '已重新啟動'); }) : null,
        can('admin') ? [btn('設定檔', () => nodeConfig(n)), btn('編輯', () => nodeDialog(n)),
          btn('刪除', async () => { if (await confirmBox('刪除節點', `刪除「${n.name}」以及它的 ${n.mappings} 個映射？`, { danger: true, label: '刪除' })) act(() => DEL('/admin/frp/nodes/' + n.id), '已刪除'); }, 'danger')] : null] },
    ], r.items, { empty: '還沒有 FRP 節點' }), null, { flush: true });
  });
}
async function nodeConfig(n) {
  try {
    const r = await GET(`/admin/frp/nodes/${n.id}/config`, { text: true });
    const text = typeof r === 'string' ? r : JSON.stringify(r, null, 2);
    infoBox(`frps.toml — ${n.name}`, [h('div', { class: 'notice' }, '含有 frps 驗證權杖與 plugin 金鑰，請勿外流。'), h('pre', { class: 'code' }, text),
      h('div', { class: 'row' }, h('button', { class: 'btn', onclick: () => copy(text) }, icon('copy'), '複製'))], { wide: true, label: '關閉' });
  } catch (e) { toast(e.message, true); }
}
function nodeDialog(n) {
  formDialog(n ? `編輯節點「${n.name}」` : '新增 FRP 節點', [
    { k: 'name', label: '名稱', value: n && n.name, required: true, placeholder: '例如 tpe-1' },
    { k: 'region', label: '地區', value: n && n.region, placeholder: '例如 Taipei' },
    { k: 'public_addr', label: '公開位址', value: n && n.public_addr, required: true, placeholder: 'IP 或網域，frpc 與訪客由此連入' },
    { k: 'bind_port', label: 'frps 連線埠（bindPort）', type: 'number', value: (n && n.bind_port) || 7000, required: true },
    { k: 'vhost_http_port', label: 'HTTP 虛擬主機埠', type: 'number', value: n ? n.vhost_http_port || '' : '', placeholder: '例如 80，留空停用' },
    { k: 'vhost_https_port', label: 'HTTPS 虛擬主機埠', type: 'number', value: n ? n.vhost_https_port || '' : '', placeholder: '例如 443，留空停用' },
    { k: 'subdomain_host', label: '子網域主機', value: n && n.subdomain_host, placeholder: 'apps.example.com（需設定萬用 DNS）' },
    { k: 'allow_ports', label: '允許的公開埠', value: n && n.allow_ports, placeholder: '6000-7000,9000', hint: '留空表示不限制' },
    { k: 'dashboard_addr', label: 'frps 儀表板位址', value: n && n.dashboard_addr, placeholder: '127.0.0.1:7500，用來讀取即時統計' },
    { k: 'dashboard_user', label: '儀表板帳號', value: n && n.dashboard_user },
    { k: 'dashboard_password', label: '儀表板密碼', type: 'password', value: '', hint: n ? '留空表示不變更' : '' },
    { k: 'managed', label: '由 meshportd 在本機啟動並監管這台 frps', type: 'check', value: n ? n.managed : false },
    { k: 'enabled', label: '啟用', type: 'check', value: n ? n.enabled : true },
  ], async (v) => {
    const body = Object.assign({}, v);
    for (const k of ['vhost_http_port', 'vhost_https_port']) if (body[k] === null) body[k] = 0;
    if (!body.dashboard_password) delete body.dashboard_password;
    if (n) { await PATCH('/admin/frp/nodes/' + n.id, body); toast('已儲存'); return; }
    const r = await POST('/admin/frp/nodes', body);
    if (r && r.frps_toml && !v.managed) {
      infoBox('節點已新增', [h('div', { class: 'notice info' }, '這是遠端節點。請把下面的 frps.toml 放到那台主機並重新啟動 frps，它會透過 plugin 回報給 meshportd。'),
        h('pre', { class: 'code' }, r.frps_toml), h('button', { class: 'btn', onclick: () => copy(r.frps_toml) }, icon('copy'), '複製')], { wide: true });
    } else toast('已新增');
  }, { wide: true });
}
function frpClients(el) {
  region(el, async () => {
    const r = await GET('/admin/frp/clients');
    return card(null, table([
      { t: '裝置', f: (c) => c.device || c.device_id || '—' },
      { t: '節點', f: (c) => c.node },
      { t: 'frpc 版本', f: (c) => c.version || '—' },
      { t: '主機', f: (c) => h('div', null, c.hostname || '—', h('div', { class: 'sub' }, [c.os, c.arch].filter(Boolean).join('/'))) },
      { t: '來源位址', f: (c) => h('span', { class: 'mono' }, c.addr || '—') },
      { t: '在線代理', f: (c) => c.proxies_online },
      { t: '登入', f: (c) => fmtAgo(c.login_at) },
      { t: '最後心跳', f: (c) => fmtAgo(c.last_ping_at) },
    ], r.items, { empty: '目前沒有已連線的 frpc' }), null, { flush: true });
  });
}

// ======================================================================
// page: access & approvals
// ======================================================================
P.access = (el, tab) => {
  const cur = pageTabs(el, 'access', [['grants', '等級申請'], ['levels', '等級說明'], ['users', '控制台使用者']], tab);
  if (cur === 'levels') return accessLevels(el);
  if (cur === 'users') return accessUsers(el);
  const f = filt('access', { status: 'pending' });
  const segBox = h('span');
  const drawSeg = (c) => segBox.replaceChildren(seg([['pending', `待審批 ${c.pending || 0}`], ['approved', '已核准'], ['rejected', '已駁回'], ['revoked', '已撤銷'], ['expired', '已到期'], ['', '全部']], f.status, (v) => { f.status = v; reload(); }));
  drawSeg({});
  el.append(h('div', { class: 'toolbar' }, segBox, can('operator') ? h('button', { class: 'btn primary sm right', onclick: directGrantDialog }, icon('plus'), '直接授予等級') : null));
  const reload = region(el, async () => {
    const r = await GET('/admin/grants' + qs(f));
    drawSeg(r.counts || {});
    S.badges.access = (r.counts && r.counts.pending) || 0; updateBadges();
    return card(null, table([
      { t: '目標裝置', f: (g) => h('b', null, g.target) },
      { t: '申請等級', f: (g) => h('span', { class: 'row', style: 'gap:6px' }, g.prev_level ? [levelPill(g.prev_level), '→'] : null, levelPill(g.level)) },
      { t: '期限', f: (g) => g.status === 'approved' ? fmtIn(g.expires_at) : lab('duration', g.duration) },
      { t: '申請者 / 理由', f: (g) => h('div', null, g.requester, h('div', { class: 'sub' }, g.reason || '—')) },
      { t: '狀態', f: (g) => h('div', null, grantPill(g.status), g.decided_by ? h('div', { class: 'sub' }, `${g.decided_by} · ${fmtAgo(g.decided_at)}${g.decision_note ? ' · ' + g.decision_note : ''}`) : null) },
      { t: '申請時間', f: (g) => fmtAgo(g.created_at) },
      { t: '', cls: 'act', f: (g) => !can('operator') ? null : g.status === 'pending' ? [btn('核准', () => approveGrant(g), 'primary'), btn('駁回', () => rejectGrant(g), 'danger')]
        : g.status === 'approved' ? btn('撤銷', async () => { if (await confirmBox('撤銷授予', `撤銷後「${g.target}」會恢復為 ${lab('level', g.prev_level)}。`, { danger: true, label: '撤銷' })) act(() => POST(`/admin/grants/${g.id}/revoke`), '已撤銷'); }, 'danger') : null },
    ], r.items, { empty: f.status === 'pending' ? '沒有待審批的申請' : '沒有紀錄' }), null, { flush: true });
  });
};
async function directGrantDialog() {
  let r;
  try { r = await GET('/admin/devices'); } catch (e) { toast(e.message, true); return; }
  const devs = (r.items || []).filter((d) => d.status === 'active');
  if (!devs.length) { toast('沒有可授權的裝置，請先註冊並核准裝置', true); return; }
  formDialog('直接授予等級', [
    { k: 'target', label: '裝置', type: 'select', options: devs.map((d) => [d.id, `${d.name}（目前 ${lab('level', d.level)}）`]) },
    { k: 'level', label: '新等級', type: 'select', value: 'member', options: Object.entries(L.level) },
    { k: 'duration', label: '期限', type: 'select', value: 'perm', options: Object.entries(L.duration), hint: '有期限的授予到期後自動恢復原等級' },
    { k: 'reason', label: '理由', placeholder: '選填' },
  ], async (v) => { await POST('/admin/grants', v); toast('已授予'); });
}
function accessLevels(el) {
  region(el, async () => {
    const r = await GET('/admin/levels');
    return h('div', { class: 'cards' }, (r.items || []).map((lv) => card(null, h('div', { style: 'display:grid;gap:10px' },
      h('div', { class: 'row' }, levelPill(lv.level), h('span', { class: 'right muted small' }, `${lv.devices} 台裝置`)),
      h('ul', { style: 'margin:0;padding-left:18px' }, (lv.capabilities || []).map((c) => h('li', null, lab('cap', c))))))));
  });
}
function accessUsers(el) {
  el.append(h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, '管理員：全部權限；操作員：可變更裝置、映射、審批；檢視者：唯讀；一般使用者：只能註冊自己的裝置。'),
    can('admin') ? h('button', { class: 'btn primary sm right', onclick: () => userDialog() }, icon('plus'), '新增使用者') : null));
  region(el, async () => {
    const r = await GET('/admin/users');
    return card(null, table([
      { t: '帳號', f: (u) => nameCell(u.username, u.display_name || u.email) },
      { t: '角色', f: (u) => rolePill(u.role) },
      { t: '狀態', f: (u) => u.disabled ? pill('已停用', 'warn') : pill('啟用', 'ok') },
      { t: '兩步驟驗證', f: (u) => u.mfa_enabled ? pill('已啟用', 'ok') : pill('未啟用', 'warn') },
      { t: '裝置', f: (u) => h('div', null, u.devices, h('div', { class: 'sub' }, Object.entries(u.levels || {}).map(([k, v]) => `${lab('level', k)} ${v}`).join(' · '))) },
      { t: '上次登入', f: (u) => fmtAgo(u.last_login_at) },
      { t: '', cls: 'act', f: (u) => can('admin') ? [btn('編輯', () => userDialog(u)),
        u.mfa_enabled ? btn('重設 MFA', async () => { if (await confirmBox('重設兩步驟驗證', `移除「${u.username}」的驗證器綁定？對方下次登入只需要密碼。`, { danger: true, label: '重設' })) act(() => POST(`/admin/users/${u.id}/reset-mfa`), '已重設'); }) : null,
        S.me.user && S.me.user.id === u.id ? null : btn('刪除', async () => { if (await confirmBox('刪除使用者', `刪除「${u.username}」？他的裝置不會被刪除，但會失去擁有者。`, { danger: true, label: '刪除' })) act(() => DEL('/admin/users/' + u.id), '已刪除'); }, 'danger')] : null },
    ], r.items), null, { flush: true });
  });
}
function userDialog(u) {
  const roles = Object.entries(L.role);
  formDialog(u ? `編輯「${u.username}」` : '新增使用者', [
    u ? null : { k: 'username', label: '帳號', required: true },
    { k: 'display_name', label: '顯示名稱', value: u && u.display_name },
    { k: 'email', label: 'Email', type: 'email', value: u && u.email },
    { k: 'role', label: '角色', type: 'select', value: (u && u.role) || 'viewer', options: roles },
    { k: 'password', label: u ? '重設密碼' : '密碼', type: 'password', hint: u ? '留空表示不變更' : '留空會自動產生一組' },
    u ? { k: 'disabled', label: '停用這個帳號', type: 'check', value: u.disabled } : null,
  ].filter(Boolean), async (v) => {
    const body = Object.assign({}, v);
    if (!body.password) delete body.password;
    if (u) { await PATCH('/admin/users/' + u.id, body); toast('已儲存'); return; }
    const r = await POST('/admin/users', body);
    if (r && r.password) showSecret('使用者已建立', [['帳號', r.user.username], ['初始密碼', r.password]], '請把初始密碼交給對方，並請他登入後立即變更。');
    else toast('已建立');
  });
}

// ======================================================================
// page: security
// ======================================================================
P.security = (el, tab) => {
  const cur = pageTabs(el, 'security', [['alerts', '告警'], ['logins', '登入紀錄'], ['keys', '裝置金鑰'], ['mfa', '兩步驟驗證'], ['tokens', 'API 權杖'], ['acl', '存取控制'], ['bans', '封鎖清單']], tab);
  ({ alerts: secAlerts, logins: secLogins, keys: secKeys, mfa: secMFA, tokens: secTokens, acl: secACL, bans: secBans })[cur](el);
};
function secAlerts(el) {
  const f = filt('alerts', { status: 'open', severity: '' });
  el.append(h('div', { class: 'toolbar' },
    seg([['open', '未處理'], ['acked', '已確認'], ['resolved', '已解決'], ['', '全部']], f.status, (v) => { f.status = v; route(); }),
    selectEl([['', '所有等級'], ['crit', '嚴重'], ['warn', '警告'], ['info', '資訊']], f.severity, (v) => { f.severity = v; reload(); })));
  const reload = region(el, async () => {
    const r = await GET('/admin/alerts' + qs(f));
    S.badges.security = r.open || 0; updateBadges();
    return card(null, table([
      { t: '等級', f: (a) => sevPill(a.severity) },
      { t: '告警', f: (a) => h('div', null, h('b', null, a.title), h('div', { class: 'sub' }, a.detail || '')) },
      { t: '類別', f: (a) => a.category },
      { t: '次數', f: (a) => a.count },
      { t: '狀態', f: (a) => pill(lab('astatus', a.status), { open: 'bad', acked: 'warn', resolved: 'ok' }[a.status]) },
      { t: '最後發生', f: (a) => fmtAgo(a.updated_at) },
      { t: '', cls: 'act', f: (a) => can('operator') && a.status !== 'resolved' ? [a.status === 'open' ? btn('確認', () => act(() => POST(`/admin/alerts/${a.id}/ack`), '已確認')) : null, btn('標記解決', () => act(() => POST(`/admin/alerts/${a.id}/resolve`), '已解決'), 'primary')] : null },
    ], r.items, { empty: '沒有告警' }), null, { flush: true });
  });
}
function secLogins(el) {
  const f = filt('logins', { kind: '', abnormal: false, failed: false, q: '' });
  el.append(h('div', { class: 'toolbar' },
    selectEl([['', '所有類型'], ['user', '控制台'], ['device', '裝置'], ['api', 'API 權杖']], f.kind, (v) => { f.kind = v; reload(); }),
    h('label', { class: 'check small' }, sw(f.failed, (c) => { f.failed = c; reload(); }), '只看失敗'),
    h('label', { class: 'check small' }, sw(f.abnormal, (c) => { f.abnormal = c; reload(); }), '只看異常'),
    searchBox(f.q, '帳號、裝置、IP', (v) => { f.q = v; reload(); }),
    can('operator') ? h('button', { class: 'btn sm right', onclick: () => formDialog('解除暫時封鎖', [{ k: 'ip', label: 'IP 位址', required: true }], async (v) => { await POST('/admin/security/unblock', { ip: v.ip.trim() }); toast('已解除'); }) }, '解除 IP 暫時封鎖') : null));
  const reload = region(el, async () => {
    const r = await GET('/admin/security/logins' + qs(f));
    return card(null, table([
      { t: '時間', f: (x) => fmtDate(x.at) },
      { t: '對象', f: (x) => nameCell(x.subject || '—', { user: '控制台', device: '裝置', api: 'API' }[x.kind] || x.kind) },
      { t: '方式', f: (x) => x.method },
      { t: 'IP', f: (x) => h('span', { class: 'mono' }, x.ip) },
      { t: '結果', f: (x) => h('div', null, x.success ? pill('成功', 'ok') : pill('失敗', 'bad'), x.abnormal ? [' ', pill('異常', 'warn')] : null) },
      { t: '說明', f: (x) => h('div', { class: 'small' }, x.reason || '', x.abnormal_reason ? h('div', { class: 'sub' }, x.abnormal_reason) : null) },
      { t: '', cls: 'act', f: (x) => !x.success && can('operator') ? btn('解除封鎖', () => act(() => POST('/admin/security/unblock', { ip: x.ip }), '已解除 ' + x.ip)) : null },
    ], r.items, { empty: '沒有紀錄' }), null, { flush: true });
  });
}
function secKeys(el) {
  region(el, async () => {
    const r = await GET('/admin/security/keys');
    return [h('div', { class: 'muted small' }, `身分金鑰超過 ${r.max_age_days} 天會標示為需要輪替。要求輪替後，裝置下次連線會自動產生新金鑰（舊金鑰簽署確認）。`),
      card(null, table([
        { t: '裝置', f: (k) => nameCell(k.device, k.did) },
        { t: '指紋', f: (k) => h('span', { class: 'mono small' }, k.fingerprint) },
        { t: '金鑰年齡', f: (k) => `${k.age_days} 天` },
        { t: '加密公鑰', f: (k) => k.has_box_key ? pill('有', 'ok') : pill('無', 'warn') },
        { t: '狀態', f: (k) => pill({ ok: '正常', stale: '需要輪替', rotation_pending: '已要求輪替' }[k.state] || k.state, { ok: 'ok', stale: 'warn', rotation_pending: 'info' }[k.state]) },
        { t: '', cls: 'act', f: (k) => can('operator') && k.state !== 'rotation_pending' ? btn('要求輪替', () => act(() => POST(`/admin/devices/${k.device_id}/rotate-key`), '已送出要求')) : null },
      ], r.items), null, { flush: true })];
  });
}
function secMFA(el) {
  region(el, async () => {
    const r = await GET('/admin/security/mfa');
    const me = S.me.user || {};
    return [
      h('div', { class: 'cards' }, statCard('已啟用', `${r.enabled} / ${r.console_users}`, '控制台使用者'),
        card(null, h('div', { style: 'display:grid;gap:8px' }, h('div', { class: 'row' }, h('b', null, '要求所有控制台使用者啟用'),
          h('span', { class: 'right' }, sw(r.required, async (c, inp) => {
            if (c && !me.mfa_enabled) { inp.checked = false; toast('請先在「我的帳戶」替自己啟用兩步驟驗證', true); return; }
            await act(() => PUT('/admin/security/mfa', { required: c }), c ? '已要求啟用' : '已取消要求');
          }, !can('admin')))), h('div', { class: 'hint' }, '開啟後，沒有啟用的使用者登入後必須先完成設定才能使用控制台。')))),
      card(`尚未啟用（${(r.missing || []).length}）`, table([
        { t: '帳號', f: (u) => u.username }, { t: '角色', f: (u) => rolePill(u.role) }, { t: '上次提醒', f: (u) => fmtAgo(u.reminded_at) },
      ], r.missing, { empty: '所有使用者都已啟用' }), can('operator') && (r.missing || []).length ? btn('全部提醒', () => act(() => POST('/admin/security/mfa/remind', {}), '已送出提醒（觸發 user.mfa_reminder Webhook）')) : null, { flush: true }),
    ];
  });
}
function tokenTable(items, revokePath) {
  return table([
    { t: '名稱', f: (t) => nameCell(t.name, t.prefix + '…') },
    { t: '權限', f: (t) => (t.scopes || []).map((sc) => pill({ read: '讀取', write: '寫入', admin: '管理' }[sc] || sc)) },
    { t: '擁有者', f: (t) => t.owner || '—' },
    { t: '狀態', f: (t) => pill({ active: '有效', revoked: '已撤銷', expired: '已過期' }[t.state] || t.state, { active: 'ok', revoked: '', expired: 'warn' }[t.state]) },
    { t: '到期', f: (t) => t.expires_at ? fmtDate(t.expires_at) : '不過期' },
    { t: '最後使用', f: (t) => t.last_used_at ? h('div', null, fmtAgo(t.last_used_at), h('div', { class: 'sub mono' }, t.last_used_ip)) : '從未使用' },
    { t: '建立', f: (t) => fmtDate(t.created_at) },
    { t: '', cls: 'act', f: (t) => t.state === 'active' ? btn('撤銷', async () => { if (await confirmBox('撤銷', `撤銷「${t.name}」？使用它的程式會立即失去存取權。`, { danger: true, label: '撤銷' })) act(() => DEL(revokePath + t.id), '已撤銷'); }, 'danger') : null },
  ], items, { empty: '沒有權杖' });
}
function tokenDialog(title, path, hint) {
  formDialog(title, [
    { k: 'name', label: '名稱', required: true, placeholder: '例如 grafana、備份腳本' },
    { k: 'scopes', label: '權限', type: 'multi', value: ['read'], options: [['read', '讀取'], ['write', '寫入（操作員）'], ['admin', '管理']], hint: '實際權限不會超過擁有者的角色' },
    { k: 'ttl_days', label: '有效天數', type: 'number', value: 90, min: 0, hint: '0 表示不過期' },
  ], async (v) => {
    if (!v.scopes.length) throw new Error('請至少選擇一項權限');
    const r = await POST(path, v);
    showSecret('權杖已建立', [['權杖', r.token]], '這個權杖只會顯示這一次。呼叫 API 時放在 Authorization: Bearer <權杖>。');
  }, { submitLabel: '建立', intro: hint ? h('div', { class: 'hint' }, hint) : null });
}
function secTokens(el) {
  el.append(h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, '個人 API 權杖代表你自己呼叫 API（例如自動化腳本、Prometheus 抓取 /metrics）。'),
    h('button', { class: 'btn primary sm right', onclick: () => tokenDialog('建立個人 API 權杖', '/admin/security/tokens') }, icon('plus'), '建立權杖')));
  region(el, async () => card(null, tokenTable((await GET('/admin/security/tokens')).items, '/admin/security/tokens/'), null, { flush: true }));
}
function secACL(el) {
  region(el, async () => {
    const r = await GET('/admin/security/acl');
    return [
      h('div', { class: 'notice info' }, '拒絕規則會封鎖該範圍的所有流量（API、裝置、frpc、已發布服務的訪客）；只要存在任何允許規則，控制台與 API 權杖就只能從允許的範圍存取。', h('div', { class: 'small', style: 'margin-top:4px' }, '你目前的 IP：', h('code', null, r.your_ip))),
      card('規則', table([
        { t: '動作', f: (a) => a.action === 'deny' ? pill('拒絕', 'bad') : pill('允許', 'ok') },
        { t: '範圍', f: (a) => h('span', { class: 'mono' }, a.cidr) },
        { t: '備註', f: (a) => a.note || '—' },
        { t: '建立', f: (a) => `${a.created_by} · ${fmtDate(a.created_at)}` },
        { t: '', cls: 'act', f: (a) => can('admin') ? btn('刪除', () => act(() => DEL('/admin/security/acl/' + a.id), '已刪除'), 'danger') : null },
      ], r.items, { empty: '沒有規則（所有位址都可以存取）' }), can('admin') ? h('button', { class: 'btn primary sm', onclick: () => formDialog('新增存取規則', [
        { k: 'action', label: '動作', type: 'select', value: 'deny', options: [['deny', '拒絕'], ['allow', '允許（限制控制台來源）']] },
        { k: 'cidr', label: 'IP 或 CIDR', required: true, placeholder: '203.0.113.0/24 或 198.51.100.7' },
        { k: 'note', label: '備註' },
      ], async (v) => {
        if (v.action === 'allow' && !(r.items || []).some((x) => x.action === 'allow') && !(await confirmBox('確認', `新增第一條允許規則後，只有 ${v.cidr} 可以使用控制台。請確認包含你目前的 IP（${r.your_ip}），否則你會被鎖在外面。`, { label: '我確定' }))) return false;
        await POST('/admin/security/acl', v); toast('已新增');
      }) }, icon('plus'), '新增規則') : null, { flush: true }),
    ];
  });
}
function secBans(el) {
  region(el, async () => {
    const r = await GET('/admin/security/bans');
    return card(null, table([
      { t: '裝置', f: (b) => nameCell(b.name, b.did) },
      { t: '原因', f: (b) => b.ban_reason || '—' },
      { t: '擁有者', f: (b) => b.user || '—' },
      { t: '最後 IP', f: (b) => h('span', { class: 'mono' }, b.ip || '—') },
      { t: '最後上線', f: (b) => fmtAgo(b.last_seen_at) },
      { t: '', cls: 'act', f: (b) => can('operator') ? btn('解除封鎖', () => act(() => POST(`/admin/devices/${b.id}/unban`), '已解除')) : null },
    ], r.items, { empty: '沒有被封鎖的裝置' }), null, { flush: true });
  });
}

// ======================================================================
// page: monitor
// ======================================================================
P.monitor = (el, tab) => {
  const cur = pageTabs(el, 'monitor', [['metrics', '指標'], ['failures', '連線失敗'], ['logs', '服務日誌'], ['audit', '稽核紀錄']], tab);
  ({ metrics: monMetrics, failures: monFailures, logs: monLogs, audit: monAudit })[cur](el);
};
function monMetrics(el) {
  const f = filt('metrics', { range: '1h' });
  el.append(h('div', { class: 'toolbar' }, seg([['live', '即時'], ['5m', '5 分鐘'], ['1h', '1 小時'], ['24h', '24 小時'], ['7d', '7 天']], f.range, (v) => { f.range = v; route(); })));
  const reload = region(el, async () => {
    const [r, p] = await Promise.all([GET('/admin/metrics' + qs({ range: f.range })), GET('/admin/metrics/p2p')]);
    const sr = r.series || {};
    const pts = (k) => sr[k] || [];
    const c = (title, series, opts) => card(title, lineChart(series, opts));
    const p2pItems = (p.items || []).map((x) => ({ t: (toDate(x.hour) || new Date()).getTime() / 1000, v: x.rate || 0 }));
    return [
      h('div', { class: 'cards' },
        statCard('CPU', fmtPct(r.latest.cpu), `負載 ${r.latest.load1}`, 'cpu', fmtPct),
        statCard('記憶體', fmtPct(r.latest.mem), `${fmtNum(r.latest.mem_used_mb)} / ${fmtNum(r.latest.mem_total_mb)} MB`, 'mem', fmtPct),
        statCard('磁碟', fmtPct(r.latest.disk), `剩餘 ${r.latest.disk_free_gb} GB`, 'disk', fmtPct),
        statCard('裝置 WebSocket', r.latest.ws_conns, `在線裝置 ${r.latest.devices_online}`, 'ws_conns', (v) => v),
        statCard('P2P 成功率（24h）', p.attempted ? fmtPct(p.rate) : '—', `${p.succeeded} / ${p.attempted} 次`)),
      h('div', { class: 'charts' },
        c('CPU / 記憶體 / 磁碟（%）', [{ name: 'CPU', points: pts('cpu') }, { name: '記憶體', points: pts('mem') }, { name: '磁碟', points: pts('disk') }], { fmt: (v) => Math.round(v) + '%', max: 100 }),
        c('網路', [{ name: '下載', points: pts('net_rx_bps') }, { name: '上傳', points: pts('net_tx_bps') }], { fmt: fmtRate, min: 1024 }),
        c('傳輸流量', [{ name: 'P2P', points: pts('p2p_bps') }, { name: '中繼', points: pts('relay_bps') }, { name: 'FRP', points: pts('frp_bps') }], { fmt: fmtRate, min: 1024 }),
        c('工作階段', [{ name: 'P2P', points: pts('sessions_p2p') }, { name: '中繼', points: pts('sessions_relay') }, { name: '協商中', points: pts('sessions_nego') }], { min: 3 }),
        c('在線裝置 / 在線映射', [{ name: '裝置', points: pts('devices_online') }, { name: 'FRP 映射', points: pts('frp_active') }], { min: 3 }),
        c('P2P 成功率（%，每小時）', [{ name: '成功率', points: pts('p2p_success').length ? pts('p2p_success') : p2pItems }], { fmt: (v) => Math.round(v) + '%', max: 100 })),
    ];
  });
  if (f.range === 'live') {
    let n = 0;
    S.liveHooks.push(() => { if (++n % 3 === 0 && !$('.overlay')) reload(); });
  }
}
function monFailures(el) {
  const f = filt('failures', { kind: '' });
  const sumBox = h('div', { class: 'cards' });
  el.append(sumBox, h('div', { class: 'toolbar' }, seg([['', '全部'], ['p2p', 'P2P'], ['relay', '中繼'], ['frp', 'FRP'], ['auth', '驗證']], f.kind, (v) => { f.kind = v; reload(); })));
  const reload = region(el, async () => {
    const r = await GET('/admin/failures' + qs(f));
    const l = r.last_24h || {};
    sumBox.replaceChildren(...['p2p', 'relay', 'frp', 'auth'].map((k) => statCard(`${lab('fkind', k)} 失敗（24h）`, fmtNum(l[k] || 0))));
    return card(null, table([
      { t: '時間', f: (x) => fmtDate(x.at) },
      { t: '類型', f: (x) => pill(lab('fkind', x.kind), { p2p: 'info', relay: 'violet', frp: 'warn', auth: 'bad' }[x.kind]) },
      { t: '對象', f: (x) => [x.a, x.b].filter(Boolean).join(' ↔ ') || '—' },
      { t: '原因', f: (x) => h('div', null, x.reason, x.detail ? h('div', { class: 'sub' }, typeof x.detail === 'string' ? x.detail : JSON.stringify(x.detail)) : null) },
    ], r.items, { empty: '沒有失敗紀錄' }), null, { flush: true });
  });
}
function monLogs(el) {
  const f = filt('logs', { level: '', source: '', q: '', follow: true });
  let rows = [];
  const tbody = h('tbody');
  const lvPill = (lv) => pill(lv, { error: 'bad', warn: 'warn', info: 'info', debug: '' }[lv]);
  const rowEl = (x) => h('tr', null, h('td', { class: 'nowrap small' }, fmtDate(x.ts)), h('td', null, lvPill(x.level)), h('td', { class: 'small' }, x.source || ''), h('td', { class: 'mono' }, x.msg));
  const more = btn('載入更早的紀錄', () => load(true));
  el.append(h('div', { class: 'toolbar' },
    selectEl([['', '所有層級'], ['error', 'error'], ['warn', 'warn 以上'], ['info', 'info 以上'], ['debug', 'debug']], f.level, (v) => { f.level = v; load(); }),
    searchBox(f.source, '來源（hub、frp、audit…）', (v) => { f.source = v; load(); }),
    searchBox(f.q, '關鍵字', (v) => { f.q = v; load(); }),
    h('label', { class: 'check small' }, sw(f.follow, (c) => { f.follow = c; }), '即時追蹤')));
  const tableBox = card(null, h('div', { class: 'tbl-wrap' }, h('table', { class: 'tbl' }, h('thead', null, h('tr', null, h('th', null, '時間'), h('th', null, '層級'), h('th', null, '來源'), h('th', null, '訊息'))), tbody)), null, { flush: true });
  el.append(tableBox, h('div', { class: 'row' }, more));
  async function load(older) {
    try {
      const q = Object.assign({}, f, { follow: '', limit: 200 });
      if (older && rows.length) q.before = rows[rows.length - 1].id;
      const r = await GET('/admin/logs' + qs(q));
      const items = r.items || [];
      rows = older ? rows.concat(items) : items;
      if (!older) tbody.replaceChildren();
      items.forEach((x) => tbody.append(rowEl(x)));
      if (!rows.length) tbody.replaceChildren(h('tr', null, h('td', { colspan: 4 }, empty('沒有紀錄'))));
      more.hidden = items.length < 200;
    } catch (e) { toast(e.message, true); }
  }
  const rank = { debug: 0, info: 1, warn: 2, error: 3 };
  S.reload = () => load();
  S.onEvent = (ev) => {
    if (ev.type !== 'log' || !f.follow) return;
    const x = ev.data || {};
    if (f.level && (rank[x.level] || 0) < (rank[f.level] || 0)) return;
    if (f.source && !(x.source || '').includes(f.source)) return;
    if (f.q && !(x.msg || '').toLowerCase().includes(f.q.toLowerCase())) return;
    const r = { id: x.id, ts: x.ts || ev.time, level: x.level, source: x.source, msg: x.msg };
    if (!rows.length) tbody.replaceChildren();
    rows.unshift(r);
    tbody.prepend(rowEl(r));
    if (tbody.children.length > 1000) tbody.lastChild.remove();
  };
  load();
}
function monAudit(el) {
  const f = filt('audit', { q: '', action: '', offset: 0 });
  el.append(h('div', { class: 'toolbar' },
    searchBox(f.action, '動作（例如 device.、frp.mapping）', (v) => { f.action = v; f.offset = 0; reload(); }),
    searchBox(f.q, '操作者、目標', (v) => { f.q = v; f.offset = 0; reload(); })));
  const reload = region(el, async () => {
    const r = await GET('/admin/audit' + qs(Object.assign({}, f, { limit: 100 })));
    const items = r.items || [];
    return [card(null, table([
      { t: '時間', f: (a) => fmtDate(a.at) },
      { t: '操作者', f: (a) => nameCell(a.actor, { user: '使用者', device: '裝置', token: 'API 權杖', system: '系統' }[a.actor_type] || a.actor_type) },
      { t: '動作', f: (a) => h('code', null, a.action) },
      { t: '目標', f: (a) => h('div', { class: 'small' }, a.target_type, a.target_id ? h('div', { class: 'sub mono' }, a.target_id) : null) },
      { t: '內容', f: (a) => { const t = a.detail && Object.keys(a.detail).length ? JSON.stringify(a.detail) : ''; return h('span', { class: 'mono small', title: t }, t.length > 90 ? t.slice(0, 90) + '…' : t); } },
      { t: 'IP', f: (a) => h('span', { class: 'mono small' }, a.ip || '') },
    ], items, { empty: '沒有紀錄' }), null, { flush: true }),
    h('div', { class: 'row' }, f.offset > 0 ? btn('上一頁', () => { f.offset = Math.max(0, f.offset - 100); reload(); }) : null,
      items.length === 100 ? btn('下一頁', () => { f.offset += 100; reload(); }) : null)];
  });
}

// ======================================================================
// page: system & API
// ======================================================================
P.system = (el, tab) => {
  const tabs = [['status', '服務狀態'], ['backups', '備份'], ['webhooks', 'Webhook'], ['apikeys', 'API 金鑰'], ['update', '線上更新'], ['settings', '系統設定'], ['endpoints', 'API 端點']];
  const cur = pageTabs(el, 'system', can('admin') ? tabs : tabs.filter((t) => t[0] !== 'apikeys'), tab);
  ({ status: sysStatus, backups: sysBackups, webhooks: sysWebhooks, apikeys: sysAPIKeys, update: sysUpdate, settings: sysSettings, endpoints: sysEndpoints })[cur](el);
};
function sysStatus(el) {
  region(el, async () => {
    const [st, ver, db] = await Promise.all([GET('/admin/system/status'), GET('/admin/system/versions'), GET('/admin/system/db')]);
    const dbBox = h('div');
    const drawDB = (d) => dbBox.replaceChildren(h('div', { class: 'kv' },
      h('div', null, '引擎'), h('div', null, d.engine), h('div', null, '結構版本'), h('div', null, d.schema_version),
      h('div', null, '檔案'), h('div', { class: 'mono small' }, d.path),
      h('div', null, '大小'), h('div', null, `${fmtBytes(d.pages * d.page_size)}（WAL ${fmtBytes(d.wal_bytes)}）`),
      d.integrity !== undefined ? [h('div', null, '完整性檢查'), h('div', null, d.integrity === 'ok' ? pill('ok', 'ok') : pill(String(d.integrity), 'bad'))] : null),
    h('div', { class: 'tbl-wrap', style: 'margin-top:10px' }, table([{ t: '資料表', f: (x) => x[0] }, { t: '筆數', f: (x) => fmtNum(x[1]) }], Object.entries(d.rows || {}))));
    drawDB(db);
    return [
      h('div', { class: 'cards' }, statCard('版本', st.version), statCard('執行時間', fmtDur(st.uptime_s)), statCard('公開網址', h('span', { class: 'small mono' }, st.public_url || '未設定'))),
      card('服務', table([
        { t: '服務', f: (x) => h('b', null, x.name) },
        { t: '類型', f: (x) => ({ core: '核心', frps: 'frps', external: '外部' }[x.kind] || x.kind) },
        { t: '狀態', f: (x) => pill(lab('nstatus', x.status), { running: 'ok', online: 'ok', external: 'info', unknown: '', offline: 'bad', crashed: 'bad' }[x.status]) },
        { t: '說明', f: (x) => h('span', { class: 'small' }, x.detail || '') },
        { t: '', cls: 'act', f: (x) => x.restartable && can('admin') ? btn('重新啟動', async () => { if (await confirmBox('重新啟動服務', `重新啟動「${x.name}」？相關連線會短暫中斷並自動重連。`, { label: '重新啟動' })) act(() => POST(`/admin/system/services/${encodeURIComponent(x.name)}/restart`), '已重新啟動'); }) : null },
      ], st.services), null, { flush: true }),
      h('div', { class: 'cols' },
        card('元件版本', table([{ t: '元件', f: (x) => x.name }, { t: '版本', f: (x) => h('span', { class: 'mono' }, x.version) }], ver.items), null, { flush: true }),
        card('資料庫', dbBox, btn('完整性檢查', async () => { try { drawDB(await GET('/admin/system/db?check=1')); } catch (e) { toast(e.message, true); } }))),
    ];
  });
}
function sysBackups(el) {
  region(el, async () => {
    const r = await GET('/admin/system/backups');
    const sch = r.schedule || {};
    return [
      h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, sch.hour_utc >= 0 ? `每天 UTC ${pad(sch.hour_utc)}:00 自動備份，保留最近 ${sch.keep} 份。存放於 ${r.dir}` : '已停用排程備份。', ' 可在「系統設定」調整。'),
        can('admin') ? h('button', { class: 'btn primary sm right', onclick: () => act(() => POST('/admin/system/backups'), '備份完成') }, '立即備份') : null),
      card(null, table([
        { t: '時間', f: (b) => fmtDate(b.created_at) },
        { t: '類型', f: (b) => b.kind === 'manual' ? '手動' : '排程' },
        { t: '檔案', f: (b) => h('span', { class: 'mono small' }, b.file) },
        { t: '大小', f: (b) => fmtBytes(b.size) },
        { t: '狀態', f: (b) => b.status === 'ok' ? pill('完成', 'ok') : h('div', null, pill(b.status, b.status === 'running' ? 'info' : 'bad'), b.error ? h('div', { class: 'sub err' }, b.error) : null) },
        { t: 'SHA-256', f: (b) => h('span', { class: 'mono small', title: b.sha256 }, (b.sha256 || '').slice(0, 12)) },
        { t: '', cls: 'act', f: (b) => can('admin') && b.status === 'ok' ? [h('a', { class: 'btn sm', href: `/api/v1/admin/system/backups/${encodeURIComponent(b.id)}/download`, download: b.file }, '下載'),
          btn('刪除', async () => { if (await confirmBox('刪除備份', `刪除 ${b.file}？`, { danger: true, label: '刪除' })) act(() => DEL('/admin/system/backups/' + b.id), '已刪除'); }, 'danger')] : null },
      ], r.items, { empty: '還沒有備份' }), null, { flush: true }),
    ];
  });
}
let hookTypes = [];
function sysWebhooks(el) {
  el.append(h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, '事件以 POST JSON 送出，並以 X-Meshport-Signature（HMAC-SHA256）簽章。'),
    can('admin') ? h('button', { class: 'btn primary sm right', onclick: () => webhookDialog() }, icon('plus'), '新增 Webhook') : null));
  region(el, async () => {
    const r = await GET('/admin/system/webhooks');
    hookTypes = r.event_types || [];
    return card(null, table([
      { t: '名稱 / 網址', f: (w) => nameCell(w.name || '—', w.url) },
      { t: '事件', f: (w) => h('span', { class: 'small' }, (w.events || []).join(', ')) },
      { t: '啟用', f: (w) => sw(w.enabled, (c) => act(() => PATCH('/admin/system/webhooks/' + w.id, { enabled: c }), c ? '已啟用' : '已停用'), !can('admin')) },
      { t: '最後投遞', f: (w) => w.last_delivery_at ? h('div', null, fmtAgo(w.last_delivery_at), h('div', { class: 'sub' }, w.last_error ? h('span', { class: 'err' }, w.last_error) : 'HTTP ' + w.last_status)) : '尚未投遞' },
      { t: '連續失敗', f: (w) => w.fail_count ? pill(String(w.fail_count), 'bad') : '0' },
      { t: '', cls: 'act', f: (w) => can('admin') ? [btn('測試', () => act(() => POST(`/admin/system/webhooks/${w.id}/test`), '已送出測試事件')), btn('編輯', () => webhookDialog(w)),
        btn('刪除', async () => { if (await confirmBox('刪除 Webhook', `刪除 ${w.url}？`, { danger: true, label: '刪除' })) act(() => DEL('/admin/system/webhooks/' + w.id), '已刪除'); }, 'danger')] : null },
    ], r.items, { empty: '還沒有 Webhook' }), null, { flush: true });
  });
}
function webhookDialog(w) {
  formDialog(w ? '編輯 Webhook' : '新增 Webhook', [
    { k: 'name', label: '名稱', value: w && w.name },
    { k: 'url', label: '網址', type: 'url', value: w && w.url, required: true, placeholder: 'https://example.com/hooks/meshport' },
    { k: 'events', label: '事件', type: 'multi', value: w ? w.events : ['*'], options: [['*', '全部']].concat(hookTypes.map((t) => [t, t])) },
    { k: 'enabled', label: '啟用', type: 'check', value: w ? w.enabled : true },
  ], async (v) => {
    if (!v.events.length) v.events = ['*'];
    if (w) { await PATCH('/admin/system/webhooks/' + w.id, v); toast('已儲存'); return; }
    const r = await POST('/admin/system/webhooks', v);
    showSecret('Webhook 已建立', [['簽章金鑰', r.secret]], '用這把金鑰驗證 X-Meshport-Signature。它只會顯示這一次。');
  }, { wide: true });
}
function sysAPIKeys(el) {
  el.append(h('div', { class: 'toolbar' }, h('span', { class: 'muted small' }, '服務 API 金鑰給整合系統使用，不綁定個人登入（擁有者為建立它的管理員）。'),
    h('button', { class: 'btn primary sm right', onclick: () => tokenDialog('建立服務 API 金鑰', '/admin/system/api-keys') }, icon('plus'), '建立金鑰')));
  region(el, async () => card(null, tokenTable((await GET('/admin/system/api-keys')).items, '/admin/system/api-keys/'), null, { flush: true }));
}
function sysUpdate(el) {
  region(el, async () => {
    const r = await GET('/admin/system/update');
    return card('線上更新', h('div', { style: 'display:grid;gap:12px' },
      h('div', { class: 'kv' }, h('div', null, '目前版本'), h('div', { class: 'mono' }, r.current), h('div', null, '更新頻道'), h('div', null, r.channel),
        h('div', null, '最新版本'), h('div', { class: 'mono' }, r.latest || r.version || '—'), h('div', null, '自動更新'), h('div', null, r.auto_update ? '開啟' : '關閉')),
      r.error ? h('div', { class: 'notice' }, '無法檢查更新：' + r.error) : null,
      r.notes ? h('pre', { class: 'code' }, r.notes) : null,
      r.available ? h('div', { class: 'row' }, h('span', null, '有新版本可以安裝。'), can('admin') ? h('button', { class: 'btn primary', onclick: async () => {
        if (await confirmBox('安裝更新', '下載並驗證簽章後會替換 meshportd 並重新啟動，期間所有連線會中斷數秒。', { label: '安裝並重新啟動' })) act(() => POST('/admin/system/update/install'), '更新已開始，服務將重新啟動');
      } }, '安裝更新') : null) : (!r.error ? h('div', { class: 'muted' }, '已是最新版本。') : null)),
    btn('重新檢查', () => S.reload()));
  });
}
function sysSettings(el) {
  region(el, async () => {
    const r = await GET('/admin/system/settings');
    const st = r.settings || {};
    const ro = !can('admin');
    const F = [
      ['一般', [
        { k: 'console_name', label: '控制台名稱', value: st.console_name },
        { k: 'theme', label: '預設外觀', type: 'select', value: st.theme, options: [['system', '跟隨系統'], ['light', '淺色'], ['dark', '深色']] },
        { k: 'accent', label: '主題色', type: 'select', value: st.accent, options: [['jade', '玉綠'], ['terracotta', '赤陶'], ['rose', '玫瑰'], ['olive', '橄欖'], ['graphite', '石墨']] },
        { k: 'language', label: '語言', type: 'select', value: st.language, options: [['zh-TW', '繁體中文'], ['ja', '日本語'], ['en', 'English']], hint: '目前控制台介面只提供繁體中文' },
      ]],
      ['裝置註冊與探索', [
        { k: 'open_registration', label: '允許裝置用帳號密碼註冊', type: 'check', value: st.open_registration },
        { k: 'registration_approval', label: '帳密註冊的審批方式', type: 'select', value: st.registration_approval, options: [['manual', '需要管理員核准'], ['auto', '自動核准']] },
        { k: 'default_level', label: '新裝置的預設等級', type: 'select', value: st.default_level, options: Object.entries(L.level) },
        { k: 'discovery_scope', label: '探索範圍', type: 'select', value: st.discovery_scope, options: [['all', '所有裝置'], ['group', '同群組'], ['user', '同擁有者']], hint: '管理級裝置不受限制' },
        { k: 'device_token_hours', label: '裝置權杖有效時間（小時）', type: 'number', value: st.device_token_hours, min: 1 },
      ]],
      ['通訊', [
        { k: 'relay_enabled', label: '允許伺服器中繼（訊息與檔案）', type: 'check', value: st.relay_enabled },
        { k: 'relay_max_file_mb', label: '中繼檔案大小上限（MB）', type: 'number', value: st.relay_max_file_mb, min: 1 },
        { k: 'message_retention_days', label: '中繼訊息保留天數', type: 'number', value: st.message_retention_days, min: 1 },
        { k: 'file_retention_hours', label: '中繼檔案保留時數', type: 'number', value: st.file_retention_hours, min: 1 },
      ]],
      ['安全', [
        { k: 'session_hours', label: '控制台登入有效時間（小時）', type: 'number', value: st.session_hours, min: 1 },
        { k: 'abnormal_fail_limit', label: '每個 IP 10 分鐘內失敗幾次視為異常並暫時封鎖', type: 'number', value: st.abnormal_fail_limit, min: 1 },
        { k: 'mfa_required', label: '要求控制台使用者啟用兩步驟驗證', type: 'check', value: st.mfa_required },
      ]],
      ['維運', [
        { k: 'log_retention_days', label: '日誌保留天數', type: 'number', value: st.log_retention_days, min: 1 },
        { k: 'backup_hour', label: '每日備份時間（UTC 小時，-1 停用）', type: 'number', value: st.backup_hour, min: -1, max: 23 },
        { k: 'backup_keep', label: '保留備份份數', type: 'number', value: st.backup_keep, min: 1 },
        { k: 'update_channel', label: '更新頻道', type: 'select', value: st.update_channel, options: [['stable', '穩定版'], ['beta', '測試版']] },
        { k: 'auto_update', label: '自動安裝更新', type: 'check', value: st.auto_update },
      ]],
    ];
    const inputs = {};
    const sections = F.map(([title, fields]) => card(title, h('div', { class: 'grid2' }, fields.map((f) => {
      let el;
      if (f.type === 'check') {
        el = h('input', { type: 'checkbox', checked: !!f.value, disabled: ro });
        inputs[f.k] = el;
        return h('label', { class: 'check', style: 'grid-column:1/-1' }, el, f.label);
      }
      if (f.type === 'select') { el = h('select', { class: 'input', disabled: ro }, f.options.map(([v, t]) => h('option', { value: v }, t))); el.value = f.value; }
      else { el = h('input', { class: 'input', type: f.type || 'text', min: f.min, max: f.max, disabled: ro }); el.value = f.value === undefined ? '' : f.value; }
      inputs[f.k] = el;
      return h('label', { class: 'field' }, h('span', null, f.label), el, f.hint ? h('span', { class: 'hint' }, f.hint) : null);
    }))));
    const save = async () => {
      const body = {};
      for (const [, fields] of F) for (const f of fields) {
        const el = inputs[f.k];
        const v = f.type === 'check' ? el.checked : f.type === 'number' ? Number(el.value) : el.value;
        if (v !== st[f.k]) body[f.k] = v;
      }
      if (!Object.keys(body).length) { toast('沒有變更'); return; }
      const nst = await act(() => PATCH('/admin/system/settings', body), '設定已儲存');
      if (nst) { S.settings = Object.assign({}, S.settings, nst); applyTheme(); }
    };
    return [ro ? h('div', { class: 'notice info' }, '只有管理員可以變更系統設定。') : null, ...sections,
      ro ? null : h('div', { class: 'row' }, h('button', { class: 'btn primary', onclick: save }, '儲存設定'), h('button', { class: 'btn', onclick: () => S.reload() }, '還原'))];
  });
}
function sysEndpoints(el) {
  const f = filt('endpoints', { q: '' });
  el.append(h('div', { class: 'toolbar' }, searchBox(f.q, '路徑或說明', (v) => { f.q = v; reload(); }),
    h('a', { class: 'btn sm right', href: '/api/docs', target: '_blank', rel: 'noopener' }, icon('book'), 'API 文件'),
    h('a', { class: 'btn sm', href: '/api/v1/openapi.json', target: '_blank', rel: 'noopener' }, 'OpenAPI 3.1')));
  const reload = region(el, async () => {
    const r = await GET('/admin/system/endpoints');
    const q = f.q.toLowerCase();
    const items = (r.items || []).filter((x) => !q || (x.path + ' ' + x.summary + ' ' + x.tag).toLowerCase().includes(q));
    return card(`${items.length} 個端點`, table([
      { t: '方法', f: (x) => pill(x.method, { GET: 'info', POST: 'ok', PATCH: 'warn', PUT: 'warn', DELETE: 'bad' }[x.method]) },
      { t: '路徑', f: (x) => h('code', null, x.path) },
      { t: '權限', f: (x) => pill({ public: '公開', device: '裝置', viewer: '檢視者', operator: '操作員', admin: '管理員', any: '裝置或檢視者', session: '登入使用者' }[x.auth] || x.auth) },
      { t: '分類', f: (x) => x.tag },
      { t: '說明', f: (x) => h('span', { class: 'small' }, x.summary) },
    ], items), null, { flush: true });
  });
}

// ======================================================================
// start
// ======================================================================
if (location.pathname === '/enroll') showEnroll();
else boot();
