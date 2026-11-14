/* Meshport console – tiny template runtime.
 *
 * Renders admin.tpl.html (the approved console design) from a plain values
 * object. Supported template syntax:
 *   {{a.b.c}}               text / attribute interpolation (dotted paths only)
 *   <sc-for list="{{x}}" as="it">   repeat children for each item
 *   <sc-if value="{{x}}">   render children when x is truthy
 *   data-s="..."            inline style (applied through CSSOM, CSP-safe)
 *   data-on-<event>="{{f}}" event handler (click, change, submit, mouseenter…)
 *   <select data-options="{{x}}">  options from [{v, label}]
 * Re-rendering diffs against the previous virtual tree, so focus, scroll and
 * input state survive the 2-second live refresh.
 */
(function () {
  'use strict';
  const RX = /\{\{\s*([^}]+?)\s*\}\}/g;

  function lookup(scope, path) {
    if (path === 'true') return true;
    if (path === 'false') return false;
    const parts = path.split('.');
    let v = scope[parts[0]];
    for (let i = 1; i < parts.length && v != null; i++) v = v[parts[i]];
    return v;
  }
  function single(str) {
    const m = /^\{\{\s*([^}]+?)\s*\}\}$/.exec(str.trim());
    return m ? m[1] : null;
  }
  function interp(str) {
    if (str.indexOf('{{') < 0) return () => str;
    const parts = [];
    let last = 0;
    str.replace(RX, (m, p, off) => {
      if (off > last) parts.push(str.slice(last, off));
      parts.push({ p });
      last = off + m.length;
      return m;
    });
    if (last < str.length) parts.push(str.slice(last));
    return (sc) => {
      let out = '';
      for (const x of parts) {
        if (typeof x === 'string') out += x;
        else {
          const v = lookup(sc, x.p);
          if (v != null && v !== false) out += v;
        }
      }
      return out;
    };
  }

  // ---- compile template DOM into render functions returning vnode arrays
  function compileChildren(node) {
    const fns = [];
    node.childNodes.forEach((ch) => {
      const f = compileNode(ch);
      if (f) fns.push(f);
    });
    return (sc, out) => { for (const f of fns) f(sc, out); };
  }
  function compileNode(n) {
    if (n.nodeType === 3) {
      const txt = n.nodeValue;
      if (!txt.trim() && txt.indexOf('\n') >= 0) return null;
      const f = interp(txt.replace(/\s*\n\s*/g, ' '));
      return (sc, out) => out.push({ text: f(sc) });
    }
    if (n.nodeType !== 1) return null;
    const tag = n.localName;
    if (tag === 'sc-for') {
      const list = single(n.getAttribute('list') || '');
      const as = n.getAttribute('as') || 'it';
      const kids = compileChildren(n);
      return (sc, out) => {
        const arr = lookup(sc, list);
        if (!arr || !arr.length) return;
        for (let i = 0; i < arr.length; i++) {
          const s2 = Object.create(sc);
          s2[as] = arr[i];
          kids(s2, out);
        }
      };
    }
    if (tag === 'sc-if') {
      const p = single(n.getAttribute('value') || '');
      const kids = compileChildren(n);
      return (sc, out) => { if (lookup(sc, p)) kids(sc, out); };
    }
    const ns = n.namespaceURI === 'http://www.w3.org/2000/svg' ? n.namespaceURI : null;
    const attrs = [];
    const on = [];
    let style = null;
    let opts = null;
    for (const a of Array.from(n.attributes)) {
      if (a.name === 'data-s') style = interp(a.value);
      else if (a.name === 'data-options') opts = single(a.value) || a.value.trim();
      else if (a.name.indexOf('data-on-') === 0) on.push([a.name.slice(8), single(a.value)]);
      else attrs.push([a.name, interp(a.value)]);
    }
    const kids = compileChildren(tag === 'template' ? n.content : n);
    return (sc, out) => {
      const v = { tag, ns, attrs: {}, style: style ? style(sc) : '', on: {}, kids: [] };
      for (const [k, f] of attrs) v.attrs[k] = f(sc);
      for (const [ev, p] of on) {
        const fn = lookup(sc, p);
        if (typeof fn === 'function') v.on[ev] = fn;
      }
      if (opts) {
        const list = lookup(sc, opts) || [];
        for (const o of list) v.kids.push({ tag: 'option', ns: null, attrs: { value: String(o.v) }, style: '', on: {}, kids: [{ text: String(o.label) }] });
      } else kids(sc, v.kids);
      out.push(v);
    };
  }

  // ---- DOM patching
  const PROPS = { value: 1, checked: 1 };
  function create(v) {
    if (v.text != null) { v.el = document.createTextNode(v.text); return v.el; }
    const el = v.ns ? document.createElementNS(v.ns, v.tag) : document.createElement(v.tag);
    v.el = el;
    el._on = {};
    update(el, null, v);
    for (const k of v.kids) el.appendChild(create(k));
    syncValue(el, v);
    return el;
  }
  function listener(e) {
    const fn = this._on && this._on[e.type === 'input' ? 'change' : e.type];
    if (e.type === 'submit') e.preventDefault();
    if (fn) fn(e);
  }
  function evName(tag, ev) {
    if (ev === 'change' && (tag === 'input' || tag === 'textarea')) return 'input';
    return ev;
  }
  function update(el, o, v) {
    const oa = o ? o.attrs : {};
    for (const k in v.attrs) {
      if (PROPS[k] && (v.tag === 'input' || v.tag === 'select' || v.tag === 'textarea')) continue;
      if (oa[k] !== v.attrs[k]) el.setAttribute(k, v.attrs[k]);
    }
    for (const k in oa) if (!(k in v.attrs)) el.removeAttribute(k);
    if (!o || o.style !== v.style) el.style.cssText = v.style;
    for (const ev in v.on) {
      if (!el._on[ev]) el.addEventListener(evName(v.tag, ev), listener);
      el._on[ev] = v.on[ev];
    }
    for (const ev in el._on) if (!v.on[ev]) el._on[ev] = null;
  }
  function syncValue(el, v) {
    if ('value' in v.attrs && (v.tag === 'input' || v.tag === 'select' || v.tag === 'textarea')) {
      if (el.value !== v.attrs.value) el.value = v.attrs.value;
    }
  }
  function same(a, b) {
    if ((a.text != null) !== (b.text != null)) return false;
    if (a.text != null) return true;
    return a.tag === b.tag && a.ns === b.ns && (a.tag !== 'input' || a.attrs.type === b.attrs.type);
  }
  function patchKids(parent, olds, news) {
    const n = news.length;
    for (let i = 0; i < n; i++) {
      const o = olds[i];
      const v = news[i];
      if (!o) { parent.appendChild(create(v)); continue; }
      if (!same(o, v)) { const el = create(v); parent.replaceChild(el, o.el); continue; }
      v.el = o.el;
      if (v.text != null) { if (o.text !== v.text) o.el.nodeValue = v.text; continue; }
      update(o.el, o, v);
      patchKids(o.el, o.kids, v.kids);
      syncValue(o.el, v);
    }
    for (let i = n; i < olds.length; i++) if (olds[i].el.parentNode === parent) parent.removeChild(olds[i].el);
  }

  // ---- public API
  const DC = {
    templates: {},
    async load(url) {
      const res = await fetch(url, { credentials: 'same-origin', cache: 'no-cache' });
      if (!res.ok) throw new Error('template ' + res.status);
      const doc = new DOMParser().parseFromString(await res.text(), 'text/html');
      doc.querySelectorAll('template[id]').forEach((tpl) => { DC.templates[tpl.id] = compileChildren(tpl.content); });
    },
    // mount returns a renderer: call render(templateName, values) on every change.
    mount(root) {
      let tree = [];
      let cur = null;
      return {
        render(name, vals) {
          const tpl = DC.templates[name];
          if (!tpl) return;
          if (cur !== name) { root.textContent = ''; tree = []; cur = name; }
          const out = [];
          tpl(vals, out);
          patchKids(root, tree, out);
          tree = out;
        },
      };
    },
  };
  window.DC = DC;
})();
