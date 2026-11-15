/* Meshport console – application logic.
 *
 * Feeds the approved console design (admin.tpl.html, rendered by dc.js) with
 * live data from the meshportd admin API (/api/v1/admin/*) and the console
 * event stream (/api/v1/admin/stream). The structure of adminVals() follows
 * the design's own view model so every card, table and dialog keeps its look.
 */
(function () {
  'use strict';

  const T = window.MP_THEME;
  const I = Object.assign(T.icons(), {
    logout: 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4 M16 17l5-5-5-5 M21 12H9',
    terminal: 'M4 17l6-6-6-6 M12 19h8',
  });
  const DICT = T.dict();
  Object.keys(DICT).forEach((k) => Object.assign(DICT[k], (window.MP_EXTRA || {})[k] || {}));
  DICT.zhCN = window.MP_ZH_CN;
  const LANG_IN = { 'zh-CN': 'zhCN', 'zh-Hans': 'zhCN', 'zh-TW': 'zh', zh: 'zh', 'zh-Hant': 'zh', ja: 'ja', 'ja-JP': 'ja', en: 'en', 'en-US': 'en' };
  const LANG_OUT = { zhCN: 'zh-CN', zh: 'zh-TW', ja: 'ja', en: 'en' };
  const FONTS = {
    zhCN: 'Inter, "Noto Sans SC", "Microsoft YaHei", "PingFang SC", system-ui, sans-serif',
    zh: 'Inter, "Noto Sans TC", "PingFang TC", "Microsoft JhengHei", system-ui, sans-serif',
    ja: 'Inter, "Noto Sans JP", "Hiragino Sans", "Yu Gothic", system-ui, sans-serif',
    en: 'Inter, system-ui, -apple-system, "Segoe UI", sans-serif',
  };
  const LANG_TAGS = { zhCN: 'zh-Hans-CN', zh: 'zh-Hant-TW', ja: 'ja', en: 'en' };
  const PAGES = ['overview', 'devices', 'comms', 'frp', 'access', 'security', 'monitor', 'system'];
  const RANK = { viewer: 1, operator: 2, admin: 3 };

  function pref(k, v) {
    try {
      if (v === undefined) return localStorage.getItem(k);
      if (v === null) localStorage.removeItem(k); else localStorage.setItem(k, v);
    } catch (_) { /* storage unavailable */ }
    return null;
  }

  // ---------------------------------------------------------------- state
  const ST = {
    view: 'boot', authMode: 'login', page: 'overview', overlay: null,
    lang: null, theme: null,
    q: '', devFilter: 'all', commFilter: 'all', frpFilter: 'all', accFilter: 'pending', loginFilter: 'all',
    aclMode: 'allow', aclDraft: '', range: '24h', logLv: 'all', paused: false, hov: null,
    toast: '', cf: null, fm: null, mf: null, drawerId: null, busy: {}, pageErr: '',
    au: { user: '', pw: '', totp: '', needTotp: false, err: '', busy: false, mfa: null },
  };
  const D = { me: null, settings: {}, dev: {} };
  const LIVE = { ring: { p2p_bps: [], relay_bps: [] }, last: null, wsOn: false, ws: null, retry: 0, statusAt: 0 };
  let logs = [];
  let R = null;
  let raf = 0;

  const S = () => DICT[ST.lang] || DICT.en;
  const F = (str, o) => String(str).replace(/\{(\w+)\}/g, (m, k) => (o[k] !== undefined ? o[k] : m));
  const role = () => (D.me && D.me.role) || 'viewer';
  const can = (r) => (RANK[role()] || 0) >= RANK[r];
  const isAdmin = () => can('admin');
  const dark = () => (ST.theme === 'dark' ? true : ST.theme === 'light' ? false : !!(window.matchMedia && matchMedia('(prefers-color-scheme: dark)').matches));
  const accentKey = () => (T.accents()[D.settings.accent] ? D.settings.accent : 'jade');

  function render() {
    if (raf) return;
    raf = requestAnimationFrame(() => { raf = 0; draw(); });
  }
  function set(o) { Object.assign(ST, o); render(); }
  function draw() {
    if (!R) return;
    const c = T.pal(dark(), accentKey());
    document.body.style.background = c.bg;
    document.documentElement.style.colorScheme = dark() ? 'dark' : 'light';
    document.documentElement.lang = LANG_TAGS[ST.lang] || 'en';
    if (ST.view === 'admin') R.render('admin', adminVals(c));
    else if (ST.view === 'auth') R.render('auth', authVals(c));
  }
  let toastT = 0;
  function flash(msg) {
    clearTimeout(toastT);
    set({ toast: msg });
    toastT = setTimeout(() => set({ toast: '' }), 2600);
  }

  // ---------------------------------------------------------------- API
  async function api(method, path, body, o) {
    o = o || {};
    const opt = { method, credentials: 'same-origin', headers: { 'X-Requested-With': 'meshport-console' } };
    if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    const res = await fetch('/api/v1' + path, opt);
    const txt = await res.text();
    let data = null;
    if (!o.text || !res.ok) { try { data = txt ? JSON.parse(txt) : null; } catch (_) { data = null; } }
    if (!res.ok) {
      const er = (data && data.error) || {};
      const e = new Error(er.message || ('HTTP ' + res.status));
      e.status = res.status;
      e.code = er.code || '';
      if (!o.noAuth) {
        if (res.status === 401) toLogin(S().sessionsExpired);
        else if (res.status === 403 && e.code === 'mfa_required') toMFA();
      }
      throw e;
    }
    return o.text ? txt : data;
  }
  const GET = (p) => api('GET', '/admin/' + p);
  async function act(fn, okMsg, after) {
    try {
      const r = await fn();
      if (okMsg) flash(typeof okMsg === 'function' ? okMsg(r) : okMsg);
      if (after) after(r);
      reloadSoon(150);
      return r;
    } catch (e) {
      flash(e.message);
      return null;
    }
  }
  async function copy(text) {
    const s = S();
    try {
      await navigator.clipboard.writeText(text);
      flash(s.copied);
    } catch (_) {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
      document.body.appendChild(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand('copy'); } catch (_e) { ok = false; }
      ta.remove();
      flash(ok ? s.copied : s.copyFail);
    }
  }

  // ---------------------------------------------------------------- formatting
  const pad = (x) => (x < 10 ? '0' : '') + x;
  const toDate = (v) => (v ? new Date(v) : null);
  function fmtTime(v) { const d = toDate(v); return d ? pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) : '—'; }
  function fmtDate(v) { const d = toDate(v); return d ? d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) : '—'; }
  function fmtFull(v) { const d = toDate(v); return d ? fmtDate(v) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) : '—'; }
  function fmtClock(v) { const d = toDate(v); return d ? pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds()) : ''; }
  function minsAgo(v) { const d = toDate(v); return d ? Math.max(0, Math.round((Date.now() - d.getTime()) / 60000)) : null; }
  function ago(v) {
    const s = S();
    const m = minsAgo(v);
    if (m == null) return '—';
    return m <= 1 ? s.justNow : m < 60 ? F(s.minAgo, { n: m }) : m < 1440 ? F(s.hrAgo, { n: Math.round(m / 60) }) : F(s.dayAgo, { n: Math.round(m / 1440) });
  }
  function fmtBytes(n) {
    n = Number(n) || 0;
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i && n < 10 ? n.toFixed(1) : Math.round(n)) + ' ' + u[i];
  }
  function fmtRate(bps) {
    const kb = (Number(bps) || 0) / 1000;
    return kb >= 1000 ? (kb / 1000).toFixed(1) + ' MB/s' : Math.round(kb) + ' KB/s';
  }
  const mbps = (bps) => ((Number(bps) || 0) * 8) / 1e6;
  const fmtMbps = (v) => (v >= 10 ? Math.round(v) : v >= 1 ? v.toFixed(1) : v.toFixed(2)) + ' Mbps';
  function fmtDur(sec) {
    sec = Math.max(0, Math.floor(Number(sec) || 0));
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return d ? d + 'd ' + h + 'h' : h ? h + 'h ' + m + 'm' : m + 'm';
  }
  function niceMax(v, floor) {
    v = Math.max(v * 1.15, floor);
    const p = Math.pow(10, Math.floor(Math.log10(v)));
    for (const k of [1, 2, 2.5, 5, 10]) if (k * p >= v) return k * p;
    return 10 * p;
  }
  function shortUA(ua) {
    ua = String(ua || '');
    if (!ua) return '—';
    const b = /Edg\/(\d+)/.exec(ua) ? 'Edge ' + /Edg\/(\d+)/.exec(ua)[1] : /Firefox\/(\d+)/.exec(ua) ? 'Firefox ' + /Firefox\/(\d+)/.exec(ua)[1]
      : /Chrome\/(\d+)/.exec(ua) ? 'Chrome ' + /Chrome\/(\d+)/.exec(ua)[1] : /Version\/(\d+).*Safari/.exec(ua) ? 'Safari ' + /Version\/(\d+)/.exec(ua)[1] : '';
    const os = /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Windows/.test(ua) ? 'Windows' : /Mac OS X/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : '';
    if (b) return os ? b + ' · ' + os : b;
    return ua.split(' ')[0].slice(0, 40);
  }
  const axNum = (v) => (v >= 100 || v === 0 ? String(Math.round(v)) : v >= 10 ? String(Math.round(v)) : v.toFixed(1));
  const items = (k) => (D[k] && D[k].items) || [];

  // ---------------------------------------------------------------- preferences
  function initPrefs() {
    const l = pref('mp.lang');
    ST.lang = l && DICT[l] ? l : 'en';
    ST.theme = pref('mp.theme') || null;
  }
  function applySettings() {
    if (!pref('mp.lang')) ST.lang = LANG_IN[D.settings.language] || ST.lang;
    if (!pref('mp.theme')) ST.theme = D.settings.theme === 'dark' || D.settings.theme === 'light' ? D.settings.theme : null;
    document.title = (D.settings.console_name || 'Meshport') + ' · ' + S().consoleName;
  }
  function cycleLang() {
    const next = { zh: 'zhCN', zhCN: 'ja', ja: 'en', en: 'zh' }[ST.lang] || 'zh';
    pref('mp.lang', next);
    set({ lang: next });
    applySettings();
  }
  function toggleTheme() {
    const next = dark() ? 'light' : 'dark';
    pref('mp.theme', next);
    set({ theme: next });
  }

  // ---------------------------------------------------------------- auth flow
  function toLogin(msg) {
    stopStream();
    stopTimers();
    D.me = null;
    set({ view: 'auth', authMode: 'login', overlay: null, cf: null, au: Object.assign({}, ST.au, { err: msg || '', busy: false, needTotp: false, pw: '', totp: '' }) });
  }
  function toMFA() {
    stopStream();
    stopTimers();
    set({ view: 'auth', authMode: 'mfa', overlay: null, cf: null, au: Object.assign({}, ST.au, { err: '', busy: false, mfa: null, totp: '' }) });
  }
  const setAu = (o) => set({ au: Object.assign({}, ST.au, o) });
  async function submitLogin() {
    const a = ST.au;
    if (a.busy) return;
    if (!a.user.trim() || !a.pw) { setAu({ err: S().needCreds }); return; }
    setAu({ busy: true, err: '' });
    try {
      const r = await api('POST', '/auth/login', { username: a.user.trim(), password: a.pw, totp: a.needTotp ? a.totp.trim() : '' }, { noAuth: true });
      setAu({ pw: '', totp: '', busy: false, needTotp: false });
      await enterApp(r && r.mfa_setup_required);
    } catch (e) {
      if (e.code === 'mfa_required') setAu({ needTotp: true, busy: false, err: a.needTotp ? e.message : '' });
      else setAu({ busy: false, err: e.message });
    }
  }
  async function startMFA() {
    try {
      const r = await api('POST', '/auth/mfa/setup', {}, { noAuth: true });
      setAu({ mfa: r, err: '' });
    } catch (e) {
      if (e.status === 401) toLogin(S().sessionsExpired); else setAu({ err: e.message });
    }
  }
  async function submitMFA() {
    const a = ST.au;
    if (a.busy) return;
    setAu({ busy: true, err: '' });
    try {
      await api('POST', '/auth/mfa/enable', { code: a.totp.trim() }, { noAuth: true });
      setAu({ busy: false, mfa: null, totp: '' });
      flash(S().tMfaOn);
      await enterApp();
    } catch (e) {
      setAu({ busy: false, err: e.message });
    }
  }
  async function logout() {
    try { await api('POST', '/auth/logout', {}, { noAuth: true }); } catch (_) { /* already signed out */ }
    toLogin(S().loggedOut);
  }

  function authVals(c) {
    const s = S();
    const a = ST.au;
    const mode = ST.authMode;
    const au = { isLogin: mode === 'login', isMfa: mode === 'mfa', isEnroll: mode === 'enroll', user: a.user, pw: a.pw, totp: a.totp, needTotp: a.needTotp, err: a.err, hasErr: !!a.err, busy: a.busy ? 'true' : 'false' };
    au.setUser = (e) => setAu({ user: e.target.value });
    au.setPw = (e) => setAu({ pw: e.target.value });
    au.setTotp = (e) => setAu({ totp: e.target.value });
    const row = (label, value) => ({ label, value, copy: () => copy(value) });
    if (mode === 'login') {
      Object.assign(au, { title: s.loginTitle, sub: s.loginSub, btn: a.busy ? s.loggingIn : s.loginBtn, submit: submitLogin });
    } else if (mode === 'mfa') {
      Object.assign(au, { title: s.mfaTitle, sub: s.mfaSub, mfaWaiting: !a.mfa, mfaStarted: !!a.mfa, startMfa: startMFA, submit: submitMFA,
        rows: a.mfa ? [row(s.mfaSecret, a.mfa.secret), row(s.mfaUri, a.mfa.otpauth_uri)] : [] });
    } else {
      const tok = decodeURIComponent(location.hash.replace(/^#/, ''));
      const srv = location.origin;
      Object.assign(au, { title: s.enrollTitle, sub: s.enrollSub, err: tok ? '' : s.badLink, hasErr: !tok,
        rows: tok ? [row(s.serverUrl, srv), row(s.enrollCode, tok), row(s.cliCmd, 'meshport setup --server ' + srv + ' --enroll ' + tok)] : [] });
    }
    return {
      c, s, I, au, font: FONTS[ST.lang], langTag: LANG_TAGS[ST.lang], brand: D.settings.console_name || 'Meshport',
      themeIcon: dark() ? I.sun : I.moon, cycleLang, toggleTheme, logout, hasToast: !!ST.toast, toast: ST.toast,
    };
  }

  // ---------------------------------------------------------------- boot & routing
  function route() {
    const p = location.hash.replace(/^#\/?/, '').split('/')[0];
    return PAGES.indexOf(p) >= 0 ? p : 'overview';
  }
  function go(p) {
    return () => {
      if (location.hash !== '#/' + p) location.hash = '#/' + p;
      else set({ page: p, overlay: null, hov: null });
    };
  }
  window.addEventListener('hashchange', () => {
    if (ST.view !== 'admin') return;
    const p = route();
    if (p !== ST.page) { set({ page: p, overlay: null, hov: null, pageErr: '' }); loadPage(); }
  });

  async function boot() {
    R = DC.mount(document.getElementById('app'));
    initPrefs();
    try {
      await DC.load('/admin.tpl.html');
    } catch (e) {
      document.getElementById('app').textContent = 'Meshport console failed to load: ' + e.message;
      return;
    }
    if (window.matchMedia) matchMedia('(prefers-color-scheme: dark)').addEventListener('change', render);
    if (location.pathname.replace(/\/+$/, '') === '/enroll') { set({ view: 'auth', authMode: 'enroll' }); return; }
    await enterApp();
  }
  async function enterApp(mfaSetup) {
    let me;
    try {
      me = await api('GET', '/auth/me', undefined, { noAuth: true });
    } catch (e) {
      if (e.status === 403 && e.code === 'mfa_required') toMFA(); else toLogin(e.status === 401 ? '' : e.message);
      return;
    }
    D.me = me;
    D.settings = me.settings || {};
    applySettings();
    if (mfaSetup) { toMFA(); return; }
    set({ view: 'admin', page: route(), overlay: null, pageErr: '' });
    startStream();
    startTimers();
    loadGlobal();
    loadPage();
  }

  // ---------------------------------------------------------------- data loading
  function pagePaths(p) {
    switch (p) {
      case 'overview': return ['overview', 'metrics/p2p'];
      case 'devices': return ['devices', 'groups', 'users'];
      case 'comms': return ['sessions'];
      case 'frp': return ['frp/nodes', 'frp/clients', 'frp/mappings', 'devices'];
      case 'access': return ['grants', 'levels', 'users', 'devices'];
      case 'security': return ['security/logins', 'alerts', 'security/keys', 'security/mfa', 'security/tokens', 'security/acl', 'security/bans', 'users'];
      case 'monitor': return ['metrics?range=' + ST.range, 'metrics/p2p', 'failures', 'logs?limit=200', 'alerts'];
      case 'system': return ['system/status', 'system/versions', 'system/db', 'system/backups', 'system/webhooks', 'system/update', 'system/settings', 'system/endpoints', 'metrics?range=live']
        .concat(isAdmin() ? ['system/api-keys'] : []);
      default: return [];
    }
  }
  const keyOf = (p) => p.split('?')[0];
  let loadSeq = 0;
  async function loadPage() {
    const page = ST.page;
    const seq = ++loadSeq;
    const paths = pagePaths(page);
    const res = await Promise.allSettled(paths.map((p) => GET(p)));
    if (seq !== loadSeq || ST.page !== page || ST.view !== 'admin') return;
    let err = '';
    res.forEach((r, i) => {
      const k = keyOf(paths[i]);
      if (r.status === 'fulfilled') D[k] = r.value;
      else if (r.reason && r.reason.status !== 401) err = err || r.reason.message;
    });
    if (page === 'monitor' && D.logs) logs = (D.logs.items || []).slice();
    if (D['system/settings']) D.settings = Object.assign({}, D.settings, D['system/settings'].settings);
    if (D['system/status']) { D.status = D['system/status']; LIVE.statusAt = Date.now(); }
    if (D['system/update']) D.update = D['system/update'];
    if (D.overview && page === 'overview') seedRing(D.overview.series);
    if (ST.overlay === 'drawer' && ST.drawerId) loadDevice(ST.drawerId);
    set({ pageErr: err });
  }
  async function loadGlobal() {
    const [ov, st, up] = await Promise.allSettled([GET('overview'), GET('system/status'), D.update ? Promise.resolve(D.update) : GET('system/update')]);
    if (ov.status === 'fulfilled') { D.overview = ov.value; seedRing(ov.value.series); }
    if (st.status === 'fulfilled') { D.status = st.value; LIVE.statusAt = Date.now(); }
    if (up.status === 'fulfilled') D.update = up.value;
    render();
  }
  async function loadDevice(id) {
    try {
      D.dev[id] = await GET('devices/' + encodeURIComponent(id));
      render();
    } catch (e) {
      if (e.status === 404) set({ overlay: null, drawerId: null });
    }
  }
  let reloadT = 0;
  function reloadSoon(ms) {
    clearTimeout(reloadT);
    reloadT = setTimeout(() => { loadPage(); if (ST.page !== 'overview') loadGlobal(); }, ms == null ? 900 : ms);
  }
  function seedRing(series) {
    if (!series) return;
    for (const k of ['p2p_bps', 'relay_bps']) {
      if (LIVE.ring[k].length >= 2 || !series[k]) continue;
      LIVE.ring[k] = series[k].slice(-30).map((p) => p.v);
    }
  }
  let timers = [];
  function startTimers() {
    stopTimers();
    timers.push(setInterval(() => { if (!document.hidden) loadPage(); }, 15000));
    timers.push(setInterval(() => { if (!document.hidden) loadGlobal(); }, 60000));
    timers.push(setInterval(() => { if (!LIVE.wsOn) render(); }, 2000));
  }
  function stopTimers() { timers.forEach(clearInterval); timers = []; }

  // ---------------------------------------------------------------- live stream
  const PAGE_EVENTS = {
    overview: ['device.', 'grant.', 'alert.', 'frp.', 'session.'],
    devices: ['device.', 'grant.'], comms: ['session.'], frp: ['frp.', 'device.online', 'device.offline'],
    access: ['grant.', 'device.updated', 'user.'], security: ['alert.', 'security.', 'device.', 'user.'],
    system: ['backup.', 'system.'], monitor: ['failure', 'alert.'],
  };
  function startStream() {
    stopStream();
    let ws;
    try { ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/v1/admin/stream'); } catch (_) { return; }
    LIVE.ws = ws;
    ws.onopen = () => { LIVE.wsOn = true; LIVE.retry = 0; render(); };
    ws.onclose = () => {
      LIVE.wsOn = false;
      render();
      if (LIVE.ws !== ws || ST.view !== 'admin') return;
      LIVE.retry = Math.min(LIVE.retry + 1, 6);
      setTimeout(() => { if (LIVE.ws === ws && ST.view === 'admin') startStream(); }, 1000 * Math.pow(2, LIVE.retry - 1));
    };
    ws.onmessage = (m) => {
      let ev;
      try { ev = JSON.parse(m.data); } catch (_) { return; }
      onEvent(ev);
    };
  }
  function stopStream() {
    if (LIVE.ws) { const w = LIVE.ws; LIVE.ws = null; try { w.close(); } catch (_) { /* closed */ } }
    LIVE.wsOn = false;
  }
  function onEvent(ev) {
    const t = ev.type || '';
    if (t === 'metrics.tick') {
      const d = ev.data || {};
      LIVE.last = d;
      for (const k of ['p2p_bps', 'relay_bps']) {
        const r = LIVE.ring[k];
        r.push(Number(d[k]) || 0);
        while (r.length > 30) r.shift();
      }
      render();
      return;
    }
    if (t === 'log') {
      if (!ST.paused && ev.data) {
        logs.unshift(ev.data);
        if (logs.length > 400) logs.length = 400;
        if (ST.page === 'monitor') render();
      }
      return;
    }
    const interest = PAGE_EVENTS[ST.page] || [];
    if (interest.some((p) => t.indexOf(p) === 0)) reloadSoon();
    else if (/^(device\.|grant\.|alert\.)/.test(t)) { clearTimeout(LIVE.gt); LIVE.gt = setTimeout(loadGlobal, 900); }
  }

  // ---------------------------------------------------------------- dialogs
  function confirmBox(title, body, ok, fn) { set({ cf: { title, body, ok, fn } }); }
  function closeOverlay() { set({ overlay: null, fm: null, mf: null }); }

  // Generic form sheet. cfg: {title, desc, fields:[{k,label,type,…}], values, saveLabel, onSave(v) -> false keeps it open, extra:[{label, go, fg}], noCancel}
  function openForm(cfg) {
    set({ overlay: 'form', fm: { cfg, v: Object.assign({}, cfg.values || {}), err: '', busy: false } });
  }
  function infoSheet(title, desc, rows) {
    openForm({ title, desc, noCancel: true, saveLabel: S().close, fields: rows.map((r) => ({ type: 'info', label: r[0], value: r[1], full: true, copy: r[2] !== false, hint: r[3] })) });
  }
  const setFm = (o) => set({ fm: Object.assign({}, ST.fm, o) });
  function setField(k, val) { setFm({ v: Object.assign({}, ST.fm.v, { [k]: val }) }); }
  function formVals(c, seg, sw) {
    const fm = ST.fm;
    if (!fm) return { fields: [] };
    const cfg = fm.cfg;
    const s = S();
    const v = fm.v;
    const fields = cfg.fields.filter((f) => !f.when || f.when(v)).map((f) => {
      const b = { label: f.label, span: f.full ? '1 / -1' : 'auto', hint: f.hint || '', hasHint: !!f.hint };
      const val = f.type === 'info' ? f.value : v[f.k];
      if (f.type === 'select') return Object.assign(b, { isSelect: true, value: val == null ? '' : String(val), opts: f.opts, set: (e) => setField(f.k, e.target.value) });
      if (f.type === 'seg') return Object.assign(b, { isSeg: true, seg: seg(f.opts.map((o) => [o.v, o.label]), val, (x) => setField(f.k, x)) });
      if (f.type === 'switch') return Object.assign(b, { isSwitch: true, sw: sw(!!val, () => setField(f.k, !val), f.label) });
      if (f.type === 'area') return Object.assign(b, { isArea: true, value: val == null ? '' : String(val), ph: f.ph || '', set: (e) => setField(f.k, e.target.value) });
      if (f.type === 'info') return Object.assign(b, { isInfo: true, value: String(val == null ? '' : val), copy: f.copy ? () => copy(String(val)) : null });
      if (f.type === 'image') return Object.assign(b, { isImage: true, value: f.value });
      return Object.assign(b, { isText: true, type: f.type || 'text', value: val == null ? '' : String(val), ph: f.ph || '', ac: f.ac || 'off',
        font: f.mono ? 'ui-monospace, Menlo, Consolas, monospace' : 'inherit', set: (e) => setField(f.k, e.target.value) });
    });
    return {
      title: cfg.title, desc: cfg.desc || '', hasDesc: !!cfg.desc, fields, err: fm.err, hasErr: !!fm.err,
      busy: fm.busy ? 'true' : 'false', hasCancel: !cfg.noCancel, hasSave: true, saveLabel: cfg.saveLabel || s.save,
      saveBg: cfg.danger ? c.critBg : c.accentBg,
      extra: (cfg.extra || []).map((x) => ({ label: x.label, fg: x.danger ? c.crit : c.text, go: x.go })),
      save: async () => {
        if (!ST.fm || ST.fm.cfg !== cfg || ST.fm.busy) return;
        if (!cfg.onSave) { closeOverlay(); return; }
        setFm({ busy: true, err: '' });
        try {
          const keep = await cfg.onSave(Object.assign({}, ST.fm.v));
          if (keep !== false && ST.fm && ST.fm.cfg === cfg) closeOverlay();
          else if (ST.fm && ST.fm.cfg === cfg) setFm({ busy: false });
        } catch (e) {
          if (ST.fm && ST.fm.cfg === cfg) setFm({ busy: false, err: e.message });
        }
      },
    };
  }

  // ---------------------------------------------------------------- forms per feature
  const num = (x, d) => { const n = parseInt(String(x).trim(), 10); return isNaN(n) ? d : n; };
  const levelOpts = () => { const s = S(); return [{ v: 'guest', label: s.lvGuest }, { v: 'member', label: s.lvMember }, { v: 'admin', label: s.lvAdmin }]; };
  const roleOpts = () => { const s = S(); return [{ v: 'viewer', label: s.roleViewer }, { v: 'operator', label: s.roleOperator }, { v: 'admin', label: s.roleAdmin }]; };
  const durOpts = () => { const s = S(); return [{ v: '1h', label: '1h' }, { v: '6h', label: '6h' }, { v: '24h', label: '24h' }, { v: '7d', label: '7d' }, { v: '30d', label: '30d' }, { v: 'perm', label: s.dPerm }]; };
  const scopeOpts = [{ v: 'read', label: 'read' }, { v: 'write', label: 'write' }, { v: 'admin', label: 'admin' }];
  const groupOpts = (extraNew) => {
    const s = S();
    const o = [{ v: '', label: s.none }].concat(items('groups').map((g) => ({ v: g.id, label: g.name })));
    if (extraNew) o.push({ v: '__new', label: '＋ ' + s.groupNew });
    return o;
  };
  async function ensureGroup(v) {
    if (v.group !== '__new') return v.group || '';
    const name = String(v.groupNew || '').trim();
    if (!name) return '';
    const exists = items('groups').find((g) => g.name === name);
    if (exists) return exists.id;
    const g = await api('POST', '/admin/groups', { name });
    return g.id;
  }

  async function enrollForm() {
    const s = S();
    if (!D.groups) { try { D.groups = await GET('groups'); } catch (_) { /* optional */ } }
    openForm({
      title: s.fEnrollTitle, desc: s.fEnrollDesc, saveLabel: s.create,
      values: { note: '', level: D.settings.default_level || 'guest', group: '', uses: '1', ttl: '24', auto: true },
      fields: [
        { k: 'note', label: s.fNote, full: true, ph: 'Pixel 9 · lin.yuchen' },
        { k: 'level', label: s.fLevel, type: 'seg', opts: levelOpts(), full: true },
        { k: 'group', label: s.fGroup, type: 'select', opts: groupOpts(true) },
        { k: 'groupNew', label: s.groupNew, when: (v) => v.group === '__new' },
        { k: 'uses', label: s.fUses, type: 'number', when: () => !(D.status && D.status.enrollment_secure) },
        { k: 'ttl', label: s.fTtl, type: 'number' },
        { k: 'auto', label: s.fAutoApprove, hint: s.fAutoApproveHint, type: 'switch', full: true },
      ],
      onSave: async (v) => {
        const group = await ensureGroup(v);
        const r = await api('POST', '/admin/enroll-tokens', { note: v.note.trim(), level: v.level, group, uses: num(v.uses, 1), ttl_hours: num(v.ttl, 24), auto_approve: !!v.auto });
        const srv = String(r.server_url || (D.status && D.status.public_url) || location.origin).replace(/\/+$/, '');
        const code = r.join_code || r.token;
        setTimeout(() => openForm({ title: s.fEnrollTitle, desc: r.single_device ? s.singleDevice : s.enrollOnce, noCancel: true, saveLabel: s.close,
          fields: [
            {type:'image',label:s.enrollQR,value:r.qr_png,full:true},
            {type:'info',label:s.enrollCode,value:code,copy:true,full:true},
            {type:'info',label:s.serverUrl,value:srv,copy:true,full:true},
          ] }), 0);
        return true;
      },
    });
  }

  function deviceEditForm(d) {
    const s = S();
    const kinds = ['laptop', 'phone', 'tablet', 'nas', 'server', 'monitor', 'chip'];
    const kl = { laptop: s.kLaptop, phone: s.kPhone, tablet: s.kTablet, nas: s.kNas, server: s.kServer, monitor: s.kMonitor, chip: s.kChip };
    openForm({
      title: s.editDevice, saveLabel: s.save,
      values: { name: d.name, kind: d.kind, group: d.group_id || '', user: d.user_id || '' },
      fields: [
        { k: 'name', label: s.fName, full: true },
        { k: 'kind', label: s.fKind, type: 'select', opts: kinds.map((k) => ({ v: k, label: kl[k] })) },
        { k: 'user', label: s.fUser, type: 'select', opts: [{ v: '', label: s.none }].concat(items('users').map((u) => ({ v: u.id, label: u.username }))) },
        { k: 'group', label: s.fGroup, type: 'select', opts: groupOpts(true) },
        { k: 'groupNew', label: s.groupNew, hint: s.groupNewHint, when: (v) => v.group === '__new' },
      ],
      onSave: async (v) => {
        const group = await ensureGroup(v);
        await api('PATCH', '/admin/devices/' + d.id, { name: v.name.trim(), kind: v.kind, group, user: v.user });
        flash(F(s.tMapSaved, { name: v.name.trim() }));
        reloadSoon(100);
        if (ST.drawerId) loadDevice(ST.drawerId);
      },
    });
  }

  function commandForm(d) {
    const s = S();
    const cmds = [['ping', s.cmdPing], ['reload-frpc', s.cmdReload], ['refresh-peers', s.cmdPeers], ['collect-logs', s.cmdLogs], ['rotate-key', s.cmdRotate], ['restart-client', s.cmdRestart], ['disconnect', s.cmdDisconnect]];
    openForm({
      title: F(s.cmdTitle, { name: d.name }), saveLabel: s.cmdRun, values: { name: 'ping', lines: '200' },
      fields: [
        { k: 'name', label: s.fCmd, type: 'select', opts: cmds.map((x) => ({ v: x[0], label: x[1] + ' · ' + x[0] })), full: true },
        { k: 'lines', label: s.fArgs, hint: s.fArgsHint, type: 'number', when: (v) => v.name === 'collect-logs' },
      ],
      onSave: async (v) => {
        const body = { name: v.name, timeout_s: 20 };
        if (v.name === 'collect-logs') body.args = { lines: String(num(v.lines, 200)) };
        const r = await api('POST', '/admin/devices/' + d.id + '/command', body);
        setTimeout(() => infoSheet(s.cmdResult, d.name + ' · ' + v.name + ' · ' + (r && r.ok ? s.cmdOk : s.cmdFail), [[s.cmdResult, (r && r.output) || '—']]), 0);
      },
    });
  }

  function grantForm() {
    const s = S();
    const devs = items('devices').filter((d) => d.status === 'active');
    if (!devs.length) { flash(s.noMatch); return; }
    openForm({
      title: s.grantTitle, saveLabel: s.approve, values: { target: devs[0].id, level: 'member', duration: '7d', reason: '' },
      fields: [
        { k: 'target', label: s.fTarget, type: 'select', opts: devs.map((d) => ({ v: d.id, label: d.name + ' · ' + d.did })), full: true },
        { k: 'level', label: s.fLevel, type: 'seg', opts: levelOpts(), full: true },
        { k: 'duration', label: s.fDuration, type: 'seg', opts: durOpts(), full: true },
        { k: 'reason', label: s.fReason, full: true },
      ],
      onSave: async (v) => {
        await api('POST', '/admin/grants', { target: v.target, level: v.level, duration: v.duration, reason: v.reason.trim() });
        const d = devs.find((x) => x.id === v.target);
        flash(F(s.tGrantCreated, { target: d ? d.name : v.target, level: s['lv' + v.level.charAt(0).toUpperCase() + v.level.slice(1)] }));
        reloadSoon(100);
      },
    });
  }

  function userForm(u) {
    const s = S();
    if (!u) {
      openForm({
        title: s.userNew, saveLabel: s.create, values: { username: '', display: '', email: '', role: 'viewer', password: '' },
        fields: [
          { k: 'username', label: s.username, ac: 'off' }, { k: 'display', label: s.fDisplay },
          { k: 'role', label: s.fRole, type: 'seg', opts: roleOpts(), full: true },
          { k: 'email', label: s.fEmail, type: 'email' }, { k: 'password', label: s.password, type: 'password', ph: s.fPwOptional, ac: 'new-password' },
        ],
        onSave: async (v) => {
          const body = { username: v.username.trim(), role: v.role, display_name: v.display.trim(), email: v.email.trim() };
          if (v.password) body.password = v.password;
          const r = await api('POST', '/admin/users', body);
          reloadSoon(100);
          if (r && r.password) setTimeout(() => infoSheet(F(s.pwGenerated, { name: body.username }), s.pwGeneratedDesc, [[s.username, body.username], [s.password, r.password]]), 0);
          else flash(F(s.pwGenerated, { name: body.username }));
        },
      });
      return;
    }
    const me = D.me && D.me.user && D.me.user.id === u.id;
    const extra = [];
    if (u.mfa_enabled) extra.push({ label: s.resetMfa, go: () => { closeOverlay(); confirmBox(s.resetMfa + '?', u.username, s.resetMfa, () => act(() => api('POST', '/admin/users/' + u.id + '/reset-mfa', {}), F(s.tMfaReset, { name: u.username }))); } });
    if (!me) extra.push({ label: s.deleteUser, danger: true, go: () => { closeOverlay(); confirmBox(s.cfDelUser, F(s.cfDelUserBody, { name: u.username }), s.delete, () => act(() => api('DELETE', '/admin/users/' + u.id), F(s.tDeleted, { name: u.username }))); } });
    openForm({
      title: s.userEdit + ' · ' + u.username, saveLabel: s.save, extra,
      values: { display: u.display_name || '', email: u.email || '', role: u.role, password: '', disabled: !!u.disabled },
      fields: [
        { k: 'display', label: s.fDisplay }, { k: 'email', label: s.fEmail, type: 'email' },
        { k: 'role', label: s.fRole, type: 'seg', opts: roleOpts(), full: true },
        { k: 'password', label: s.fNewPassword, type: 'password', hint: s.fNewPwHint, ac: 'new-password', full: true },
        { k: 'disabled', label: s.fDisabled, hint: s.fDisabledHint, type: 'switch', full: true, when: () => !me },
      ],
      onSave: async (v) => {
        const body = { display_name: v.display.trim(), email: v.email.trim(), role: v.role };
        if (!me) body.disabled = !!v.disabled;
        if (v.password) body.password = v.password;
        await api('PATCH', '/admin/users/' + u.id, body);
        flash(F(s.tUserSaved, { name: u.username }));
        reloadSoon(100);
      },
    });
  }

  function tokenForm(service) {
    const s = S();
    openForm({
      title: service ? s.keyNew : s.tokenNew, saveLabel: s.create, values: { name: '', scope: 'read', ttl: service ? '0' : '90' },
      fields: [
        { k: 'name', label: s.fName, full: true, ph: service ? 'metrics-exporter' : 'ci-deploy' },
        { k: 'scope', label: s.fScopes, type: 'seg', opts: scopeOpts },
        { k: 'ttl', label: s.fTtlDays, hint: s.fTtlDaysHint, type: 'number' },
      ],
      onSave: async (v) => {
        const name = v.name.trim();
        const r = await api('POST', service ? '/admin/system/api-keys' : '/admin/security/tokens', { name, scopes: [v.scope], ttl_days: num(v.ttl, 0) });
        reloadSoon(100);
        setTimeout(() => infoSheet(F(s.tokenCreated, { name }), s.tokenOnce, [[name, r.token]]), 0);
      },
    });
  }

  function hookForm() {
    const s = S();
    const types = (D['system/webhooks'] && D['system/webhooks'].event_types) || [];
    openForm({
      title: s.hookNew, saveLabel: s.create, values: { url: 'https://', name: '', events: 'alert.*' },
      fields: [
        { k: 'url', label: s.fUrl, full: true, mono: true },
        { k: 'name', label: s.fName },
        { k: 'events', label: s.fEvents, hint: s.fEventsHint + (types.length ? ' · ' + types.slice(0, 8).join(', ') + '…' : ''), mono: true },
      ],
      onSave: async (v) => {
        const events = v.events.split(',').map((x) => x.trim()).filter(Boolean);
        const r = await api('POST', '/admin/system/webhooks', { url: v.url.trim(), name: v.name.trim(), events, enabled: true });
        reloadSoon(100);
        setTimeout(() => infoSheet(s.hookCreated, s.hookSecretDesc, [[s.signSecret, r.secret]]), 0);
      },
    });
  }

  function nodeForm(n) {
    const s = S();
    const v0 = n ? {
      name: n.name, region: n.region || '', public_addr: n.public_addr, bind_port: String(n.bind_port || 7000), vhost_http_port: String(n.vhost_http_port || ''),
      vhost_https_port: String(n.vhost_https_port || ''), subdomain_host: n.subdomain_host || '', allow_ports: n.allow_ports || '', dashboard_addr: n.dashboard_addr || '',
      dashboard_user: n.dashboard_user || '', dashboard_password: '', managed: !!n.managed, enabled: n.enabled !== false,
    } : { name: '', region: '', public_addr: '', bind_port: '7000', vhost_http_port: '80', vhost_https_port: '443', subdomain_host: '', allow_ports: '', dashboard_addr: '', dashboard_user: '', dashboard_password: '', managed: true, enabled: true };
    openForm({
      title: n ? F(s.nodeEdit, { name: n.name }) : s.nodeNew, saveLabel: n ? s.save : s.create, values: v0,
      fields: [
        { k: 'name', label: s.fName, ph: 'frps-taipei' }, { k: 'region', label: s.fRegion, ph: 'tpe' },
        { k: 'public_addr', label: s.fPublicAddr, full: true, mono: true, ph: 'frp.example.com' },
        { k: 'bind_port', label: s.fBindPort, type: 'number' }, { k: 'subdomain_host', label: s.fSubHost, mono: true },
        { k: 'vhost_http_port', label: s.fHttpPort, type: 'number' }, { k: 'vhost_https_port', label: s.fHttpsPort, type: 'number' },
        { k: 'allow_ports', label: s.fAllowPorts, hint: s.fAllowPortsHint, mono: true, full: true },
        { k: 'managed', label: s.fManaged, hint: s.fManagedHint, type: 'switch', full: true },
        { k: 'dashboard_addr', label: s.fDashAddr, hint: s.fDashAddrHint, mono: true, full: true, when: (v) => !v.managed },
        { k: 'dashboard_user', label: s.fDashUser, when: (v) => !v.managed }, { k: 'dashboard_password', label: s.fDashPw, type: 'password', ac: 'new-password', when: (v) => !v.managed },
        { k: 'enabled', label: s.fEnabled, type: 'switch', full: true },
      ],
      onSave: async (v) => {
        const body = {
          name: v.name.trim(), region: v.region.trim(), public_addr: v.public_addr.trim(), bind_port: num(v.bind_port, 7000),
          vhost_http_port: num(v.vhost_http_port, 0), vhost_https_port: num(v.vhost_https_port, 0), subdomain_host: v.subdomain_host.trim(),
          allow_ports: v.allow_ports.trim(), managed: !!v.managed, enabled: !!v.enabled,
        };
        if (!v.managed) { body.dashboard_addr = v.dashboard_addr.trim(); body.dashboard_user = v.dashboard_user.trim(); if (v.dashboard_password) body.dashboard_password = v.dashboard_password; }
        const r = n ? await api('PATCH', '/admin/frp/nodes/' + n.id, body) : await api('POST', '/admin/frp/nodes', body);
        reloadSoon(100);
        flash(F(s.tNodeSaved, { name: body.name }));
        if (!n && r && r.frps_toml && !body.managed) setTimeout(() => infoSheet(F(s.nodeToml, { name: body.name }), s.nodeTomlDesc, [['frps.toml', r.frps_toml]]), 0);
      },
    });
  }

  function settingsForm() {
    const s = S();
    const st = D.settings;
    openForm({
      title: s.settingsTitle, saveLabel: s.save,
      values: {
        console_name: st.console_name || '', registration_approval: st.registration_approval || 'manual', default_level: st.default_level || 'guest',
        discovery_scope: st.discovery_scope || 'all', relay_enabled: st.relay_enabled !== false, relay_max_file_mb: String(st.relay_max_file_mb || ''),
        message_retention_days: String(st.message_retention_days || ''), file_retention_hours: String(st.file_retention_hours || ''),
        backup_hour: String(st.backup_hour == null ? '' : st.backup_hour), backup_keep: String(st.backup_keep || ''), session_hours: String(st.session_hours || ''),
        device_token_hours: String(st.device_token_hours || ''), abnormal_fail_limit: String(st.abnormal_fail_limit || ''), language: st.language || 'en',
      },
      fields: [
        { k: 'console_name', label: s.setConsoleName, full: true },
        { k: 'registration_approval', label: s.setApproval, type: 'seg', opts: [{ v: 'manual', label: s.apprManual }, { v: 'auto', label: s.apprAuto }] },
        { k: 'default_level', label: s.setDefaultLevel, type: 'seg', opts: levelOpts() },
        { k: 'discovery_scope', label: s.setDiscovery, type: 'seg', opts: [{ v: 'all', label: s.discAll }, { v: 'group', label: s.discGroup }, { v: 'user', label: s.discUser }] },
        { k: 'language', label: s.setLang, type: 'seg', opts: [{ v: 'zh-TW', label: '繁體中文' }, { v: 'zh-CN', label: '简体中文' }, { v: 'ja', label: '日本語' }, { v: 'en', label: 'English' }] },
        { k: 'relay_enabled', label: s.setRelay, hint: s.setRelayHint, type: 'switch', full: true },
        { k: 'relay_max_file_mb', label: s.setRelayMax, type: 'number' }, { k: 'message_retention_days', label: s.setMsgRet, type: 'number' },
        { k: 'file_retention_hours', label: s.setFileRet, type: 'number' }, { k: 'abnormal_fail_limit', label: s.setFailLimit, type: 'number' },
        { k: 'backup_hour', label: s.setBackupHour, type: 'number' }, { k: 'backup_keep', label: s.setBackupKeep, type: 'number' },
        { k: 'session_hours', label: s.setSessionH, type: 'number' }, { k: 'device_token_hours', label: s.setDeviceTokH, type: 'number' },
      ],
      onSave: async (v) => {
        const numeric = ['relay_max_file_mb', 'message_retention_days', 'file_retention_hours', 'backup_hour', 'backup_keep', 'session_hours', 'device_token_hours', 'abnormal_fail_limit'];
        const patch = {};
        for (const k of Object.keys(v)) {
          let val = v[k];
          if (numeric.indexOf(k) >= 0) { val = num(val, null); if (val == null) continue; }
          if (typeof val === 'string') val = val.trim();
          if (val !== st[k]) patch[k] = val;
        }
        if (Object.keys(patch).length) await saveSettings(patch);
      },
    });
  }
  async function saveSettings(patch, msg) {
    const r = await api('PATCH', '/admin/system/settings', patch);
    const ns = (r && r.settings) || Object.assign({}, D.settings, patch);
    D.settings = Object.assign({}, D.settings, ns);
    if (D['system/settings']) D['system/settings'].settings = D.settings;
    if (D.me) D.me.settings = Object.assign({}, D.me.settings, ns);
    applySettings();
    flash(msg || S().tSettings);
    render();
    return r;
  }
  const quickSetting = (patch) => { if (!isAdmin()) { flash(S().adminOnly); return; } saveSettings(patch).catch((e) => flash(e.message)); };

  function accountSheet() {
    const s = S();
    const u = (D.me && D.me.user) || {};
    const extra = [{ label: s.changePw, go: passwordForm }];
    extra.push(u.mfa_enabled ? { label: s.disableMfa, go: mfaDisableForm } : { label: s.enableMfa, go: mfaEnableForm });
    extra.push({ label: s.logout, danger: true, go: () => { closeOverlay(); logout(); } });
    openForm({
      title: s.accTitle, noCancel: true, saveLabel: s.close, extra,
      fields: [
        { type: 'info', label: s.accUser, value: u.username || '—' },
        { type: 'info', label: s.accRole, value: roleLabel(role()) },
        { type: 'info', label: s.accMfa, value: u.mfa_enabled ? s.mfaOn + ' · TOTP' : s.mfaOff, full: true },
      ],
    });
  }
  function passwordForm() {
    const s = S();
    openForm({
      title: s.changePw, saveLabel: s.save, values: { current: '', next: '' },
      fields: [{ k: 'current', label: s.fCurPw, type: 'password', ac: 'current-password', full: true }, { k: 'next', label: s.fNewPw, type: 'password', ac: 'new-password', full: true }],
      onSave: async (v) => { await api('POST', '/auth/password', { current: v.current, new: v.next }); flash(s.tPwChanged); },
    });
  }
  async function mfaEnableForm() {
    const s = S();
    let r;
    try { r = await api('POST', '/auth/mfa/setup', {}); } catch (e) { flash(e.message); return; }
    openForm({
      title: s.enableMfa, desc: s.mfaSetupDesc, saveLabel: s.mfaEnable, values: { code: '' },
      fields: [
        { type: 'info', label: s.mfaSecret, value: r.secret, copy: true, full: true },
        { type: 'info', label: s.mfaUri, value: r.otpauth_uri, copy: true, full: true },
        { k: 'code', label: s.totpCode, mono: true, ac: 'one-time-code', full: true },
      ],
      onSave: async (v) => { await api('POST', '/auth/mfa/enable', { code: v.code.trim() }); D.me.user.mfa_enabled = true; flash(s.tMfaOn); },
    });
  }
  function mfaDisableForm() {
    const s = S();
    openForm({
      title: s.disableMfa, desc: s.mfaDisableDesc, saveLabel: s.disableMfa, danger: true, values: { code: '' },
      fields: [{ k: 'code', label: s.totpCode, mono: true, ac: 'one-time-code', full: true }],
      onSave: async (v) => { await api('POST', '/auth/mfa/disable', { code: v.code.trim() }); D.me.user.mfa_enabled = false; flash(s.tMfaOff); },
    });
  }
  function roleLabel(r) { const s = S(); return { admin: s.roleAdmin, operator: s.roleOperator, viewer: s.roleViewer }[r] || r; }

  async function exportAudit() {
    const s = S();
    try {
      const r = await GET('audit?limit=1000');
      const rows = (r.items || []).map((a) => [fmtFull(a.at), a.actor, a.actor_type, a.action, a.target_type, a.target_id, a.ip, JSON.stringify(a.detail || {})]);
      const esc = (x) => '"' + String(x == null ? '' : x).replace(/"/g, '""') + '"';
      const csv = '﻿' + [['time', 'actor', 'actor_type', 'action', 'target_type', 'target_id', 'ip', 'detail']].concat(rows).map((row) => row.map(esc).join(',')).join('\r\n');
      const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = 'meshport-audit-' + fmtDate(new Date()) + '.csv';
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 2000);
      flash(F(s.tExportDone, { n: rows.length }));
    } catch (e) { flash(e.message); }
  }

  // ---------------------------------------------------------------- view model
  function adminVals(c) {
    const s0 = S();
    const s = Object.create(s0);
    const set2 = (o) => set(o);
    const ST2 = { ok: { stFg: c.ok, stBg: c.okSoft }, warn: { stFg: c.warn, stBg: c.warnSoft }, crit: { stFg: c.crit, stBg: c.critSoft }, off: { stFg: c.muted, stBg: c.tile }, info: { stFg: c.accent, stBg: c.accentSoft } };
    const st = (k, label) => Object.assign({ stLabel: label }, ST2[k]);
    const lvLabel = { guest: s.lvGuest, member: s.lvMember, admin: s.lvAdmin };
    const lv = (l) => {
      const m = { guest: [c.muted, c.tile], member: [c.accent, c.accentSoft], admin: ['#FFFFFF', c.accentBg] }[l] || [c.muted, c.tile];
      return { lvFg: m[0], lvBg: m[1], lvLabel: lvLabel[l] || l };
    };
    const roleLv = (r) => Object.assign(lv({ admin: 'admin', operator: 'member', viewer: 'guest' }[r] || 'guest'), { lvLabel: roleLabel(r) });
    const seg = (opts, cur, pick) => opts.map((o) => {
      const on = o[0] === cur;
      return { label: o[1], pressed: on ? 'true' : 'false', bg: on ? c.segOn : 'transparent', fg: on ? c.text : c.muted, sh: on ? c.segShadow : 'none', pick: () => pick(o[0]) };
    });
    const sw = (on, toggle, label) => ({ checked: on ? 'true' : 'false', toggle, label, swBg: on ? c.accentBg : c.swOff, knobX: on ? 18 : 0 });
    const sevMap = {
      crit: { fg: c.crit, bg: c.critSoft, icon: I.critical, sevLabel: s.sevCrit },
      warn: { fg: c.warn, bg: c.warnSoft, icon: I.alert, sevLabel: s.sevWarn },
      info: { fg: c.accent, bg: c.accentSoft, icon: I.info, sevLabel: s.sevInfo },
    };
    const kindIcon = (k) => I[{ laptop: 'laptop', nas: 'nas', phone: 'phone', monitor: 'monitor', chip: 'chip', tablet: 'tablet', server: 'server' }[k] || 'monitor'];
    const ql = ST.q.trim().toLowerCase();
    const has = (str) => !ql || String(str).toLowerCase().indexOf(ql) >= 0;
    const page = ST.page;
    const canOp = can('operator');
    const admin = isAdmin();
    const ov = D.overview || {};
    const go2 = (p) => go(p);

    // ---------- shared device lookups
    const devList = items('devices');
    const devById = {};
    devList.forEach((d) => { devById[d.id] = d; });

    // ---------- charts (design's chart model)
    const hv = ST.hov;
    const mk = (key, sets, o) => {
      const n = sets[0].vals.length;
      const lo = o.min || 0;
      const hi = Math.max(o.max, lo + 1e-9);
      const X = (i) => (n > 1 ? (i / (n - 1)) * 600 : 0);
      const Yp = (v) => Math.max(0, Math.min(100, (1 - (v - lo) / (hi - lo)) * 100));
      const d = (vals) => vals.map((v, i) => (i ? 'L' : 'M') + X(i).toFixed(1) + ' ' + Yp(v).toFixed(2)).join(' ');
      const hiI = hv && hv.k === key && hv.i < n ? hv.i : -1;
      const hx = hiI >= 0 && n > 1 ? (hiI / (n - 1)) * 100 : 0;
      return {
        grid: [1, 0.5, 0].map((f) => ({ top: (1 - f) * 100, label: o.ax(lo + (hi - lo) * f) })),
        lines: sets.map((x) => ({ d: d(x.vals), color: x.color })),
        areas: sets.filter((x) => x.fill).map((x) => ({ d: d(x.vals) + ' L600 100 L0 100 Z', fill: T.rgba(x.color, dark() ? 0.2 : 0.12) })),
        ends: o.ends ? sets.map((x) => ({ top: Yp(x.vals[n - 1]), label: x.name, color: x.color })) : [],
        hov: hiI >= 0, hx, tipX: hx, tipShift: hx > 62 ? 'calc(-100% - 12px)' : '12px',
        dots: hiI >= 0 ? sets.map((x) => ({ top: Yp(x.vals[hiI]), color: x.color })) : [],
        tipTitle: hiI >= 0 ? o.xAt(hiI, n) : '',
        tipRows: hiI >= 0 ? sets.map((x) => ({ label: x.name, value: o.fmt(x.vals[hiI]), color: x.color })) : [],
        cols: sets[0].vals.map((v, i) => ({ enter: () => set2({ hov: { k: key, i } }) })),
        leave: () => set2({ hov: null }),
        xl: o.xl.map((t) => ({ t })),
        legend: sets.map((x) => ({ label: x.name, color: x.color, value: o.fmt(x.vals[n - 1]) })),
        now: o.nowTxt != null ? o.nowTxt : o.fmt(sets[0].vals[n - 1]),
      };
    };
    const padRing = (arr, n) => {
      const a = arr.slice(-n);
      while (a.length < n) a.unshift(a.length ? a[0] : 0);
      return a;
    };

    // ---------- nav / sidebar
    const pendDev = (ov.pending && ov.pending.devices) || [];
    const pendGr = (ov.pending && ov.pending.grants) || [];
    const openAlerts = ov.alerts || [];
    const devCounts = ov.devices || {};
    const pages = [
      ['overview', I.grid, s.pOverview, s.pOverviewSub, 0, null],
      ['devices', I.devices, s.pDevices, s.pDevicesSub, devCounts.online || 0, 'off'],
      ['comms', I.swap, s.pComms, s.pCommsSub, 0, null],
      ['frp', I.tunnel, s.pFrp, s.pFrpSub, 0, null],
      ['access', I.userCheck, s.pAccess, s.pAccessSub, pendDev.length + pendGr.length, 'warn'],
      ['security', I.shield, s.pSecurity, s.pSecuritySub, openAlerts.length, 'crit'],
      ['monitor', I.activity, s.pMonitor, s.pMonitorSub, 0, null],
      ['system', I.gear, s.pSystem, s.pSystemSub, 0, null],
    ];
    const badgeC = { off: [c.muted, c.tile], warn: [c.warn, c.warnSoft], crit: [c.crit, c.critSoft] };
    const nav = pages.map((p) => {
      const on = p[0] === page;
      return { label: p[2], icon: p[1], go: go2(p[0]), current: on ? 'page' : 'false', bg: on ? c.accentSoft : 'transparent', fg: on ? c.accent : c.text,
        hasBadge: p[4] > 0, badge: p[4] > 99 ? '99+' : p[4], badgeFg: p[5] ? badgeC[p[5]][0] : c.muted, badgeBg: p[5] ? badgeC[p[5]][1] : c.tile };
    });
    const cur = pages.filter((p) => p[0] === page)[0] || pages[0];
    const status = D.status || {};
    const upd = D.update || {};
    let host = location.host;
    try { if (status.public_url) host = new URL(status.public_url).host; } catch (_) { /* keep */ }
    const upSec = status.uptime_s != null ? status.uptime_s + (Date.now() - LIVE.statusAt) / 1000 : null;
    const srv = {
      name: host, dot: LIVE.wsOn ? c.okDot : c.warnDot, dotSoft: LIVE.wsOn ? c.okSoft : c.warnSoft, state: LIVE.wsOn ? s.healthy : s.srvDown,
      up: upSec != null ? fmtDur(upSec) : '—', ver: status.version ? 'v' + status.version : '', hasUpd: !!upd.available,
    };
    const meU = (D.me && D.me.user) || {};
    const meName = meU.display_name || meU.username || '—';
    const me = { name: meName, ini: (meU.username || '?').slice(0, 2).toUpperCase(), role: role() === 'admin' ? s.superAdmin : roleLabel(role()) };

    const V = {
      s, c, I, font: FONTS[ST.lang], langTag: LANG_TAGS[ST.lang], brand: D.settings.console_name || 'Meshport',
      themeIcon: dark() ? I.sun : I.moon, cycleLang, toggleTheme, logout, openAccount: accountSheet,
      nav, pageTitle: cur[2], pageSub: cur[3], pageErr: ST.pageErr, hasPageErr: !!ST.pageErr,
      isOverview: page === 'overview', isDevices: page === 'devices', isComms: page === 'comms', isFrp: page === 'frp',
      isAccess: page === 'access', canGrant: page === 'access' && canOp, isSecurity: page === 'security', isMonitor: page === 'monitor', isSystem: page === 'system',
      showLive: (page === 'overview' || page === 'comms') && LIVE.wsOn, isAdmin: admin,
      q: ST.q, setQ: (e) => set2({ q: e.target.value }),
      goAlerts: go2('monitor'), goSystem: go2('system'), goAccess: go2('access'), goMonitor: go2('monitor'),
      envLabel: host, hasOpenAlerts: openAlerts.length > 0, srv, me,
      addDevice: enrollForm, exportAudit, addNode: () => nodeForm(null), grantDirect: grantForm,
      gc: {
        dev: 'minmax(220px, 1.6fr) 130px 140px 96px minmax(130px, 1fr) 84px 104px 210px',
        comm: 'minmax(230px, 1.5fr) 96px minmax(190px, 1.3fr) 100px 64px 86px 130px minmax(170px, 1.2fr) 118px',
        frp: 'minmax(120px, 1fr) minmax(140px, 1.1fr) 70px 130px minmax(200px, 1.6fr) 110px 100px 50px 186px',
        acc: 'minmax(170px, 1.2fr) minmax(150px, 1fr) 84px minmax(200px, 1.6fr) 96px 120px 96px 170px',
        login: '96px 96px minmax(150px, 1.1fr) minmax(150px, 1.2fr) minmax(190px, 1.4fr)',
        fail: '86px minmax(150px, 1.4fr) minmax(130px, 1.2fr) 110px',
      },
      closeOverlay, hasConfirm: !!ST.cf, cf: ST.cf || { title: '', body: '', ok: '' },
      cancelConfirm: () => set2({ cf: null }),
      doConfirm: () => { const f = ST.cf && ST.cf.fn; set2({ cf: null }); if (f) f(); },
      hasToast: !!ST.toast, toast: ST.toast,
      hasForm: ST.overlay === 'form' && !!ST.fm, fm: formVals(c, seg, sw),
    };

    // ---------- device helpers (used by overview, devices, drawer)
    const devState = (d) => (d.status === 'banned' ? 'banned' : d.status === 'disabled' ? 'disabled' : d.status === 'pending' ? 'pending' : d.online ? 'online' : 'offline');
    const devStatus = (d) => ({ banned: st('crit', s.stBanned), disabled: st('crit', s.disabled), pending: st('warn', s.stPendingDev), online: st('ok', s.online), offline: st('off', s.offline) })[devState(d)];
    const approveDev = (d) => act(() => api('POST', '/admin/devices/' + d.id + '/approve', {}), F(s.tApprovedDev, { name: d.name }));
    const rejectDev = (d) => confirmBox(s.cfRejectDev, F(s.cfRejectDevBody, { name: d.name }), s.reject, () => act(() => api('POST', '/admin/devices/' + d.id + '/reject', {}), F(s.tRejectedDev, { name: d.name }), () => set2({ overlay: null })));
    const toggleDev = (d) => {
      const k = devState(d);
      if (k === 'pending') return approveDev(d);
      if (k === 'banned') return act(() => api('POST', '/admin/devices/' + d.id + '/unban', {}), F(s.tUnbanned, { name: d.name }));
      if (k === 'disabled') return act(() => api('POST', '/admin/devices/' + d.id + '/enable', {}), F(s.tEnabled, { name: d.name }));
      return act(() => api('POST', '/admin/devices/' + d.id + '/disable', {}), F(s.tDisabled, { name: d.name }));
    };
    const removeDev = (d) => confirmBox(s.cfDelDev, F(s.cfDelDevBody, { name: d.name }), s.delete, () => act(() => api('DELETE', '/admin/devices/' + d.id), F(s.tDeleted, { name: d.name }), () => set2({ overlay: null, drawerId: null })));
    const openDrawer = (d) => { set2({ overlay: 'drawer', drawerId: d.id }); loadDevice(d.id); };
    const grantAct = (g, what) => {
      const tl = lvLabel[g.level] || g.level;
      if (what === 'approve') return act(() => api('POST', '/admin/grants/' + g.id + '/approve', {}), F(s.tApproved, { target: g.target, level: tl }));
      if (what === 'reject') return act(() => api('POST', '/admin/grants/' + g.id + '/reject', {}), F(s.tRejected, { app: g.requester }));
      return confirmBox(s.cfRevoke, F(s.cfRevokeBody, { target: g.target, level: tl }), s.revoke, () => act(() => api('POST', '/admin/grants/' + g.id + '/revoke', {}), F(s.tRevoked, { target: g.target })));
    };

    // ---------- overview
    if (page === 'overview') {
      const L = LIVE.last || {};
      const res = ov.resources || {};
      const p2pV = padRing(LIVE.ring.p2p_bps, 30).map(mbps);
      const relV = padRing(LIVE.ring.relay_bps, 30).map(mbps);
      const bwMax = niceMax(Math.max.apply(null, p2pV.concat(relV)), 1);
      V.bw = mk('bw', [{ name: 'P2P', vals: p2pV, color: c.s1 }, { name: s.mixRelay, vals: relV, color: c.s2 }], {
        max: bwMax, ax: axNum, fmt: fmtMbps, ends: true, xl: ['-60s', '-45s', '-30s', '-15s', s.now],
        xAt: (i, n) => (i === n - 1 ? s.now : '-' + (n - 1 - i) * 2 + 's'),
      });
      const cpu = L.cpu != null ? L.cpu : res.cpu || 0;
      const mem = L.mem != null ? L.mem : res.mem || 0;
      const disk = L.disk != null ? L.disk : res.disk || 0;
      const lvlOf = (v) => (v >= 85 ? 'crit' : v >= 70 ? 'warn' : 'ok');
      const lvlTxt = { ok: s.normal, warn: s.high, crit: s.critical };
      const lvlBar = { ok: c.okDot, warn: c.warnDot, crit: c.critDot };
      const memDet = L.mem_total_mb ? (L.mem_used_mb / 1024).toFixed(1) + ' / ' + (L.mem_total_mb / 1024).toFixed(1) + ' GB' : Math.round(mem) + '%';
      V.res = [[s.cpu, cpu, 'load ' + Number(L.load1 != null ? L.load1 : res.load1 || 0).toFixed(2)], [s.memory, mem, memDet],
        [s.disk, disk, L.disk_free_gb != null ? L.disk_free_gb.toFixed(1) + ' GB free' : '']].map((r) => {
        const k = lvlOf(r[1]);
        return { label: r[0], pct: Math.round(r[1]), stLabel: lvlTxt[k], stFg: c[k], bar: lvlBar[k], detail: r[2] };
      });
      V.resSeg = seg([['local', s.resLocal]], 'local', () => {});
      const rx = L.net_rx_bps != null ? L.net_rx_bps : res.net_rx_bps;
      const tx = L.net_tx_bps != null ? L.net_tx_bps : res.net_tx_bps;
      V.resFoot = [{ label: s.wsConns, value: String(L.ws_conns != null ? L.ws_conns : devCounts.online || 0) }, { label: s.netNow, value: fmtMbps(mbps(rx)).replace(' Mbps', '') + ' / ' + fmtMbps(mbps(tx)) },
        { label: s.uptimeShort, value: srv.up }];
      const sess = ov.sessions || {};
      const bwL = ov.bandwidth || {};
      const bwNow = mbps((L.p2p_bps != null ? L.p2p_bps : bwL.p2p_bps) || 0) + mbps((L.relay_bps != null ? L.relay_bps : bwL.relay_bps) || 0) + mbps((L.frp_bps != null ? L.frp_bps : bwL.frp_bps) || 0);
      const frp = ov.frp || {};
      const pendN = pendDev.length + pendGr.length;
      V.kpis = [
        { label: s.kOnline, value: devCounts.online || 0, unit: ' / ' + (devCounts.total || 0), sub: F(s.offlineN, { n: devCounts.offline || 0 }), subFg: c.muted, icon: I.devices, iconFg: c.accent, iconBg: c.accentSoft, go: go2('devices') },
        { label: s.kP2p, value: sess.p2p || 0, unit: '', sub: s.p2pDirectSub, subFg: c.muted, icon: I.zap, iconFg: c.ok, iconBg: c.okSoft, go: go2('comms') },
        { label: s.kRelay, value: sess.relay || 0, unit: '', sub: s.relaySub.split(' · ')[0], subFg: c.muted, icon: I.relay, iconFg: c.warn, iconBg: c.warnSoft, go: go2('comms') },
        { label: s.kBw, value: bwNow >= 10 ? Math.round(bwNow) : bwNow.toFixed(1), unit: ' Mbps', sub: 'P2P ' + fmtMbps(p2pV[29]) + ' · ' + s.mixRelay + ' ' + fmtMbps(relV[29]), subFg: c.muted, icon: I.activity, iconFg: c.accent, iconBg: c.accentSoft, go: go2('monitor') },
        { label: s.kFrp, value: frp.active || 0, unit: ' / ' + (frp.total || 0), sub: (frp.nodes_online || 0) + ' / ' + (frp.nodes_total || 0) + ' frps', subFg: c.muted, icon: I.tunnel, iconFg: c.accent, iconBg: c.accentSoft, go: go2('frp') },
        { label: s.kPend, value: pendN, unit: '', sub: pendN ? s.needsAction : s.allClear, subFg: pendN ? c.warn : c.ok, icon: I.userCheck, iconFg: c.warn, iconBg: c.warnSoft, go: go2('access') },
      ];
      const pend = pendDev.map((d) => Object.assign({ title: d.name, meta: F(s.devPendingMeta, { user: d.user || '—' }) + ' · ' + d.did, icon: kindIcon(d.kind), approve: () => approveDev(d), reject: () => rejectDev(d) }, lv(d.level)))
        .concat(pendGr.map((g) => Object.assign({ title: g.requester + '  →  ' + g.target, meta: (g.reason || '—') + ' · ' + (({ '24h': s.d24h, '7d': s.d7, '30d': s.d30, '90d': s.d90, perm: s.dPerm })[g.duration] || g.duration), icon: I.userCheck,
          approve: () => grantAct(g, 'approve'), reject: () => grantAct(g, 'reject') }, lv(g.level))));
      if (!canOp) pend.forEach((p) => { p.approve = () => flash(s.roleViewer); p.reject = p.approve; });
      V.pendTop = pend.slice(0, 4);
      V.pendCount = pendN;
      V.noPend = pendN === 0;
      V.alertsTop = openAlerts.slice(0, 5).map((a) => Object.assign({ title: a.title, src: a.source || a.category, t: fmtTime(a.created_at).slice(6) }, sevMap[a.severity] || sevMap.info));
      if (!V.alertsTop.length) V.alertsTop = [Object.assign({ title: s.alertsNone, src: 'meshportd', t: s.now }, sevMap.info)];
      const p2pS = D['metrics/p2p'] || {};
      const otherN = (sess.nego || 0) + (sess.fail || 0);
      const total = (sess.p2p || 0) + (sess.relay || 0) + otherN || 1;
      V.mix = {
        rate: p2pS.attempted ? Number(p2pS.rate || 0).toFixed(1) : '—', aria: F(s.mixAria, { p: sess.p2p || 0, r: sess.relay || 0, o: otherN }),
        segs: [
          { label: s.mixP2p, n: sess.p2p || 0, pct: Math.round(((sess.p2p || 0) / total) * 100), color: c.s1 },
          { label: s.mixRelay, n: sess.relay || 0, pct: Math.round(((sess.relay || 0) / total) * 100), color: c.s2 },
          { label: s.mixOther, n: otherN, pct: Math.round((otherN / total) * 100), color: c.swOff },
        ],
      };
    }

    // ---------- devices
    if (page === 'devices') {
      const counts = (D.devices && D.devices.counts) || {};
      const f = ST.devFilter;
      const match = devList.filter((d) => f === 'all' || devState(d) === f).filter((d) => has([d.name, d.did, d.ip, d.lan_ip, d.user, d.group, d.os].join(' ')));
      V.devRows = match.map((d, i) => {
        const k = devState(d);
        const tg = { pending: [s.approve, I.check, c.ok], banned: [s.unban, I.play, c.ok], disabled: [s.enable, I.play, c.ok], online: [s.disable, I.ban, c.warn], offline: [s.disable, I.ban, c.warn] }[k];
        return Object.assign({
          name: d.name, did: d.did, os: d.os || '—', ip: d.ip || '—', lan: d.lan_ip || '', icon: kindIcon(d.kind), user: d.user || '—', group: d.group || '—',
          last: k === 'online' ? s.onlineNow : d.last_seen_at ? ago(d.last_seen_at) : '—', div: i ? c.hair : 'transparent', op: k === 'disabled' || k === 'banned' ? 0.55 : 1,
          detail: () => openDrawer(d), toggle: () => toggleDev(d), toggleLabel: tg[0], toggleIcon: tg[1], toggleFg: tg[2], remove: () => removeDev(d),
        }, devStatus(d), lv(d.level));
      });
      const segOpts = [['all', s.all + ' ' + (counts.all || devList.length)], ['online', s.online + ' ' + (counts.online || 0)], ['offline', s.offline + ' ' + (counts.offline || 0)]];
      if (counts.pending) segOpts.push(['pending', s.stPendingDev + ' ' + counts.pending]);
      segOpts.push(['disabled', s.disabled + ' ' + (counts.disabled || 0)]);
      if (counts.banned) segOpts.push(['banned', s.stBanned + ' ' + counts.banned]);
      V.devSeg = seg(segOpts, f, (v) => set2({ devFilter: v }));
      V.devEmpty = V.devRows.length === 0;
      V.devShowing = F(s.showing, { n: V.devRows.length, total: devList.length });
    }

    // ---------- device drawer
    if (ST.overlay === 'drawer' && ST.drawerId) {
      const det = D.dev[ST.drawerId];
      const dd = (det && det.device) || devById[ST.drawerId];
      if (dd) {
        const k = devState(dd);
        const pathInfo = { p2p: ['P2P', c.ok, c.okSoft], relay: [s.pathRelay, c.warn, c.warnSoft], nego: [s.pathNego, c.accent, c.accentSoft], fail: [s.pathFail, c.crit, c.critSoft], idle: [s.pathIdle, c.muted, c.tile] };
        const links = ((det && det.sessions) || []).filter((x) => x.state !== 'closed').map((x) => {
          const p = pathInfo[x.path] || pathInfo.idle;
          return { kind: p[0], fg: p[1], bg: p[2], text: x.a_id === dd.id ? x.b : x.a, meta: x.latency_ms == null ? '—' : x.latency_ms + ' ms' };
        }).concat(((det && det.mappings) || []).map((m) => ({ kind: 'FRP', fg: c.accent, bg: c.accentSoft, text: m.name, meta: String(m.public_addr || '').replace(/^https?:\/\//, '') })));
        V.dv = Object.assign({
          name: dd.name, did: dd.did, icon: kindIcon(dd.kind),
          facts: [
            { label: s.thOs, value: dd.os || '—' }, { label: s.thOwner, value: (dd.user || '—') + ' · ' + (dd.group || '—') },
            { label: s.publicIp, value: dd.ip || '—' }, { label: s.lanIp, value: dd.lan_ip || '—' },
            { label: s.thLastSeen, value: k === 'online' ? s.onlineNow : ago(dd.last_seen_at) }, { label: s.registered, value: fmtDate(dd.registered_at) },
            { label: s.keyFp, value: dd.fingerprint || '—' }, { label: 'Client', value: [dd.client_version, dd.nat_type].filter(Boolean).join(' · ') || '—' },
          ],
          lvSeg: seg([['guest', s.lvGuest], ['member', s.lvMember], ['admin', s.lvAdmin]], dd.level, (v) => {
            if (v === dd.level) return;
            act(() => api('PATCH', '/admin/devices/' + dd.id, { level: v }), F(s.tLevelSet, { name: dd.name, level: lvLabel[v] }), () => loadDevice(dd.id));
          }),
          links, noLinks: links.length === 0, isPending: k === 'pending', online: k === 'online' && canOp, canToggle: k !== 'pending' && k !== 'banned',
          approve: () => approveDev(dd), reject: () => rejectDev(dd), edit: () => deviceEditForm(dd), command: () => commandForm(dd),
          toggle: () => toggleDev(dd).then(() => loadDevice(dd.id)), toggleLabel: k === 'disabled' ? s.enable : s.disable, toggleIcon: k === 'disabled' ? I.play : I.ban,
          rotate: () => act(() => api('POST', '/admin/devices/' + dd.id + '/rotate-key', {}), F(s.tRotateReq, { name: dd.name }), () => loadDevice(dd.id)),
          banLabel: k === 'banned' ? s.unban : s.banDevice, banIcon: k === 'banned' ? I.play : I.ban,
          ban: () => (k === 'banned'
            ? act(() => api('POST', '/admin/devices/' + dd.id + '/unban', {}), F(s.tUnbanned, { name: dd.name }), () => loadDevice(dd.id))
            : confirmBox(s.cfBan, F(s.cfBanBody, { name: dd.name }), s.banDevice, () => act(() => api('POST', '/admin/devices/' + dd.id + '/ban', { reason: 'manual' }), F(s.tBanned2, { name: dd.name }), () => loadDevice(dd.id)))),
          remove: () => removeDev(dd),
        }, devStatus(dd));
        V.hasDrawer = true;
      }
    }

    // ---------- comms
    if (page === 'comms') {
      const all = items('sessions');
      const pathInfo = {
        p2p: ['P2P', c.ok, c.okSoft, I.zap], relay: [s.pathRelay, c.warn, c.warnSoft, I.relay], nego: [s.pathNego, c.accent, c.accentSoft, I.refresh],
        fail: [s.pathFail, c.crit, c.critSoft, I.x], idle: [s.pathIdle, c.muted, c.tile, I.dot], none: [s.pathNone, c.muted, c.tile, I.x],
      };
      const stateInfo = { up: ['ok', s.stConnected], nego: ['info', s.stNego], fail: ['crit', s.stFailed], idle: ['off', s.stIdle], closed: ['off', s.stClosed] };
      const modeLbl = { p2p: s.modeP2p, relay: s.modeRelay, auto: s.modeAuto };
      const f = ST.commFilter;
      const match = all.filter((x) => f === 'all' || x.mode === f).filter((x) => has([x.a, x.b, x.via, x.fail_reason].join(' ')));
      const pctOf = (fl) => (fl ? Math.min(100, Math.round(fl.percent || (fl.size ? (fl.done / fl.size) * 100 : 0))) : 0);
      V.commRows = match.map((x, i) => {
        const p = pathInfo[x.state === 'closed' ? 'none' : x.path] || pathInfo.idle;
        const si = stateInfo[x.state] || stateInfo.idle;
        const fp = pctOf(x.file);
        const canCut = canOp && (x.state === 'up' || x.state === 'nego' || x.state === 'idle');
        const pendM = Math.max(0, (x.msgs_sent || 0) - (x.msgs_delivered || 0));
        return Object.assign({
          a: x.a, b: x.b, mode: modeLbl[x.mode] || x.mode, path: p[0], pathFg: p[1], pathBg: p[2], pathIcon: p[3], via: x.via || x.fail_reason || '—',
          lat: x.latency_ms == null ? '—' : x.latency_ms + ' ms', spd: x.state === 'up' && x.rate_bps > 0 ? fmtRate(x.rate_bps) : '—',
          msgA: F(s.msgSent, { d: x.msgs_delivered || 0, t: x.msgs_sent || 0 }), msgB: pendM ? F(s.msgPending, { n: pendM }) : F(s.msgRead, { n: x.msgs_read || 0 }), msgBFg: pendM ? c.warn : c.muted,
          hasFile: !!x.file, noFile: !x.file, fname: x.file ? x.file.name : '', fpct: fp, fbar: fp >= 100 ? c.okDot : c.accentBg,
          fsize: x.file ? (fp >= 100 ? s.fileDone + ' · ' : '') + fmtBytes(x.file.size) : '', div: i ? c.hair : 'transparent',
          cut: () => { if (canCut) act(() => api('POST', '/admin/sessions/' + x.id + '/disconnect', {}), F(s.tCut, { a: x.a, b: x.b })); },
          cutDisabled: canCut ? 'false' : 'true', cutFg: canCut ? c.text : c.faint,
        }, st(si[0], si[1]));
      });
      const up = all.filter((x) => x.state === 'up');
      const p2pUp = up.filter((x) => x.path === 'p2p').length;
      const relayUp = up.filter((x) => x.path === 'relay').length;
      const lat = up.filter((x) => x.latency_ms != null).map((x) => x.latency_ms).sort((a, b) => a - b);
      const avg = lat.length ? Math.round(lat.reduce((a, b) => a + b, 0) / lat.length) : 0;
      const p95 = lat.length ? lat[Math.min(lat.length - 1, Math.floor(lat.length * 0.95))] : 0;
      const rm = (D.sessions && D.sessions.relay_messages_24h) || {};
      const files = all.filter((x) => x.file && x.state === 'up' && pctOf(x.file) < 100).length;
      V.commTiles = [
        { label: s.tActive, value: up.length, unit: ' / ' + all.length, sub: F(s.tNego, { n: all.filter((x) => x.state === 'nego').length }) },
        { label: s.tP2pShare, value: up.length ? Math.round((p2pUp / up.length) * 100) : 0, unit: '%', sub: F(s.p2pRelayCount, { p: p2pUp, r: relayUp }) },
        { label: s.tAvgLat, value: avg, unit: ' ms', sub: 'p95 · ' + p95 + ' ms' },
        { label: s.tPendMsgs, value: rm.queued || 0, unit: '', sub: s.awaitingPeer },
        { label: s.tFiles, value: files, unit: '', sub: F(s.inProgress, { n: files }) },
      ];
      const cnt = (m) => all.filter((x) => x.mode === m).length;
      V.commSeg = seg([['all', s.all + ' ' + all.length], ['p2p', s.modeP2p + ' ' + cnt('p2p')], ['relay', s.modeRelay + ' ' + cnt('relay')], ['auto', s.modeAuto + ' ' + cnt('auto')]], f, (v) => set2({ commFilter: v }));
      V.commEmpty = V.commRows.length === 0;
    }

    // ---------- FRP
    const nodes = items('frp/nodes');
    const maps = items('frp/mappings');
    if (page === 'frp') {
      const clients = items('frp/clients');
      const mState = (m) => (!m.enabled ? 'stopped' : m.status === 'online' ? 'running' : m.status === 'error' ? 'error' : 'waiting');
      V.frpsCards = nodes.map((n) => {
        const nm = maps.filter((m) => m.node_id === n.id);
        const stt = !n.enabled ? st('off', s.disabled) : n.status === 'online' ? st('ok', s.running) : n.status === 'offline' ? st('crit', s.offline) : st('off', s.stUnknown);
        return Object.assign({
          name: n.name, ver: n.version || '—', host: n.public_addr, ip: n.region || (n.managed ? 'managed' : 'remote'),
          clients: n.frpc_clients || 0, proxies: (n.mappings_online || 0) + ' / ' + (n.mappings || 0), conns: nm.reduce((a, m) => a + (m.conns || 0), 0),
          ports: [n.bind_port, n.vhost_http_port, n.vhost_https_port].filter(Boolean).join(' · ') || '—',
          traffic: fmtRate(nm.reduce((a, m) => a + (m.rate_in || 0) + (m.rate_out || 0), 0)), err: n.last_error || '', hasErr: !!n.last_error,
          canManage: admin, managed: !!n.managed, edit: () => nodeForm(n),
          config: async () => { try { const txt = await api('GET', '/admin/frp/nodes/' + n.id + '/config', undefined, { text: true }); infoSheet(F(s.nodeToml, { name: n.name }), s.nodeTomlDesc, [['frps.toml', txt]]); } catch (e) { flash(e.message); } },
          restart: () => confirmBox(F(s.cfRestart, { name: n.name }), s.cfRestartBody, s.restart, () => act(() => api('POST', '/admin/frp/nodes/' + n.id + '/restart', {}), F(s.tNodeRestarted, { name: n.name }))),
          remove: () => confirmBox(s.cfDelNode, F(s.cfDelNodeBody, { name: n.name }), s.delete, () => act(() => api('DELETE', '/admin/frp/nodes/' + n.id), F(s.tDeleted, { name: n.name }))),
        }, stt);
      });
      V.noNodes = nodes.length === 0;
      V.frpcRows = clients.map((x) => {
        const d = devById[x.device_id];
        return Object.assign({ name: x.device || x.device_id, icon: d ? kindIcon(d.kind) : I.server, ver: x.version || '—', count: F(s.nProxies, { n: maps.filter((m) => m.device_id === x.device_id).length }) }, st('ok', s.online));
      });
      V.frpcCount = F(s.nProxies, { n: maps.length }) + ' · ' + clients.length + ' frpc';
      const f = ST.frpFilter;
      const runN = maps.filter((m) => mState(m) === 'running').length;
      const match = maps.filter((m) => f === 'all' || (f === 'running' ? mState(m) === 'running' : mState(m) !== 'running'))
        .filter((m) => has([m.name, m.device, m.node, (m.domains || []).join(' '), m.local_port, m.remote_port, m.public_addr].join(' ')));
      V.mapRows = match.map((m, i) => {
        const ms = mState(m);
        const stt = ms === 'running' ? st('ok', s.running) : ms === 'stopped' ? st('off', s.stopped) : ms === 'error' ? st('crit', s.stFailed) : st('warn', s.stError);
        return Object.assign({
          name: m.name, client: m.device, server: m.node, proto: String(m.proto).toUpperCase(), local: m.local_ip + ':' + m.local_port,
          pub: m.proto === 'stcp' || m.proto === 'xtcp' ? m.proto + ' · ' + s.visitorOnly : m.public_addr || '—',
          tin: ms === 'running' ? fmtRate(m.rate_in) : '—', tout: ms === 'running' ? fmtRate(m.rate_out) : '—', conns: m.conns || 0, div: i ? c.hair : 'transparent',
          edit: () => openMapForm(m),
          toggle: () => act(() => api('POST', '/admin/frp/mappings/' + m.id + (m.enabled ? '/stop' : '/start'), {}), F(m.enabled ? s.tMapOff : s.tMapOn, { name: m.name })),
          toggleLabel: m.enabled ? s.stop : s.start, toggleIcon: m.enabled ? I.stop : I.play, toggleFg: m.enabled ? c.crit : c.ok,
          remove: () => confirmBox(s.cfDelMap, F(s.cfDelMapBody, { name: m.name }), s.delete, () => act(() => api('DELETE', '/admin/frp/mappings/' + m.id), F(s.tDeleted, { name: m.name }))),
        }, stt);
      });
      V.frpSeg = seg([['all', s.all + ' ' + maps.length], ['running', s.running + ' ' + runN], ['stopped', s.stopped + ' / ' + s.stError + ' ' + (maps.length - runN)]], f, (v) => set2({ frpFilter: v }));
      V.frpTotals = F(s.frpTotals, { run: runN, total: maps.length });
      V.frpEmpty = V.mapRows.length === 0;
      V.stopAllFrp = () => confirmBox(s.cfStopAll, s.cfStopAllBody, s.stopAllFrp, () => act(() => api('POST', '/admin/frp/stop-all', {}), s.tStopAll));
      V.openAddMap = () => openMapForm(null);
    }
    if (ST.overlay === 'map' && ST.mf) {
      const f = ST.mf;
      const web = f.proto === 'http' || f.proto === 'https';
      const secret = f.proto === 'stcp' || f.proto === 'xtcp';
      const node = nodes.find((n) => n.id === f.node) || {};
      const setMf = (k) => (e) => set2({ mf: Object.assign({}, ST.mf, { [k]: e.target.value }) });
      const pickMf = (k) => (v) => set2({ mf: Object.assign({}, ST.mf, { [k]: v }) });
      const eligible = devList.filter((d) => d.status === 'active' && (d.level === 'member' || d.level === 'admin'));
      V.mf = {
        title: f.id ? s.editMapping : s.newMapping, name: f.name, client: f.device, lip: f.lip, lport: f.lport, remote: f.remote,
        remoteLabel: web ? s.domain : secret ? s.secretKey : s.remotePort,
        preview: web ? f.proto + '://' + (f.remote || '…') + '  →  ' + f.lip + ':' + f.lport : secret ? s.stcpPreview : (node.public_addr || '…') + ':' + (f.remote || '…') + '  →  ' + f.lip + ':' + f.lport,
        clients: (f.id ? devList.filter((d) => d.id === f.device) : eligible).map((d) => ({ v: d.id, label: d.name + ' · ' + (lvLabel[d.level] || d.level) })),
        protoSeg: seg([['http', 'HTTP'], ['https', 'HTTPS'], ['tcp', 'TCP'], ['udp', 'UDP'], ['stcp', 'STCP'], ['xtcp', 'XTCP']], f.proto, pickMf('proto')),
        srvSeg: seg(nodes.map((n) => [n.id, n.name]), f.node, pickMf('node')),
        setName: setMf('name'), setClient: setMf('device'), setLip: setMf('lip'), setLport: setMf('lport'), setRemote: setMf('remote'),
        saveLabel: f.busy ? '…' : f.id ? s.save : s.create, err: f.err, hasErr: !!f.err, save: saveMap,
      };
      V.hasMapModal = true;
    }

    // ---------- access
    if (page === 'access') {
      const grants = items('grants');
      const f = ST.accFilter;
      const durLbl = { '24h': s.d24h, '7d': s.d7, '30d': s.d30, '90d': s.d90, perm: s.dPerm };
      const aSt = { pending: ['warn', s.stPending], approved: ['ok', s.stApproved], rejected: ['crit', s.stRejected], revoked: ['off', s.stRevoked], expired: ['off', s.stExpired] };
      const isHist = (g) => g.status === 'rejected' || g.status === 'revoked' || g.status === 'expired';
      const list = grants.filter((g) => f === 'all' || (f === 'history' ? isHist(g) : g.status === f)).filter((g) => has([g.requester, g.target, g.reason].join(' ')));
      V.accRows = list.map((g, i) => {
        const rq = devById[g.requester_id];
        const tg = devById[g.target_id];
        const a = aSt[g.status] || aSt.expired;
        return Object.assign({
          app: g.requester || '—', appUser: rq ? rq.user : '', appIcon: rq ? kindIcon(rq.kind) : I.devices, target: g.target, targetId: tg ? tg.did : '',
          reason: g.reason || '—', dur: durLbl[g.duration] || g.duration || '—',
          until: g.status === 'pending' ? s.afterApprove : g.expires_at ? F(s.until, { d: fmtDate(g.expires_at) }) : '—',
          at: fmtTime(g.created_at), div: i ? c.hair : 'transparent',
          isPending: g.status === 'pending' && canOp, isApproved: g.status === 'approved' && canOp, isClosed: isHist(g),
          closedBy: [g.decided_by, g.decided_at ? fmtTime(g.decided_at) : ''].filter(Boolean).join(' · '),
          approve: () => grantAct(g, 'approve'), reject: () => grantAct(g, 'reject'), revoke: () => grantAct(g, 'revoke'),
        }, st(a[0], a[1]), lv(g.level));
      });
      const cntS = (k) => grants.filter((g) => g.status === k).length;
      V.accSeg = seg([['pending', s.pending + ' ' + cntS('pending')], ['approved', s.approved + ' ' + cntS('approved')], ['history', s.history + ' ' + grants.filter(isHist).length], ['all', s.all + ' ' + grants.length]], f, (v) => set2({ accFilter: v }));
      V.accEmpty = V.accRows.length === 0;
      const lvMap = {};
      items('levels').forEach((l) => { lvMap[l.level] = l; });
      V.levelDefs = ['guest', 'member', 'admin'].map((k) => {
        const l = lv(k);
        return { label: l.lvLabel, fg: l.lvFg, bg: l.lvBg, desc: s['lv' + k.charAt(0).toUpperCase() + k.slice(1) + 'Desc'], count: lvMap[k] ? lvMap[k].devices : 0 };
      });
      V.userRows = items('users').map((u) => Object.assign({
        name: u.display_name ? u.display_name + ' · ' + u.username : u.username, ini: u.username.slice(0, 2).toUpperCase(), devices: u.devices || '—', op: u.disabled ? 0.55 : 1,
        canEdit: admin, edit: () => userForm(u),
      }, roleLv(u.role), u.mfa_enabled ? st('ok', 'TOTP') : st('warn', s.mfaNone)));
      V.addUser = () => userForm(null);
    }

    // ---------- security
    if (page === 'security') {
      const logins = items('security/logins');
      const f = ST.loginFilter;
      const abnN = logins.filter((l) => l.abnormal && minsAgo(l.at) < 1440).length;
      V.loginRows = logins.filter((l) => f === 'all' || l.abnormal).filter((l) => has([l.subject, l.ip, l.user_agent].join(' '))).slice(0, 10).map((l) => Object.assign({
        t: fmtTime(l.at), user: l.subject || '—', ip: l.ip || '—', loc: (l.kind === 'device' ? s.thDevice : s.thAccount) + ' · ' + (l.method || '—'),
        client: shortUA(l.user_agent), abn: !!l.abnormal, abnLabel: l.abnormal_reason || s.abnormal, rowBg: l.abnormal ? c.abnRow : 'transparent',
        canUnblock: !l.success && l.kind === 'user' && canOp && !!l.ip,
        unblock: () => act(() => api('POST', '/admin/security/unblock', { ip: l.ip }), s.unblock + ' · ' + l.ip),
      }, l.success ? st('ok', s.resOk) : st('crit', l.reason ? s.stFailed + ' · ' + l.reason : s.stFailed)));
      V.loginSeg = seg([['all', s.all], ['abn', s.abnormal + ' ' + logins.filter((l) => l.abnormal).length]], f, (v) => set2({ loginFilter: v }));
      const alerts = items('alerts').filter((a) => a.status !== 'resolved');
      V.secAlerts = alerts.slice(0, 6).map((a) => Object.assign({ title: a.title, t: fmtTime(a.created_at) }, sevMap[a.severity] || sevMap.info));
      if (!V.secAlerts.length) V.secAlerts = [Object.assign({ title: s.alertsNone, t: s.now }, sevMap.info)];
      const keys = items('security/keys');
      const maxAge = (D['security/keys'] && D['security/keys'].max_age_days) || 180;
      s.rotateEvery = s0.rotateEvery.replace(/\d+/, String(maxAge));
      V.keyRows = keys.map((k) => {
        const pend = k.state === 'rotation_pending';
        const fp = String(k.fingerprint || '');
        return Object.assign({
          name: k.device, fp: fp.length > 30 ? fp.slice(0, 18) + '…' + fp.slice(-6) : fp, age: k.age_days ? F(s.rotatedAgo, { d: k.age_days }) : s.today,
          rotate: () => { if (!pend && canOp) act(() => api('POST', '/admin/devices/' + k.device_id + '/rotate-key', {}), F(s.tRotateReq, { name: k.device })); },
          rotDisabled: pend || !canOp ? 'true' : 'false', rotFg: pend || !canOp ? c.faint : c.text,
        }, pend ? st('info', s.keyPending) : k.state === 'ok' ? st('ok', s.keyValid) : st('warn', s.keyDue));
      });
      const keysDue = keys.filter((k) => k.state !== 'ok').length;
      const mfa = D['security/mfa'] || {};
      const users = items('users');
      const missing = {};
      (mfa.missing || []).forEach((u) => { missing[u.id] = u; });
      V.mfaRows = users.map((u) => Object.assign({
        name: u.username, ini: u.username.slice(0, 2).toUpperCase(), canRemind: !u.mfa_enabled && canOp && !(missing[u.id] && missing[u.id].reminded_at),
        remind: () => act(() => api('POST', '/admin/security/mfa/remind', { users: [u.id] }), F(s.tReminded, { name: u.username })),
      }, u.mfa_enabled ? st('ok', 'TOTP') : st('warn', s.mfaNone)));
      V.mfaSw = sw(!!mfa.required, () => {
        if (!admin) return;
        act(() => api('PUT', '/admin/security/mfa', { required: !mfa.required }), mfa.required ? s.mfaReqOff : s.mfaReqOn);
      }, s.requireMfa);
      const toks = items('security/tokens').filter((t) => !t.revoked_at);
      V.tokRows = toks.map((t) => {
        const lm = minsAgo(t.last_used_at);
        return {
          name: t.name, scope: (t.scopes || []).join(', ') || '—', last: t.last_used_at ? F(s.lastUsed, { t: ago(t.last_used_at) }) : s.lastUsedNever,
          lastFg: lm != null && lm > 90 * 1440 ? c.warn : c.muted, exp: t.expires_at ? fmtDate(t.expires_at) : s.never,
          revoke: () => confirmBox(s.cfRevokeTok, F(s.cfRevokeTokBody, { name: t.name }), s.revoke, () => act(() => api('DELETE', '/admin/security/tokens/' + t.id), F(s.tTokRevoked, { name: t.name }))),
        };
      });
      V.tokEmpty = V.tokRows.length === 0;
      V.createToken = () => tokenForm(false);
      const acl = items('security/acl');
      const allow = acl.filter((r) => r.action === 'allow');
      const deny = acl.filter((r) => r.action === 'deny');
      const curL = ST.aclMode === 'allow' ? allow : deny;
      V.aclRows = curL.map((r) => ({
        cidr: r.cidr, note: r.note || r.created_by || '', icon: ST.aclMode === 'allow' ? I.check : I.ban, fg: ST.aclMode === 'allow' ? c.ok : c.crit,
        remove: () => act(() => api('DELETE', '/admin/security/acl/' + r.id), F(s.tAclRemoved, { cidr: r.cidr })),
      }));
      V.aclSeg = seg([['allow', s.allow + ' ' + allow.length], ['deny', s.deny + ' ' + deny.length]], ST.aclMode, (v) => set2({ aclMode: v }));
      V.aclDraft = ST.aclDraft;
      V.setAclDraft = (e) => set2({ aclDraft: e.target.value });
      V.addAcl = () => {
        const v = ST.aclDraft.trim();
        if (!/^[0-9a-fA-F:.]+(\/\d{1,3})?$/.test(v)) { flash(s.tBadCidr); return; }
        const doAdd = () => act(() => api('POST', '/admin/security/acl', { action: ST.aclMode, cidr: v, note: '' }), F(s.tAclAdded, { list: ST.aclMode === 'allow' ? s.allow : s.deny, cidr: v }), () => set2({ aclDraft: '' }));
        if (ST.aclMode === 'allow' && !allow.length) {
          const ip = (D['security/acl'] && D['security/acl'].your_ip) || '—';
          confirmBox(s.aclLockTitle, F(s.aclLockBody, { ip }), s.add, doAdd);
        } else doAdd();
      };
      const bans = items('security/bans');
      V.banRows = bans.map((b) => ({ name: b.name, did: b.did, reason: b.ban_reason || '—', unban: () => act(() => api('POST', '/admin/devices/' + b.id + '/unban', {}), F(s.tUnbanned, { name: b.name })) }));
      V.banEmpty = V.banRows.length === 0;
      V.banCount = String(bans.length);
      const noMfa = users.filter((u) => !u.mfa_enabled).length;
      V.secTiles = [
        { label: s.tAbn, value: abnN, sub: abnN ? s.needsReview : s.allClear, fg: abnN ? c.crit : c.ok, icon: I.alert },
        { label: s.tMfa, value: (users.length - noMfa) + ' / ' + users.length, sub: noMfa ? F(s.mfaCover, { n: noMfa }) : s.mfaCoverZero, fg: noMfa ? c.warn : c.ok, icon: I.lock },
        { label: s.tKeysDue, value: keysDue, sub: s.keysDueSub.replace(/\d+/, String(maxAge)), fg: keysDue ? c.warn : c.ok, icon: I.key },
        { label: s.tTokens, value: toks.length, sub: F(s.tokensSub, { n: toks.filter((t) => { const m = minsAgo(t.last_used_at); return m != null && m > 90 * 1440; }).length }), fg: c.muted, icon: I.key },
        { label: s.tBanned, value: bans.length, sub: F(s.bannedSub, { n: deny.length }), fg: c.muted, icon: I.ban },
      ];
    }

    // ---------- monitor
    if (page === 'monitor') {
      const M = D.metrics || {};
      const ser = M.series || {};
      const range = ST.range;
      const span = { '1h': 3600, '24h': 86400, '7d': 7 * 86400 }[range];
      const N = 24;
      const now = Date.now() / 1000;
      const t0 = now - span;
      const step = span / N;
      const bucket = (pts, fn) => {
        const sum = new Array(N).fill(0);
        const cnt = new Array(N).fill(0);
        (pts || []).forEach((p) => {
          const i = Math.floor((p.t - t0) / step);
          if (i >= 0 && i < N) { sum[i] += fn ? fn(p.v) : p.v; cnt[i]++; }
        });
        let lastV = null;
        return sum.map((v, i) => { if (cnt[i]) lastV = v / cnt[i]; return cnt[i] ? v / cnt[i] : lastV == null ? 0 : lastV; });
      };
      const xlR = range === '1h' ? ['-60m', '-45m', '-30m', '-15m', s.now] : range === '24h' ? ['-24h', '-18h', '-12h', '-6h', s.now] : ['-7d', '-5d', '-3d', '-1d', s.now];
      const xAtR = (i, n) => {
        const back = (n - 1 - i) * step;
        if (back < 1) return s.now;
        return back < 3600 ? '-' + Math.round(back / 60) + 'm' : back < 86400 * 2 ? '-' + Math.round(back / 3600) + 'h' : '-' + Math.round(back / 86400) + 'd';
      };
      const pct = (v) => Math.round(v) + '%';
      const lt = M.latest || {};
      const cpuV = bucket(ser.cpu);
      const memV = bucket(ser.mem);
      const rxV = bucket(ser.net_rx_bps, mbps);
      const txV = bucket(ser.net_tx_bps, mbps);
      const netV = rxV.map((v, i) => v + txV[i]);
      const relV = bucket(ser.relay_bps, mbps);
      V.cpu = mk('cpu', [{ name: s.cpu, vals: cpuV, color: c.bar, fill: true }], { max: 100, ax: pct, fmt: pct, xl: xlR, xAt: xAtR, nowTxt: lt.cpu != null ? pct(lt.cpu) : '—' });
      V.mem = mk('mem', [{ name: s.memory, vals: memV, color: c.bar, fill: true }], { max: 100, ax: pct, fmt: pct, xl: xlR, xAt: xAtR, nowTxt: lt.mem != null ? pct(lt.mem) : '—' });
      V.net = mk('net', [{ name: s.network, vals: netV, color: c.bar, fill: true }], { max: niceMax(Math.max.apply(null, netV), 1), ax: axNum, fmt: fmtMbps, xl: xlR, xAt: xAtR,
        nowTxt: fmtMbps(mbps((lt.net_rx_bps || 0) + (lt.net_tx_bps || 0))) });
      const p2pS = D['metrics/p2p'] || {};
      let rateV = (ser.p2p_success || []).length ? bucket(ser.p2p_success) : bucket((p2pS.items || []).map((x) => ({ t: new Date(x.hour).getTime() / 1000 + 1800, v: x.rate })));
      const rmin = Math.min.apply(null, rateV);
      const rlo = Math.max(0, Math.min(80, Math.floor((rmin - 5) / 10) * 10));
      V.rate = mk('rate', [{ name: s.p2pSuccess, vals: rateV, color: c.s1 }], { min: rlo, max: 100, ax: pct, fmt: (v) => v.toFixed(1) + '%', xl: xlR, xAt: xAtR,
        nowTxt: p2pS.attempted ? Number(p2pS.rate).toFixed(1) + '%' : '—' });
      V.relay = mk('relay', [{ name: s.relayBw, vals: relV, color: c.s2, fill: true }], { max: niceMax(Math.max.apply(null, relV), 1), ax: axNum, fmt: fmtMbps, xl: xlR, xAt: xAtR, nowTxt: fmtMbps(mbps(lt.relay_bps)) });
      V.relaySub = s.relaySub;
      s.p2pSuccessSub = p2pS.attempted ? p2pS.succeeded + ' / ' + p2pS.attempted + ' · %' : s.p2pNoData;
      // FRP traffic per bucket = average throughput × bucket length
      const frpB = bucket(ser.frp_bps).map((v) => v * step);
      const maxB = Math.max.apply(null, frpB);
      const unit = maxB >= 1e9 ? ['GB', 1e9] : maxB >= 1e6 ? ['MB', 1e6] : ['KB', 1e3];
      const fv = frpB.map((v) => v / unit[1]);
      const fmax = niceMax(Math.max.apply(null, fv), 1);
      const fhI = hv && hv.k === 'frp' ? hv.i : -1;
      const fhx = fhI >= 0 ? ((fhI + 0.5) / fv.length) * 100 : 0;
      s.frpTrendSub = s0.frpTrendSub + ' · ' + unit[0];
      V.frpBars = {
        grid: [1, 0.5, 0].map((x) => ({ top: (1 - x) * 100, label: axNum(fmax * x) })),
        bars: fv.map((v, i) => ({ h: (v / fmax) * 100, color: i === fhI ? c.bar : c.barDim, enter: () => set2({ hov: { k: 'frp', i } }) })),
        leave: () => set2({ hov: null }), hov: fhI >= 0, tipX: fhx, tipShift: fhx > 62 ? 'calc(-100% - 12px)' : '12px',
        tipTitle: fhI >= 0 ? xAtR(fhI, fv.length) : '', tipRows: fhI >= 0 ? [{ label: s.frpTrend, value: fv[fhI].toFixed(2) + ' ' + unit[0], color: c.bar }] : [],
        xl: xlR.map((t) => ({ t })), total: fmtBytes(frpB.reduce((a, b) => a + b, 0)),
      };
      V.rangeSeg = seg([['1h', '1h'], ['24h', '24h'], ['7d', '7d']], range, (v) => { set2({ range: v, hov: null }); loadPage(); });
      const kindSt = { p2p: ['warn', s.kindP2p], relay: ['crit', s.kindRelay], frp: ['crit', s.kindFrp], auth: ['crit', s.kindAuth] };
      const fails = items('failures');
      V.failRows = fails.slice(0, 12).map((x) => Object.assign({ t: fmtTime(x.at).slice(6), parties: (x.a || '—') + (x.b ? ' ↔ ' + x.b : ''), reason: x.reason || x.detail || '—' }, st((kindSt[x.kind] || ['off', x.kind])[0], (kindSt[x.kind] || ['off', x.kind])[1])));
      const l24 = (D.failures && D.failures.last_24h) || {};
      V.failCount = Object.keys(l24).reduce((a, k) => a + l24[k], 0) + ' · 24h';
      const lvFg = { INFO: '#7FD3BC', WARN: '#F6B556', ERROR: '#FF8A7A', DEBUG: '#7C848C' };
      const lvF = ST.logLv;
      V.logRows = logs.filter((l) => lvF === 'all' || String(l.level).toUpperCase() === lvF).slice(0, 14).reverse()
        .map((l) => { const u = String(l.level || '').toUpperCase(); return { t: fmtClock(l.ts), lv: u, src: l.source || '—', msg: l.msg, fg: lvFg[u] || '#D7DCE0' }; });
      if (!V.logRows.length) V.logRows = [{ t: fmtClock(new Date()), lv: '—', src: 'meshportd', msg: s.noLogs, fg: '#7C848C' }];
      V.logSeg = seg([['all', s.logAll], ['INFO', 'INFO'], ['WARN', 'WARN'], ['ERROR', 'ERROR']], lvF, (v) => set2({ logLv: v }));
      V.pausedStr = ST.paused ? 'true' : 'false';
      V.pauseIcon = ST.paused ? I.play : I.pause;
      V.pauseLabel = ST.paused ? s.resume : s.pause;
      V.togglePause = () => set2({ paused: !ST.paused });
      const alSt = { open: ['warn', s.stOpen], acked: ['info', s.stAck], resolved: ['ok', s.stResolved] };
      V.alertHist = items('alerts').slice(0, 12).map((a) => Object.assign({
        title: a.title, t: fmtTime(a.created_at), dur: (a.count > 1 ? '×' + a.count + ' · ' : '') + (a.source || a.category || ''),
        canResolve: a.status !== 'resolved' && canOp, resolve: () => act(() => api('POST', '/admin/alerts/' + a.id + '/resolve', {}), s.stResolved),
      }, sevMap[a.severity] || sevMap.info, st((alSt[a.status] || alSt.open)[0], (alSt[a.status] || alSt.open)[1])));
      if (!V.alertHist.length) V.alertHist = [Object.assign({ title: s.alertsNone, t: s.now, dur: 'meshportd', canResolve: false }, sevMap.info, st('ok', s.stResolved))];
    }

    // ---------- system
    if (page === 'system') {
      const roleOf = { api: 'HTTP API', hub: s.roleSignal, database: s.roleDb, turn: s.roleStun };
      V.svcRows = ((D.status && D.status.services) || []).map((v) => {
        const b = !!ST.busy[v.name];
        const ok = v.status === 'running' || v.status === 'online';
        const canR = v.restartable && admin && !b;
        return Object.assign({
          name: v.name, role: roleOf[v.name] || (v.kind === 'frps' ? s.roleFrps : v.kind), detail: v.detail || '', up: v.name === 'api' ? srv.up : '',
          busy: canR ? 'false' : 'true', btn: b ? s.restarting : s.restart,
          restart: () => {
            if (!canR) return;
            confirmBox(F(s.cfRestart, { name: v.name }), s.cfRestartBody, s.restart, async () => {
              set2({ busy: Object.assign({}, ST.busy, { [v.name]: true }) });
              await act(() => api('POST', '/admin/system/services/' + encodeURIComponent(v.name) + '/restart', {}), F(s.tRestartReq, { name: v.name }));
              setTimeout(() => set2({ busy: Object.assign({}, ST.busy, { [v.name]: false }) }), 2500);
            });
          },
        }, b ? st('info', s.restarting) : ok ? st('ok', s.running) : v.status === 'external' || v.status === 'unknown' ? st('off', v.status === 'external' ? 'external' : s.stUnknown) : st('crit', v.status));
      });
      V.svcSummary = F(s.svcSummary, { ok: V.svcRows.filter((v) => v.stFg === c.ok).length, total: V.svcRows.length });
      const db = D['system/db'] || {};
      const lt = (D.metrics && D.metrics.latest) || LIVE.last || {};
      const pages = db.pages || 0;
      const used = pages ? Math.round(((pages - (db.free_pages || 0)) / pages) * 100) : 0;
      const rows = db.rows ? Object.keys(db.rows).reduce((a, k) => a + db.rows[k], 0) : 0;
      V.db = st('ok', s.healthy);
      V.dbMeters = [
        { label: s.diskUse, detail: lt.disk != null ? Math.round(lt.disk) + '% · ' + (lt.disk_free_gb != null ? lt.disk_free_gb.toFixed(1) + ' GB free' : '') : '—', pct: Math.round(lt.disk || 0), bar: (lt.disk || 0) >= 90 ? c.critDot : (lt.disk || 0) >= 75 ? c.warnDot : c.okDot },
        { label: s.dbPages, detail: (pages - (db.free_pages || 0)) + ' / ' + pages, pct: used, bar: c.okDot },
      ];
      V.dbFacts = [{ label: s.dbEngine, value: db.engine || '—' }, { label: s.dbSize, value: fmtBytes((db.size_bytes || 0) + (db.wal_bytes || 0)) },
        { label: s.dbRows, value: rows.toLocaleString() }, { label: s.dbSchema, value: db.schema_version != null ? 'v' + db.schema_version : '—' }];
      const latest = upd.latest || upd.current;
      V.verRows = items('system/versions').map((v) => {
        const isCore = v.name === 'meshportd';
        const lat = isCore ? latest || v.version : v.version;
        const fresh = !isCore || !upd.available;
        return { name: v.name, cur: v.version, lat: lat || '—', label: fresh ? s.upToDate : s.updAvailShort, fg: fresh ? c.ok : c.accent, bg: fresh ? c.okSoft : c.accentSoft };
      });
      const bk = D['system/backups'] || {};
      const sched = bk.schedule || {};
      s.backupPolicy = F(s0.backupPolicy, { h: pad(sched.hour_utc != null ? sched.hour_utc : 3), n: sched.keep || 14 });
      const bks = bk.items || [];
      const running = bks.some((b) => b.status === 'running');
      V.bkRows = bks.slice(0, 6).map((b) => Object.assign({
        t: fmtFull(b.created_at), kind: b.kind === 'manual' ? s.manual : s.auto, size: b.size ? fmtBytes(b.size) : '—',
        dl: () => { if (b.status === 'ok') { location.href = '/api/v1/admin/system/backups/' + encodeURIComponent(b.id) + '/download'; flash(s.tDownload); } },
      }, b.status === 'running' ? st('info', s.backingUp) : b.status === 'ok' ? st('ok', s.resOk) : st('crit', s.backupFailed)));
      V.backupNow = () => { if (!running) act(() => api('POST', '/admin/system/backups', {}), s.tBackupStarted); };
      V.backupBusy = running ? 'true' : 'false';
      V.backupBtn = running ? s.backingUp : s.backupNow;
      V.hookRows = items('system/webhooks').map((h) => {
        const okc = h.last_status >= 200 && h.last_status < 300;
        return Object.assign({
          url: h.url, ev: (h.events || []).join(', ') || '*',
          last: h.last_delivery_at ? F(s.lastDelivery, { code: h.last_status ? 'HTTP ' + h.last_status : h.last_error || 'error' }) : s.lastNever,
          lastFg: !h.last_delivery_at ? c.muted : okc ? c.ok : c.crit,
          test: () => act(() => api('POST', '/admin/system/webhooks/' + h.id + '/test', {}), s.tHookTest),
          remove: () => confirmBox(s.cfDelHook, F(s.cfDelHookBody, { name: h.name || h.url }), s.delete, () => act(() => api('DELETE', '/admin/system/webhooks/' + h.id), F(s.tDeleted, { name: h.name || h.url }))),
        }, sw(!!h.enabled, () => act(() => api('PATCH', '/admin/system/webhooks/' + h.id, { enabled: !h.enabled }), s.saved), h.url));
      });
      V.hookEmpty = V.hookRows.length === 0;
      V.addHook = hookForm;
      V.keyList = (admin ? items('system/api-keys') : []).filter((k) => !k.revoked_at).map((k) => ({
        name: k.name, key: (k.prefix || '') + '…', created: fmtDate(k.created_at), scope: (k.scopes || []).join(', '),
        copy: () => copy(k.prefix || ''),
        revoke: () => confirmBox(s.cfRevokeKey, F(s.cfRevokeKeyBody, { name: k.name }), s.revoke, () => act(() => api('DELETE', '/admin/system/api-keys/' + k.id), F(s.tKeyRevoked, { name: k.name }))),
      }));
      V.keyEmpty = V.keyList.length === 0;
      V.keyEmptyText = admin ? s.noKeys : s.keysAdminOnly;
      V.createKey = () => (admin ? tokenForm(true) : flash(s.keysAdminOnly));
      const mth = { GET: [c.ok, c.okSoft], POST: [c.accent, c.accentSoft], PATCH: [c.warn, c.warnSoft], PUT: [c.warn, c.warnSoft], DELETE: [c.crit, c.critSoft] };
      const eps = items('system/endpoints');
      V.endpoints = eps.filter((e) => has(e.path + ' ' + e.summary)).map((e) => ({ m: e.method, p: e.path, d: e.summary, fg: (mth[e.method] || mth.GET)[0], bg: (mth[e.method] || mth.GET)[1] }));
      V.epCount = eps.length + ' endpoints';
      V.baseUrl = location.origin + '/api/v1';
      V.openDocs = () => window.open('/api/docs', '_blank', 'noopener');
      if (upd.available) {
        V.updTitle = F(s.updTitle, { v: upd.latest });
        V.updSub = upd.notes || upd.channel || '';
      } else {
        V.updTitle = s.upToDateTitle;
        V.updSub = upd.error ? (/manifest_url/.test(upd.error) ? s.updNotConfigured : upd.error) : 'Meshport Server ' + (upd.current || status.version || '');
      }
      V.updBtn = ST.busy.install ? s.installing : upd.available ? s.installNow : s.upToDate;
      V.updDisabled = upd.available && admin && !ST.busy.install ? 'false' : 'true';
      V.installUpdate = () => {
        if (!upd.available || !admin || ST.busy.install) return;
        confirmBox(F(s.cfInstall, { v: upd.latest }), s.cfInstallBody, s.install, async () => {
          set2({ busy: Object.assign({}, ST.busy, { install: true }) });
          await act(() => api('POST', '/admin/system/update/install', {}), s.tInstalling);
        });
      };
      const sv = D.settings;
      V.chanSeg = seg([['stable', s.stable], ['beta', s.beta]], sv.update_channel || 'stable', (v) => quickSetting({ update_channel: v }));
      V.autoSw = sw(!!sv.auto_update, () => quickSetting({ auto_update: !sv.auto_update }), s.autoUpdate);
      V.retSeg = seg([7, 14, 30, 90].map((d) => [String(d), F(s.days, { n: d })]), String(sv.log_retention_days || 14), (v) => quickSetting({ log_retention_days: Number(v) }));
      V.regSw = sw(!!sv.open_registration, () => quickSetting({ open_registration: !sv.open_registration }), s.openReg);
      const accAll = T.accents();
      const accNames = { jade: s.cJade, terracotta: s.cTerra, rose: s.cRose, olive: s.cOlive, graphite: s.cGraphite };
      const ak = accentKey();
      V.swatches = Object.keys(accAll).map((k) => ({
        label: accNames[k], color: accAll[k].bg, pressed: k === ak ? 'true' : 'false', ring: k === ak ? accAll[k].bg : 'transparent', check: k === ak ? I.check : '',
        pick: () => quickSetting({ accent: k }),
      }));
      V.accentName = accNames[ak];
      V.openSettings = () => (admin ? settingsForm() : flash(s.adminOnly));
    }
    V.checkUpdate = async () => {
      try {
        D.update = await GET('system/update');
        render();
        flash(D.update.available ? F(s.updTitle, { v: D.update.latest }) : D.update.error ? (/manifest_url/.test(D.update.error) ? s.updNotConfigured : D.update.error) : s.upToDateTitle);
      } catch (e) { flash(e.message); }
    };
    return V;
  }

  // ---------------------------------------------------------------- mapping modal
  function openMapForm(m) {
    const s = S();
    const nodes = items('frp/nodes');
    if (!nodes.length) { flash(s.noNodes); return; }
    if (m) {
      const web = m.proto === 'http' || m.proto === 'https';
      set({ overlay: 'map', mf: { id: m.id, name: m.name, device: m.device_id, node: m.node_id, proto: m.proto, lip: m.local_ip, lport: String(m.local_port),
        remote: web ? (m.domains || [])[0] || m.subdomain || '' : m.proto === 'stcp' || m.proto === 'xtcp' ? '' : String(m.remote_port || ''), err: '' } });
      return;
    }
    const devs = items('devices').filter((d) => d.status === 'active' && (d.level === 'member' || d.level === 'admin'));
    if (!devs.length) { flash(s.lvMemberDesc); return; }
    set({ overlay: 'map', mf: { id: null, name: '', device: devs[0].id, node: nodes[0].id, proto: 'tcp', lip: '127.0.0.1', lport: '', remote: '', err: '' } });
  }
  async function saveMap() {
    const s = S();
    const f = ST.mf;
    if (!f || f.busy) return;
    if (!String(f.name).trim() || !String(f.lport).trim()) { set({ mf: Object.assign({}, f, { err: s.tNeedFields }) }); return; }
    const web = f.proto === 'http' || f.proto === 'https';
    const secret = f.proto === 'stcp' || f.proto === 'xtcp';
    const body = { name: f.name.trim(), node: f.node, proto: f.proto, local_ip: f.lip.trim() || '127.0.0.1', local_port: num(f.lport, 0) };
    if (web) body.domains = f.remote.trim() ? [f.remote.trim()] : [];
    else if (secret) { if (f.remote.trim()) body.secret_key = f.remote.trim(); } else body.remote_port = num(f.remote, 0);
    set({ mf: Object.assign({}, f, { busy: true, err: '' }) });
    try {
      if (f.id) await api('PATCH', '/admin/frp/mappings/' + f.id, body);
      else await api('POST', '/admin/frp/mappings', Object.assign({ device: f.device }, body));
      set({ overlay: null, mf: null });
      flash(F(f.id ? s.tMapSaved : s.tMapCreated, { name: body.name }));
      reloadSoon(100);
    } catch (e) {
      set({ mf: Object.assign({}, ST.mf, { busy: false, err: e.message }) });
    }
  }

  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    if (ST.cf) set({ cf: null });
    else if (ST.overlay) closeOverlay();
  });
  boot();
})();
