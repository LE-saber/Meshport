#!/usr/bin/env python3
"""Build internal/console/web/admin.tpl.html from the approved demo (Admin.dc.html).

Usage: python3 build_tpl.py Admin.dc.html ../../internal/console/web/admin.tpl.html auth.tpl.html

The demo markup is kept verbatim; only hard-coded demo values are swapped for
bindings, a few controls the real API needs are added in the same style, and
inline style/on* attributes are renamed to data-s / data-on-* so the strict
CSP of the console never sees inline styles or handlers.
"""
import re
import sys

src, dst = sys.argv[1], sys.argv[2]
lines = open(src, encoding='utf-8').read().split('\n')
start = next(i for i, l in enumerate(lines) if l.startswith('<div lang="{{langTag}}"'))
end = next(i for i, l in enumerate(lines) if l.startswith('</x-dc>'))
t = '\n'.join(lines[start:end])


def rep(old, new, count=1):
    global t
    n = t.count(old)
    if n != count:
        raise SystemExit('expected %d x %r, found %d' % (count, old[:90], n))
    t = t.replace(old, new)


def svg(icon, size=16):
    return ('<svg width="%d" height="%d" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" '
            'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="%s"></path></svg>') % (size, size, icon)


BTN = ('display: inline-flex; align-items: center; justify-content: center; gap: 6px; height: 34px; padding: 0 12px; '
       'border-radius: 10px; border: 1px solid {{c.field}}; background: {{c.input}}; color: {{c.text}}; font: inherit; '
       'font-size: 13px; font-weight: 600; cursor: pointer; white-space: nowrap')
PRIMARY = ('display: inline-flex; align-items: center; gap: 8px; height: 40px; padding: 0 16px; border: 0; border-radius: 12px; '
           'background: {{c.accentBg}}; color: #FFFFFF; font: inherit; font-size: 14px; font-weight: 600; cursor: pointer; '
           'box-shadow: 0 6px 16px {{c.glow}}; white-space: nowrap')
LINK = ('display: inline-flex; align-items: center; gap: 4px; height: 32px; padding: 0 6px; border: 0; border-radius: 8px; '
        'background: transparent; color: {{c.accent}}; font: inherit; font-size: 13px; font-weight: 600; cursor: pointer')
LABEL = 'display: flex; flex-direction: column; gap: 6px; font-size: 12.5px; font-weight: 600; color: {{c.muted}}'
INPUT = ('width: 100%; height: 42px; box-sizing: border-box; padding: 0 12px; border-radius: 12px; border: 1px solid {{c.field}}; '
         'background: {{c.input}}; color: {{c.text}}; font: inherit; font-size: 14px; font-weight: 400; outline: none')
SEGWRAP = 'display: inline-flex; flex-wrap: wrap; gap: 3px; padding: 3px; border-radius: 12px; background: {{c.segBg}}'
SEGBTN = ('height: 32px; padding: 0 12px; border: 0; border-radius: 9px; background: {{o.bg}}; color: {{o.fg}}; box-shadow: {{o.sh}}; '
          'font: inherit; font-size: 13px; font-weight: 600; cursor: pointer; white-space: nowrap; transition: background 0.2s')
MONO = 'ui-monospace, Menlo, Consolas, monospace'
GLASS = ('border-radius: 20px; background: {{c.glass}}; border: 1px solid {{c.stroke}}; box-shadow: {{c.shadow}}; '
         'backdrop-filter: blur(24px) saturate(170%)')


def switch(v):
    return ('<button type="button" role="switch" aria-checked="{{%s.checked}}" aria-label="{{%s.label}}" onClick="{{%s.toggle}}" '
            'style="display: flex; align-items: center; justify-content: center; width: 52px; height: 40px; flex: none; padding: 0; '
            'border: 0; background: transparent; cursor: pointer"><span style="position: relative; display: block; width: 42px; '
            'height: 24px; border-radius: 12px; background: {{%s.swBg}}; transition: background 0.2s"><span style="position: absolute; '
            'top: 3px; left: 3px; width: 18px; height: 18px; border-radius: 50%%; background: #FFFFFF; box-shadow: 0 1px 3px '
            'rgba(0, 0, 0, 0.25); transform: translateX({{%s.knobX}}px); transition: transform 0.2s"></span></span></button>') % (v, v, v, v, v)


