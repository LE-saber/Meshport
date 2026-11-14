const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const root = path.join(__dirname, '..', 'web', 'console');

function app(fetch) {
  class DCLogic {
    constructor() { this.state = {}; this.props = {}; }
    setState(patch) { Object.assign(this.state, patch); }
  }
  const scope = { DCLogic, fetch, AbortSignal, setTimeout, clearTimeout, Date,
    location: { origin: 'http://127.0.0.1:18080', protocol: 'http:', host: '127.0.0.1:18080' },
    matchMedia: () => ({ matches: false }) };
  vm.createContext(scope);
  vm.runInContext(fs.readFileSync(path.join(root, 'design.js'), 'utf8') + '\n' +
    fs.readFileSync(path.join(root, 'app.js'), 'utf8') + '\nthis.ConsoleComponent = Component;', scope);
  const instance = new scope.ConsoleComponent();
  instance.state = { me: { role: 'admin', user: { username: 'test' } }, data: {} };
  Object.assign(instance, { _epoch: 0, _alive: true, _data: {} });
  return instance;
}
const reply = (value, status = 200) => ({ ok: status < 400, status, text: async () => JSON.stringify(value) });

test('requests use same-origin cookie authentication and CSRF header', async () => {
  const a = app(async (url, options) => {
    assert.equal(url, '/api/v1/admin/groups');
    assert.equal(options.credentials, 'same-origin');
    assert.equal(options.headers['X-Requested-With'], 'MeshportConsole');
    assert.equal(options.headers.Authorization, undefined);
    assert.deepEqual(JSON.parse(options.body), { name: 'group' });
    return reply({ id: 'grp_test' });
  });
  assert.equal((await a.request('/admin/groups', 'POST', { name: 'group' })).id, 'grp_test');
});

test('a rejected write keeps the form and existing data, and shows the server error', async () => {
  const a = app(async () => reply({ error: { code: 'bad_request', message: 'Port already in use' } }, 400));
  a.state.dialog = { title: 'Mapping' };
  a.state.data.mappings = { items: [{ id: 'existing' }] };
  a.refresh = async () => assert.fail('A rejected write must not report success by refreshing');
  await a.change('/admin/frp/mappings', 'POST', { name: 'new' });
  assert.equal(a.state.dialog.title, 'Mapping');
  assert.equal(a.state.data.mappings.items.length, 1);
  assert.equal(a.state.dialogError, 'Port already in use');
  assert.equal(a.state.busy, false);
});

test('expired sessions remove private view state', async () => {
  const a = app(async () => reply({ error: { code: 'unauthorized', message: 'Expired' } }, 401));
  a.state.result = { text: 'one-time-secret' };
  await assert.rejects(a.request('/admin/overview'), /Expired/);
  assert.equal(a.state.me, null);
  assert.equal(a.state.result, null);
  assert.equal(Object.keys(a.state.data).length, 0);
});

test('settings response is unwrapped and rendered from actual server values', async () => {
  const settings = { console_name: 'Actual server', backup_hour: 5, backup_keep: 9 };
  const a = app(async url => reply(url.endsWith('/system/settings') ? { settings, defaults: {} } :
    url.endsWith('/system/status') ? { version: 'test', services: [], uptime_s: 60 } : { items: [] }));
  await a.refresh('system');
  const view = a.renderVals();
  assert.equal(view.serverName, 'Actual server');
  assert.equal(view.backupSchedule, '每日 UTC 5:00 · 保留 9 份');
  assert.equal(a.state.data.settings.backup_keep, 9);
});

test('viewer controls are read-only and empty charts contain no simulated samples', () => {
  const a = app();
  a.state.me.role = 'viewer';
  const view = a.renderVals();
  assert.equal(view.writeDisabled, true);
  assert.equal(view.adminDisabled, true);
  assert.equal(view.bw.lines[0].d, '');
  assert.equal(view.bw.legend[0].value, '—');
});

test('exported runtime preserves names made from multiple interpolations', () => {
  const runtime = fs.readFileSync(path.join(root, 'vendor', 'support.js'), 'utf8');
  const begin = runtime.indexOf('function O(t)');
  const end = runtime.indexOf('function te(', begin);
  assert.ok(begin >= 0 && end > begin);
  const resolve = (scope, key) => key.trim().split('.').reduce((v, part) => v?.[part], scope);
  const interpolate = new Function('P', runtime.slice(begin, end) + ';return O;')(resolve);
  const scope = { s: { edit: 'Edit' }, m: { name: 'mapping' } };
  assert.equal(interpolate('{{s.edit}} {{m.name}}')(scope), 'Edit mapping');
  assert.equal(interpolate('{{m.name}}')(scope), 'mapping');
});
