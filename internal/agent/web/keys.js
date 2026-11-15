/* Key details are fetched only after local password verification, never kept in S. */
'use strict';
let activeKeyView = null;
function closeKeyView() { if (activeKeyView) activeKeyView(); }
function openKeyView() {
  closeKeyView();
  let setup = !(S.state && S.state.key_password_set), timer = null;
  const password = h('input', { class:'input', type:'password', autocomplete: setup ? 'new-password' : 'current-password', 'aria-label':mpText('密鑰密碼') });
  const confirmation = h('input', { class:'input', type:'password', autocomplete:'new-password', 'aria-label':mpText('確認密鑰密碼') });
  const error = h('div', { class:'err small', role:'alert' });
  const dialog = h('dialog', { class:'key-dialog', 'aria-label':mpText('查看自己的密鑰') });
  const close = () => {
    clearTimeout(timer); password.value=''; confirmation.value=''; dialog.replaceChildren();
    dialog.close(); dialog.remove(); if (activeKeyView === close) activeKeyView=null;
  };
  activeKeyView=close;
  dialog.addEventListener('cancel', e => { e.preventDefault(); close(); });
  const submit = h('button', { class:'btn primary', type:'submit' }, setup ? mpText('設定並查看') : mpText('解鎖查看'));
  const form = h('form', { class:'card key-form', onsubmit:async e => {
    e.preventDefault(); error.textContent=''; submit.disabled=true;
    try {
      const value=password.value;
      if (setup) {
        await api('POST','/local/keys/password',{password:value,confirmation:confirmation.value});
        S.state.key_password_set=true; setup=false;
      }
      if (!dialog.isConnected) return;
      const keys=await api('POST','/local/keys/view',{password:value});
      password.value=''; confirmation.value='';
      if (!dialog.isConnected) return;
      dialog.replaceChildren(h('div',{class:'card key-form'},
        h('h2',null,mpText('自己的密鑰資訊')),
        h('p',{class:'small muted'},mpText('關閉、切換到背景或 60 秒後自動隱藏。')),
        h('dl',{class:'kv key-values'}, ...[
          [mpText('身分公鑰'),keys.identity_public_key], [mpText('身分金鑰指紋'),keys.identity_fingerprint],
          [mpText('E2E 公鑰'),keys.e2e_public_key], [mpText('伺服器公鑰指紋'),keys.server_fingerprint || mpText('未綁定')]
        ].flatMap(([label,value])=>[h('dt',null,label),h('dd',{class:'mono'},value)])),
        h('button',{class:'btn',type:'button',onclick:close},mpText('隱藏並關閉'))));
      timer=setTimeout(close,60000);
    } catch (ex) {
      password.value=''; confirmation.value='';
      if (dialog.isConnected) error.textContent=mpText(ex.message);
    } finally { submit.disabled=false; }
  }},
    h('h2',null,setup ? mpText('首次查看：設定密鑰密碼') : mpText('查看自己的密鑰')),
    h('p',{class:'small muted'},setup ? mpText('請設定自己的查看密碼，至少 8 個字元。之後每次查看都需要輸入。') : mpText('請輸入自己的密鑰密碼。')),
    h('label',{class:'field'},h('span',null,mpText('密鑰密碼')),password),
    setup ? h('label',{class:'field'},h('span',null,mpText('確認密鑰密碼')),confirmation) : null,
    error, h('div',{class:'row'},h('button',{class:'btn',type:'button',onclick:close},mpText('取消')),submit));
  dialog.append(form); document.body.append(dialog); dialog.showModal(); password.focus();
}
document.addEventListener('visibilitychange',()=>{ if (document.hidden) closeKeyView(); });
window.addEventListener('blur',closeKeyView);