def modal(cond, width, title, body, footer, z=30):
    return ('<sc-if value="{{%s}}">\n<div style="position: fixed; inset: 0; z-index: %d; display: flex; align-items: center; '
            'justify-content: center; padding: 24px; box-sizing: border-box">\n'
            '<button type="button" onClick="{{closeOverlay}}" aria-label="{{s.close}}" style="position: absolute; inset: 0; border: 0; '
            'padding: 0; background: {{c.scrim}}; cursor: pointer"></button>\n'
            '<div role="dialog" aria-modal="true" aria-label="%s" style="position: relative; width: %dpx; max-width: 100%%; '
            'max-height: 100%%; overflow-y: auto; box-sizing: border-box; padding: 22px 24px; border-radius: 24px; background: {{c.sheet}}; '
            'border: 1px solid {{c.stroke}}; backdrop-filter: blur(30px) saturate(180%%); box-shadow: 0 30px 80px rgba(0, 0, 0, 0.25)">\n'
            '<div style="display: flex; align-items: center; justify-content: space-between; gap: 12px">\n'
            '<h2 style="margin: 0; font-size: 19px; font-weight: 700">%s</h2>\n'
            '<button type="button" onClick="{{closeOverlay}}" aria-label="{{s.close}}" style="%s; width: 36px; padding: 0">%s</button>\n'
            '</div>\n%s\n%s\n</div>\n</div>\n</sc-if>\n') % (cond, z, title, width, title, BTN, svg('{{I.x}}'), body, footer)


# ---- full-height app instead of the 900px artboard
rep('min-height: 900px; overflow: hidden;', 'min-height: 100vh; overflow: hidden;')
rep('<div style="position: relative; display: flex; flex-wrap: wrap; min-height: 900px">',
    '<div style="position: relative; display: flex; flex-wrap: wrap; min-height: 100vh">')

# ---- sidebar: real server card
rep('<div style="font-size: 18px; font-weight: 700; letter-spacing: -0.01em; line-height: 1.15">Meshport</div>',
    '<div style="font-size: 18px; font-weight: 700; letter-spacing: -0.01em; line-height: 1.15">{{brand}}</div>')
rep('<span style="width: 8px; height: 8px; border-radius: 50%; background: {{c.okDot}}; box-shadow: 0 0 0 4px {{c.okSoft}}"></span>\n<span>mesh-core-tpe</span>',
    '<span style="width: 8px; height: 8px; border-radius: 50%; background: {{srv.dot}}; box-shadow: 0 0 0 4px {{srv.dotSoft}}"></span>\n'
    '<span style="min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis">{{srv.name}}</span>')
rep('{{s.healthy}} · {{s.uptime}} 41d 6h', '{{srv.state}} · {{s.uptime}} {{srv.up}}')
rep('>v2.4.1</span>', '>{{srv.ver}}</span>')
upd = re.search(r'<button onClick="\{\{goSystem\}\}"[^\n]*?</button>', t).group(0)
rep(upd, '<sc-if value="{{srv.hasUpd}}">' + upd + '</sc-if>')
theme_btn_end = '<path d="{{themeIcon}}"></path></svg>\n</button>'
rep(theme_btn_end, theme_btn_end + '\n<button type="button" onClick="{{logout}}" aria-label="{{s.logout}}" title="{{s.logout}}" '
    'style="display: flex; align-items: center; justify-content: center; width: 44px; height: 44px; border-radius: 14px; '
    'border: 1px solid {{c.stroke}}; background: {{c.glass}}; color: {{c.text}}; cursor: pointer">' + svg('{{I.logout}}', 18) + '</button>')

# ---- top bar
rep('{{s.envProd}} · tpe-1', '{{envLabel}}')
rep('<span style="position: absolute; top: 7px; right: 8px; width: 8px; height: 8px; border-radius: 50%; background: {{c.critDot}}; box-shadow: 0 0 0 2px {{c.bg}}"></span>',
    '<sc-if value="{{hasOpenAlerts}}"><span style="position: absolute; top: 7px; right: 8px; width: 8px; height: 8px; border-radius: 50%; '
    'background: {{c.critDot}}; box-shadow: 0 0 0 2px {{c.bg}}"></span></sc-if>')
rep('<div style="display: flex; align-items: center; gap: 10px; padding-left: 4px">',
    '<button type="button" onClick="{{openAccount}}" aria-label="{{s.account}}" style="display: flex; align-items: center; gap: 10px; '
    'padding: 0 0 0 4px; border: 0; background: transparent; color: inherit; font: inherit; text-align: left; cursor: pointer">')
