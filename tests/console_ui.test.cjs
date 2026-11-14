const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

// Run the actual console functions without booting a browser or a server.
function consoleApp(data) {
  const requests = [];
  const scope = {
    window: { addEventListener() {} }, document: { addEventListener() {} },
    URLSearchParams, setTimeout, clearTimeout,
    fetch: async (url, options) => {
      requests.push({ url, options });
      const value = await data(url, options);
      return { ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => value, text: async () => JSON.stringify(value) };
    },
  };
  vm.createContext(scope);
  const source = fs.readFileSync(path.join(__dirname, '../internal/console/web/app.js'), 'utf8');
  vm.runInContext(source.slice(0, source.lastIndexOf("if (location.pathname === '/enroll')")), scope);
  vm.runInContext(`
    h = (tag, attrs, ...children) => ({ tag, attrs: attrs || {}, children: children.flat(Infinity).filter(x => x != null && x !== false),
      append(...xs) { this.children.push(...xs); }, replaceChildren(...xs) { this.children = xs; } });
    region = (el, loader) => { this.loaded = loader().then(nodes => el.append(...[].concat(nodes))); return () => {}; };
    icon = () => h('svg');
    toast = (message, bad) => { this.notice = { message, bad }; };
    S.role = 'admin';
  `, scope);
  return { scope, requests, run: code => vm.runInContext(code, scope) };
}

test('direct grant handles empty/inactive devices and a failed load without opening a form', async () => {
  for (const items of [[], [{ id: 'pending', status: 'pending' }]]) {
    const c = consoleApp(() => ({ items }));
    c.run('formDialog = () => { throw Error("unexpected form"); };');
    await c.run('directGrantDialog()');
    assert.match(c.scope.notice.message, /沒有可授權/);
    assert.equal(c.scope.notice.bad, true);
  }
  const c = consoleApp(() => { throw Error('offline'); });
  await c.run('directGrantDialog()');
  assert.equal(c.scope.notice.bad, true);
});

test('direct grant submits the selected device, level and duration with cookie CSRF protection', async () => {
  const c = consoleApp(() => ({ items: [{ id: 'dev_target', name: 'Target', status: 'active', level: 'guest' }] }));
  c.run('formDialog = (title, fields, submit) => { this.form = { fields, submit }; };');
  await c.run('directGrantDialog()');
  assert.equal(c.scope.form.fields[0].options[0][0], 'dev_target');
  await c.scope.form.submit({ target: 'dev_target', level: 'member', duration: '7d', reason: 'publish service' });
  const req = c.requests.at(-1);
  assert.equal(req.url, '/api/v1/admin/grants');
  assert.equal(req.options.credentials, 'same-origin');
  assert.ok(req.options.headers['X-Requested-With']);
  assert.deepEqual(JSON.parse(req.options.body), { target: 'dev_target', level: 'member', duration: '7d', reason: 'publish service' });
});

function walk(node) {
  if (!node || typeof node !== 'object') return [];
  return [node, ...(node.children || []).flatMap(walk)];
}

test('communication filters use telemetry path/state values', async () => {
  const c = consoleApp(() => ({ items: [] }));
  const el = c.run("h('main')");
  c.scope.el = el;
  c.run('P.comms(el)');
  await c.scope.loaded;
  const selects = walk(el).filter(n => n.tag === 'select');
  const paths = selects[1].children.map(n => n.attrs.value);
  const states = selects[2].children.map(n => n.attrs.value);
  assert.equal(paths.includes('turn'), false);
  assert.ok(paths.includes('relay'));
  assert.ok(states.includes('up'));
  assert.equal(states.includes('p2p') || states.includes('relay'), false);
  assert.equal(c.run('lab("sstate", "up")'), '已連線');
});

test('database check button renders both successful and failed integrity results', async () => {
  for (const integrity of ['ok', 'database page damaged']) {
    const c = consoleApp(url => url.includes('/system/db') ? { engine: 'SQLite', pages: 1, page_size: 4096, rows: {}, ...(url.includes('check=1') ? { integrity } : {}) } : { items: [], services: [] });
    const el = c.run("h('main')"); c.scope.el = el;
    c.run('sysStatus(el)'); await c.scope.loaded;
    const button = walk(el).find(n => n.tag === 'button' && n.children.includes('完整性檢查'));
    await button.attrs.onclick();
    const result = walk(el).find(n => n.attrs.class === 'pill' + (integrity === 'ok' ? ' ok' : ' bad'));
    assert.ok(result, 'integrity result should be visible');
    assert.ok(result.children.includes(integrity));
  }
});
