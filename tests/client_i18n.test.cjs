const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const root = path.join(__dirname,'../internal/agent/web');
function i18n(saved, browser = 'zh-CN') {
  const scope = {window:{},navigator:{language:browser},localStorage:{getItem:()=>saved}};
  vm.createContext(scope);
  for (const file of ['zh-CN.js','en.js','i18n.js']) vm.runInContext(fs.readFileSync(path.join(root,file),'utf8'),scope);
  return scope;
}
test('English is the default even on non-English browsers', () => {
  const s = i18n(null);
  assert.equal(s.mpLanguage(), 'en');
  assert.equal(s.mpText('裝置名稱'), 'Device name');
});
test('existing Chinese language preferences remain available', () => {
  for (const language of ['zh-CN','zh-TW']) {
    const s = i18n(language);
    assert.equal(s.mpText('裝置名稱'), language === 'zh-CN' ? '设备名称' : '裝置名稱');
  }
});
test('translation only covers interface strings, not arbitrary user data', () => {
  for (const language of ['en','zh-CN','zh-TW']) {
    const s = i18n(language);
    assert.equal(s.mpText('用戶自己的裝置 🐱 mpe_private'), '用戶自己的裝置 🐱 mpe_private');
  }
});
test('all Chinese UI literals have English translations', () => {
  const s = i18n('en');
  for (const key of Object.keys(s.window.MP_CLIENT_ZH_CN)) {
    assert.ok(s.window.MP_CLIENT_EN[key], 'missing English translation for: ' + key);
    assert.equal(/[\u3400-\u9fff]/u.test(s.window.MP_CLIENT_EN[key]), false, 'untranslated: ' + key);
  }
});