rep('color: #FFFFFF; font-size: 13px; font-weight: 700">AD</span>', 'color: #FFFFFF; font-size: 13px; font-weight: 700">{{me.ini}}</span>')
rep('<span style="display: block; font-size: 13.5px; font-weight: 600">admin</span><span style="display: block; font-size: 12px; color: {{c.muted}}">{{s.superAdmin}}</span></span>\n</div>',
    '<span style="display: block; font-size: 13.5px; font-weight: 600">{{me.name}}</span><span style="display: block; font-size: 12px; color: {{c.muted}}">{{me.role}}</span></span>\n</button>')

# ---- page header actions
rep('<sc-if value="{{isFrp}}" hint-placeholder-val="{{ false }}">\n<button onClick="{{stopAllFrp}}"',
    '<sc-if value="{{isFrp}}" hint-placeholder-val="{{ false }}">\n<sc-if value="{{isAdmin}}"><button type="button" onClick="{{addNode}}" '
    'style="' + BTN + '; height: 40px">' + svg('{{I.server}}', 15) + '<span>{{s.addNode}}</span></button></sc-if>\n<button onClick="{{stopAllFrp}}"')
rep('<sc-if value="{{isSecurity}}" hint-placeholder-val="{{ false }}">\n<button onClick="{{exportAudit}}"',
    '<sc-if value="{{canGrant}}"><button type="button" onClick="{{grantDirect}}" style="' + PRIMARY + '">' + svg('{{I.plus}}') +
    '<span>{{s.grantDirect}}</span></button></sc-if>\n'
    '<sc-if value="{{isSecurity}}" hint-placeholder-val="{{ false }}">\n<button onClick="{{exportAudit}}"')
# page-level error / loading line under the title
rep('<p style="margin: 4px 0 0; font-size: 14px; color: {{c.muted}}">{{pageSub}}</p>',
    '<p style="margin: 4px 0 0; font-size: 14px; color: {{c.muted}}">{{pageSub}}</p>\n'
    '<sc-if value="{{hasPageErr}}"><p style="margin: 6px 0 0; font-size: 13px; color: {{c.crit}}">{{pageErr}}</p></sc-if>')

# ---- FRP: node actions + empty state
traffic = ('<div style="margin-top: 6px; font-size: 12px; color: {{c.muted}}; display: flex; justify-content: space-between"><span>{{s.thTraffic}}</span>'
           '<span style="font-family: ui-monospace, Menlo, Consolas, monospace">{{n.traffic}}</span></div>')
rep(traffic, traffic + '\n<sc-if value="{{n.hasErr}}"><div style="margin-top: 8px; font-size: 12px; color: {{c.crit}}; word-break: break-word">{{n.err}}</div></sc-if>\n'
    '<sc-if value="{{n.canManage}}"><div style="display: flex; flex-wrap: wrap; gap: 6px; margin-top: 14px">'
    '<button type="button" onClick="{{n.edit}}" style="' + BTN + '">' + svg('{{I.edit}}', 14) + '<span>{{s.edit}}</span></button>'
    '<button type="button" onClick="{{n.config}}" style="' + BTN + '">' + svg('{{I.book}}', 14) + '<span>frps.toml</span></button>'
    '<sc-if value="{{n.managed}}"><button type="button" onClick="{{n.restart}}" style="' + BTN + '">' + svg('{{I.refresh}}', 14) + '<span>{{s.restart}}</span></button></sc-if>'
    '<button type="button" onClick="{{n.remove}}" aria-label="{{s.delete}} {{n.name}}" style="' + BTN + '; width: 36px; padding: 0; color: {{c.crit}}">' + svg('{{I.trash}}', 15) + '</button>'
    '</div></sc-if>')
rep('<section style="flex: 1.4 1 420px;',
    '<sc-if value="{{noNodes}}"><section style="flex: 1 1 300px; min-width: 0; padding: 18px; ' + GLASS + '; font-size: 13.5px; color: {{c.muted}}; line-height: 1.55">'
    '<div style="font-size: 15px; font-weight: 600; color: {{c.text}}">{{s.noNodesTitle}}</div><div style="margin-top: 6px">{{s.noNodes}}</div></section></sc-if>\n'
    '<section style="flex: 1.4 1 420px;')

