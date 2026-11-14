const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const root = path.join(__dirname, '../internal/console/web');

function app(respond = () => ({})) {
  const requests = [];
  const scope = {
    window: { addEventListener() {} }, document: { addEventListener() {} },
    requestAnimationFrame: () => 1, setTimeout: () => 1, clearTimeout() {},
    location: { origin: 'http://127.0.0.1:18080', hash: '', pathname: '/' },
    fetch: async (url, options) => {
      requests.push({ url, options });
      const value = await respond(url, options);
      return { ok: true, status: 200, text: async () => JSON.stringify(value) };
    },
  };
  vm.createContext(scope);
  for (const file of ['theme.js', 'strings.js']) vm.runInContext(fs.readFileSync(path.join(root, file), 'utf8'), scope);
  const source = fs.readFileSync(path.join(root, 'app.js'), 'utf8');
  const end = source.lastIndexOf('  boot();');
  assert.ok(end > 0);
  vm.runInContext(source.slice(0, end) + `
    window.testApp = { ST, D, grantForm, formVals, adminVals, api, T };
  })();`, scope);
  const a = scope.window.testApp;
  a.ST.lang = 'zh'; a.ST.theme = 'light';
  a.D.me = { role: 'admin', user: { username: 'admin' } };
  return { a, requests, scope };
}

test('grant form handles empty/inactive devices and provides permanent authorization', () => {
  for (const items of [[], [{ id: 'pending', status: 'pending' }]]) {
    const { a } = app(); a.D.devices = { items };
    a.grantForm();
    assert.equal(a.ST.overlay, null);
    assert.ok(a.ST.toast);
  }
  const { a } = app();
  a.D.devices = { items: [{ id: 'target', name: 'Target', status: 'active', level: 'guest' }] };
  a.grantForm();
  assert.equal(a.ST.overlay, 'form');
  const durations = a.ST.fm.cfg.fields.find(f => f.k === 'duration').opts.map(o => o.v);
  assert.ok(durations.includes('perm'));
});

test('grant form submits target and duration with same-origin cookies and CSRF protection', async () => {
  const { a, requests } = app();
  a.D.devices = { items: [{ id: 'target', name: 'Target', status: 'active' }] };
  a.grantForm();
  await a.ST.fm.cfg.onSave({ target: 'target', level: 'member', duration: 'perm', reason: 'test' });
  const r = requests.at(-1);
  assert.equal(r.url, '/api/v1/admin/grants');
  assert.equal(r.options.credentials, 'same-origin');
  assert.ok(r.options.headers['X-Requested-With']);
  assert.deepEqual(JSON.parse(r.options.body), { target: 'target', level: 'member', duration: 'perm', reason: 'test' });
});

test('a rejected form preserves values and displays the server error', async () => {
  const { a } = app();
  a.D.devices = { items: [{ id: 'target', name: 'Target', status: 'active' }] };
  a.grantForm();
  a.ST.fm.cfg.onSave = async () => { throw Error('Rejected by server'); };
  await a.formVals(a.T.pal(false, 'jade'), () => [], () => ({})).save();
  assert.equal(a.ST.overlay, 'form');
  assert.equal(a.ST.fm.err, 'Rejected by server');
  assert.equal(a.ST.fm.v.target, 'target');
  assert.equal(a.ST.fm.busy, false);
});

test('communication view renders actual telemetry states and relay path', () => {
  const { a } = app(); a.ST.page = 'comms';
  a.D.sessions = { items: [{ id: 's1', a: 'A', b: 'B', state: 'up', path: 'relay', mode: 'auto', via: 'TURN relay', rate_bps: 100 }] };
  const view = a.adminVals(a.T.pal(false, 'jade'));
  assert.equal(view.commRows.length, 1);
  assert.equal(view.commRows[0].via, 'TURN relay');
  assert.ok(view.commRows[0].stLabel);
  assert.notEqual(view.commRows[0].stLabel, 'up');
});
