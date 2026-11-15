/* Local UI strings only: never translate device names, messages or key material. */
'use strict';
function mpLanguage() {
  // Explicit preferences are kept across upgrades; a fresh client starts in English.
  try { const saved = localStorage.getItem('meshport.language'); if (['en', 'zh-CN', 'zh-TW'].includes(saved)) return saved; } catch (_) { }
  return 'en';
}
function mpText(text) {
  const lang = mpLanguage();
  if (lang === 'en') return (window.MP_CLIENT_EN || {})[text] || text;
  return lang === 'zh-CN' ? (window.MP_CLIENT_ZH_CN[text] || text) : text;
}
function mpApplyLanguage() {
  document.documentElement.lang = { en: 'en', 'zh-CN': 'zh-Hans-CN', 'zh-TW': 'zh-Hant-TW' }[mpLanguage()];
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
  select.setAttribute('aria-label', 'Language');
  for (const [value, label] of [['en', 'English'], ['zh-TW', '繁體中文'], ['zh-CN', '简体中文']]) {
    const opt = document.createElement('option'); opt.value = value; opt.textContent = label; opt.selected = value === mpLanguage(); select.append(opt);
  }
  select.onchange = () => mpSetLanguage(select.value);
  return select;
}