# ---- access: user management
rep('<h2 style="margin: 0; font-size: 15px; font-weight: 600; letter-spacing: -0.005em">{{s.userLevels}}</h2></div>',
    '<h2 style="margin: 0; font-size: 15px; font-weight: 600; letter-spacing: -0.005em">{{s.userLevels}}</h2>'
    '<sc-if value="{{isAdmin}}"><button type="button" onClick="{{addUser}}" style="' + LINK + '">' + svg('{{I.plus}}', 14) + ' {{s.addUser}}</button></sc-if></div>')
rep('grid-template-columns: minmax(0, 1.3fr) 90px 80px minmax(0, 1fr); padding: 8px 18px">\n<span>{{s.thUser}}</span><span>{{s.thLevel}}</span><span>{{s.thDevices}}</span><span>MFA</span>',
    'grid-template-columns: minmax(0, 1.3fr) 90px 80px minmax(0, 1fr) 36px; padding: 8px 18px">\n<span>{{s.thUser}}</span><span>{{s.thRole}}</span><span>{{s.thDevices}}</span><span>MFA</span><span></span>')
rep('<div style="display: grid; grid-template-columns: minmax(0, 1.3fr) 90px 80px minmax(0, 1fr); gap: 12px; align-items: center; padding: 9px 18px; border-top: 1px solid {{c.hair}}; font-size: 13.5px">',
    '<div style="display: grid; grid-template-columns: minmax(0, 1.3fr) 90px 80px minmax(0, 1fr) 36px; gap: 12px; align-items: center; padding: 9px 18px; border-top: 1px solid {{c.hair}}; font-size: 13.5px; opacity: {{u.op}}">')
rep('<span style="width: 6px; height: 6px; border-radius: 50%; background: currentColor"></span>{{u.stLabel}}</span></span>\n</div>',
    '<span style="width: 6px; height: 6px; border-radius: 50%; background: currentColor"></span>{{u.stLabel}}</span></span>\n'
    '<span><sc-if value="{{u.canEdit}}"><button type="button" onClick="{{u.edit}}" aria-label="{{s.edit}} {{u.name}}" style="' + BTN + '; width: 34px; padding: 0">'
    + svg('{{I.edit}}', 14) + '</button></sc-if></span>\n</div>')

# ---- security: create token, login unblock
rep('<h2 style="margin: 0; font-size: 15px; font-weight: 600; letter-spacing: -0.005em">{{s.apiTokens}}</h2><span style="font-size: 12.5px; color: {{c.muted}}">{{tokCount}}</span></div>',
    '<h2 style="margin: 0; font-size: 15px; font-weight: 600; letter-spacing: -0.005em">{{s.apiTokens}}</h2>'
    '<button type="button" onClick="{{createToken}}" style="' + LINK + '">' + svg('{{I.plus}}', 14) + ' {{s.createToken}}</button></div>')
rep('{{l.abnLabel}}</span></sc-if></span>',
    '{{l.abnLabel}}</span></sc-if><sc-if value="{{l.canUnblock}}"><button type="button" onClick="{{l.unblock}}" style="' + BTN +
    '; height: 26px; padding: 0 8px; font-size: 12px">{{s.unblock}}</button></sc-if></span>')

# ---- monitor
rep('<div style="margin-top: 2px; font-size: 12.5px; color: {{c.muted}}">relay-tpe + relay-nrt</div>',
    '<div style="margin-top: 2px; font-size: 12.5px; color: {{c.muted}}">{{relaySub}}</div>')
rep('<span style="display: block; margin-top: 2px; font-size: 12px; color: {{c.muted}}">{{a.t}} · {{a.dur}}</span>\n</span>\n'
    '<span style="display: inline-flex; align-items: center; gap: 6px; padding: 3px 9px; border-radius: 8px; font-size: 12px; font-weight: 600; white-space: nowrap; color: {{a.stFg}}; background: {{a.stBg}}"><span style="width: 6px; height: 6px; border-radius: 50%; background: currentColor"></span>{{a.stLabel}}</span>\n</div>',
    '<span style="display: block; margin-top: 2px; font-size: 12px; color: {{c.muted}}">{{a.t}} · {{a.dur}}</span>\n</span>\n'
    '<span style="display: inline-flex; align-items: center; gap: 6px; padding: 3px 9px; border-radius: 8px; font-size: 12px; font-weight: 600; white-space: nowrap; color: {{a.stFg}}; background: {{a.stBg}}"><span style="width: 6px; height: 6px; border-radius: 50%; background: currentColor"></span>{{a.stLabel}}</span>\n'
    '<sc-if value="{{a.canResolve}}"><button type="button" onClick="{{a.resolve}}" style="' + BTN + '">{{s.resolve}}</button></sc-if>\n</div>')
