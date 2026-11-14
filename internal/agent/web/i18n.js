/* Local UI strings only: never translate device names, messages or key material. */
'use strict';
function mpLanguage() {
  try { const saved = localStorage.getItem('meshport.language'); if (saved === 'zh-CN' || saved === 'zh-TW') return saved; } catch (_) { }
  return /^zh-(CN|SG|Hans)/i.test(navigator.language || '') ? 'zh-CN' : 'zh-TW';
}
function mpText(text) {
  return mpLanguage() === 'zh-CN' ? (window.MP_CLIENT_ZH_CN[text] || text) : text;
}
function mpApplyLanguage() {
  document.documentElement.lang = mpLanguage() === 'zh-CN' ? 'zh-Hans-CN' : 'zh-Hant-TW';
  for (const el of document.querySelectorAll('[data-i18n]')) el.textContent = mpText(el.dataset.i18n);
}
async function mpSetLanguage(value) {
  try {
    await api('PATCH', '/local/settings', { language: value });
    localStorage.setItem('meshport.language', value);
    location.reload();
  } catch (e) { toast(e.message, true); }
}
function mpLanguagePicker() {
  const select = document.createElement('select'); select.className = 'input'; select.style.width = 'auto';
  select.setAttribute('aria-label', 'Language / 语言');
  for (const [value, label] of [['zh-TW', '繁體中文'], ['zh-CN', '简体中文']]) {
    const opt = document.createElement('option'); opt.value = value; opt.textContent = label; opt.selected = value === mpLanguage(); select.append(opt);
  }
  select.onchange = () => mpSetLanguage(select.value);
  return select;
}
