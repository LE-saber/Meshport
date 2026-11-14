"""Assemble the original Design template with the live API logic. No npm/CDN required."""
from pathlib import Path
from hashlib import sha256

root = Path(__file__).resolve().parents[1] / 'web' / 'console'
template = (root / 'console.template.html').read_text(encoding='utf-8')
template = template.replace('<!-- AUTH -->', (root / 'auth.html').read_text(encoding='utf-8'))
template = template.replace('<!-- EXTRA -->', (root / 'dialogs.html').read_text(encoding='utf-8'))
for asset in ('console.css', 'vendor/react.js', 'vendor/react-dom.js', 'vendor/support.js'):
    version = sha256((root / asset).read_text(encoding='utf-8').encode('utf-8')).hexdigest()[:12]
    template = template.replace(f'"{asset}"', f'"{asset}?v={version}"')
# Add contextual controls without changing the original cards and navigation.
anchor = '<!-- PAGE ACTIONS -->'
if anchor not in template:
    marker = '<div style="display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 12px">'
    template = template.replace(marker, anchor + '\n' + marker, 1)
template = template.replace(anchor, '<div class="extra-actions"><sc-for list="{{extraActions}}" as="a"><button onClick="{{a.run}}" disabled="{{a.disabled}}">{{a.label}}</button></sc-for></div>')
code = '\n'.join((root / file).read_text(encoding='utf-8') for file in ('design.js', 'app.js'))
if '</script' in code.lower():
    raise ValueError('Inline console source contains a script closing tag')
template += '<script type="text/x-dc" data-dc-script>\n' + code + '\n</script>\n</body></html>\n'
(root / 'index.html').write_text(template, encoding='utf-8', newline='\n')
print('Built web/console/index.html')