rep('<sc-if value="{{noPend}}" hint-placeholder-val="{{ false }}">', '<sc-if value="{{noPend}}" hint-placeholder-val="{{ false }}">')

# ---- system
rep('<span style="color: {{h.lastFg}}">{{h.last}}</span></span>\n</span>\n',
    '<span style="color: {{h.lastFg}}">{{h.last}}</span></span>\n</span>\n'
    '<button type="button" onClick="{{h.test}}" style="' + BTN + '">{{s.test}}</button>\n'
    '<button type="button" onClick="{{h.remove}}" aria-label="{{s.delete}} {{h.url}}" style="' + BTN + '; width: 34px; padding: 0; color: {{c.crit}}">' + svg('{{I.trash}}', 14) + '</button>\n')
rep('<sc-for list="{{hookRows}}" as="h" hint-placeholder-count="3">',
    '<sc-if value="{{hookEmpty}}"><div style="padding: 22px 18px; border-top: 1px solid {{c.hair}}; text-align: center; font-size: 13.5px; color: {{c.muted}}">{{s.noHooks}}</div></sc-if>\n'
    '<sc-for list="{{hookRows}}" as="h" hint-placeholder-count="3">')
rep('<sc-for list="{{keyList}}" as="k" hint-placeholder-count="3">',
    '<sc-if value="{{keyEmpty}}"><div style="padding: 22px 18px; border-top: 1px solid {{c.hair}}; text-align: center; font-size: 13.5px; color: {{c.muted}}">{{keyEmptyText}}</div></sc-if>\n'
    '<sc-for list="{{keyList}}" as="k" hint-placeholder-count="3">')
rep('>https://api.meshport.example.com/v1</span></div>', '>{{baseUrl}}</span> · {{epCount}}</div>\n<div style="max-height: 340px; overflow-y: auto">')
rep('<span style="flex: 1; min-width: 0; font-size: 12.5px; color: {{c.muted}}; text-align: right; white-space: nowrap; overflow: hidden; text-overflow: ellipsis">{{e.d}}</span>\n</div>\n</sc-for>',
    '<span style="flex: 1; min-width: 0; font-size: 12.5px; color: {{c.muted}}; text-align: right; white-space: nowrap; overflow: hidden; text-overflow: ellipsis">{{e.d}}</span>\n</div>\n</sc-for>\n</div>')
rep('<h2 style="margin: 0; font-size: 15px; font-weight: 600; letter-spacing: -0.005em">{{s.updateSettings}}</h2></div>',
    '<h2 style="margin: 0; font-size: 15px; font-weight: 600; letter-spacing: -0.005em">{{s.updateSettings}}</h2>'
    '<button type="button" onClick="{{openSettings}}" style="' + LINK + '">' + svg('{{I.gear}}', 14) + ' {{s.moreSettings}}</button></div>')
rep('<span style="flex: none; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 12.5px">{{e.p}}</span>',
    '<span style="flex: none; min-width: 0; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 12.5px; word-break: break-all">{{e.p}}</span>')

# ---- device drawer: approve / edit / remote command, unban label
rep('<div style="display: flex; flex-wrap: wrap; gap: 8px; margin-top: 24px">\n<button onClick="{{dv.toggle}}"',
    '<div style="display: flex; flex-wrap: wrap; gap: 8px; margin-top: 24px">\n'
    '<sc-if value="{{dv.isPending}}"><button type="button" onClick="{{dv.approve}}" style="' + BTN + '; height: 40px; border: 0; background: {{c.accentBg}}; color: #FFFFFF">'
    + svg('{{I.check}}', 15) + '<span>{{s.approve}}</span></button><button type="button" onClick="{{dv.reject}}" style="' + BTN + '; height: 40px">'
    + svg('{{I.x}}', 15) + '<span>{{s.reject}}</span></button></sc-if>\n'
    '<button type="button" onClick="{{dv.edit}}" style="' + BTN + '; height: 40px">' + svg('{{I.edit}}', 15) + '<span>{{s.edit}}</span></button>\n'
    '<sc-if value="{{dv.online}}"><button type="button" onClick="{{dv.command}}" style="' + BTN + '; height: 40px">' + svg('{{I.terminal}}', 15) + '<span>{{s.remoteCmd}}</span></button></sc-if>\n'
    '<sc-if value="{{dv.canToggle}}"><button onClick="{{dv.toggle}}"')
