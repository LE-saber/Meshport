const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const root = path.join(__dirname,'../internal/agent/web');
test('client supports both Chinese locales without translating arbitrary user data', () => {
  for (const language of ['zh-CN','zh-TW']) {
    const scope = {window:{},navigator:{language},localStorage:{getItem:()=>null}};
    vm.createContext(scope);
    for (const file of ['zh-CN.js','i18n.js']) vm.runInContext(fs.readFileSync(path.join(root,file),'utf8'),scope);
    assert.equal(scope.mpText('裝置名稱'),language === 'zh-CN' ? '设备名称' : '裝置名稱');
    assert.equal(scope.mpText('用戶自己的裝置 🐱 mpe_private'), '用戶自己的裝置 🐱 mpe_private');
  }
});