rep('<span>{{dv.toggleLabel}}</span></button>', '<span>{{dv.toggleLabel}}</span></button></sc-if>')
rep('<path d="{{I.ban}}"></path></svg><span>{{s.banDevice}}</span></button>', '<path d="{{dv.banIcon}}"></path></svg><span>{{dv.banLabel}}</span></button>')
rep('<div style="display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 16px; margin-top: 20px; padding: 16px; border-radius: 16px; background: {{c.tile}}">',
    '<div style="display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px 16px; margin-top: 20px; padding: 16px; border-radius: 16px; background: {{c.tile}}; word-break: break-all">')

# ---- mapping modal: node list may be long; protocol hint
rep('<div style="margin-top: 16px; padding: 14px; border-radius: 14px; background: {{c.accentSoft}}">',
    '<sc-if value="{{mf.hasErr}}"><div style="margin-top: 12px; font-size: 13px; color: {{c.crit}}">{{mf.err}}</div></sc-if>\n'
    '<div style="margin-top: 16px; padding: 14px; border-radius: 14px; background: {{c.accentSoft}}">')

# ---- generic form sheet and info sheet (same look as the mapping modal)
field_text = ('<sc-if value="{{f.isText}}"><label style="' + LABEL + '; grid-column: {{f.span}}">{{f.label}}'
              '<input type="{{f.type}}" value="{{f.value}}" placeholder="{{f.ph}}" autocomplete="{{f.ac}}" onChange="{{f.set}}" style="' + INPUT + '; font-family: {{f.font}}">'
              '<sc-if value="{{f.hasHint}}"><span style="font-weight: 400; font-size: 12px; color: {{c.faint}}">{{f.hint}}</span></sc-if></label></sc-if>')
field_sel = ('<sc-if value="{{f.isSelect}}"><label style="' + LABEL + '; grid-column: {{f.span}}">{{f.label}}'
             '<select value="{{f.value}}" onChange="{{f.set}}" style="' + INPUT + '"><sc-for list="{{f.opts}}" as="o"><option value="{{o.v}}">{{o.label}}</option></sc-for></select>'
             '<sc-if value="{{f.hasHint}}"><span style="font-weight: 400; font-size: 12px; color: {{c.faint}}">{{f.hint}}</span></sc-if></label></sc-if>')
field_seg = ('<sc-if value="{{f.isSeg}}"><div style="' + LABEL + '; grid-column: {{f.span}}"><span>{{f.label}}</span>'
             '<div role="group" aria-label="{{f.label}}" style="' + SEGWRAP + '; align-self: flex-start"><sc-for list="{{f.seg}}" as="o">'
             '<button type="button" onClick="{{o.pick}}" aria-pressed="{{o.pressed}}" style="' + SEGBTN + '">{{o.label}}</button></sc-for></div>'
             '<sc-if value="{{f.hasHint}}"><span style="font-weight: 400; font-size: 12px; color: {{c.faint}}">{{f.hint}}</span></sc-if></div></sc-if>')
field_sw = ('<sc-if value="{{f.isSwitch}}"><div style="grid-column: {{f.span}}; display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13.5px">'
            '<span><span style="display: block; font-weight: 600">{{f.label}}</span><span style="display: block; font-size: 12px; color: {{c.muted}}">{{f.hint}}</span></span>'
            + switch('f.sw') + '</div></sc-if>')
field_area = ('<sc-if value="{{f.isArea}}"><label style="' + LABEL + '; grid-column: {{f.span}}">{{f.label}}'
              '<textarea value="{{f.value}}" placeholder="{{f.ph}}" onChange="{{f.set}}" style="' + INPUT + '; height: 120px; padding: 10px 12px; resize: vertical; font-family: ' + MONO + '; font-size: 13px"></textarea></label></sc-if>')
field_info = ('<sc-if value="{{f.isInfo}}"><div style="' + LABEL + '; grid-column: {{f.span}}"><span>{{f.label}}</span>'
              '<div style="display: flex; align-items: flex-start; gap: 8px"><div style="flex: 1; min-width: 0; padding: 10px 12px; border-radius: 12px; background: {{c.tile}}; '
              'color: {{c.text}}; font-family: ' + MONO + '; font-size: 13px; font-weight: 500; white-space: pre-wrap; word-break: break-all; max-height: 360px; overflow-y: auto">{{f.value}}</div>'
              '<sc-if value="{{f.copy}}"><button type="button" onClick="{{f.copy}}" aria-label="{{s.copy}} {{f.label}}" style="' + BTN + '; width: 36px; height: 38px; padding: 0">'
              + svg('{{I.copy}}', 15) + '</button></sc-if></div>'
              '<sc-if value="{{f.hasHint}}"><span style="font-weight: 400; font-size: 12px; color: {{c.faint}}">{{f.hint}}</span></sc-if></div></sc-if>')
field_image = ('<sc-if value="{{f.isImage}}"><div style="grid-column: 1 / -1; text-align: center">'
               '<img src="{{f.value}}" alt="{{f.label}}" width="256" height="256" style="max-width: 100%; height: auto; background: #fff; border-radius: 12px; padding: 12px; box-sizing: border-box"></div></sc-if>')
form_body = ('<sc-if value="{{fm.hasDesc}}"><p style="margin: 8px 0 0; font-size: 13.5px; color: {{c.muted}}; line-height: 1.55">{{fm.desc}}</p></sc-if>\n'
             '<div style="display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; margin-top: 18px">\n'
             '<sc-for list="{{fm.fields}}" as="f">' + field_image + field_text + field_sel + field_seg + field_sw + field_area + field_info + '</sc-for>\n</div>\n'
             '<sc-if value="{{fm.hasErr}}"><div style="margin-top: 14px; font-size: 13px; color: {{c.crit}}">{{fm.err}}</div></sc-if>')
form_foot = ('<div style="display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 10px; margin-top: 20px">\n'
             '<sc-for list="{{fm.extra}}" as="b"><button type="button" onClick="{{b.go}}" style="' + BTN + '; height: 42px; padding: 0 18px; color: {{b.fg}}">{{b.label}}</button></sc-for>\n'
             '<div style="flex: 1"></div>\n'
             '<sc-if value="{{fm.hasCancel}}"><button type="button" onClick="{{closeOverlay}}" style="' + BTN + '; height: 42px; padding: 0 18px">{{s.cancel}}</button></sc-if>\n'
             '<sc-if value="{{fm.hasSave}}"><button type="submit" aria-disabled="{{fm.busy}}" style="' + PRIMARY + '; height: 42px; background: {{fm.saveBg}}">{{fm.saveLabel}}</button></sc-if>\n</div>')
form_modal = modal('hasForm', 600, '{{fm.title}}', '<form onSubmit="{{fm.save}}" novalidate>\n' + form_body + '\n' + form_foot + '\n</form>', '')
rep('<sc-if value="{{hasConfirm}}"', form_modal + '\n<sc-if value="{{hasConfirm}}"')

# ---- <select> cannot hold <sc-for> in the HTML parser: move the list to data-options
t, n = re.subn(r'(<select[^>]*?)>\s*<sc-for list="\{\{([\w.]+)\}\}" as="o"[^>]*>\s*<option value="\{\{o\.v\}\}">\{\{o\.label\}\}</option>\s*</sc-for>\s*</select>',
               r'\1 data-options="\2"></select>', t)
assert n == 2, n

# ---- convert to CSP-friendly attributes
t = t.replace(' style="', ' data-s="')
t = re.sub(r' on([A-Z][a-zA-Z]*)="', lambda m: ' data-on-' + m.group(1).lower() + '="', t)
t = re.sub(r' hint-[a-z-]+="[^"]*"', '', t)
assert ' style="' not in t and not re.search(r' on[A-Z]', t)

header = ('<!-- Meshport server console template.\n'
          '     Markup and styling follow the approved console design (Admin.dc.html); app.js supplies the\n'
          '     values from the live API. Bindings: {{path}}, <sc-for list as>, <sc-if value>, data-s = style,\n'
          '     data-on-<event> = handler. Generated by scripts/console-template/build_tpl.py. -->\n')
auth = open(sys.argv[3], encoding='utf-8').read() if len(sys.argv) > 3 else ''
out = header + '<template id="admin">\n' + t + '\n</template>\n' + auth
open(dst, 'w', encoding='utf-8').write(out)
print('ok', len(out))
