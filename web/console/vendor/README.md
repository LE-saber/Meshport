# Runtime provenance

These JavaScript files were already embedded in the supplied server Design export:

- `react.js`: React 18.3.1 production UMD bundle, MIT license (notice retained).
- `react-dom.js`: accompanying React DOM 18 production UMD bundle, MIT license (notice retained).
- `support.js`: exported dc-runtime template interpreter.

The console loads these local copies before boot, so no CDN is needed.

One local correction is applied to the exported template interpreter: a whole-attribute
`{{expression}}` match must contain a single interpolation only. A value such as
`{{s.edit}} {{m.name}}` must use string interpolation rather than be treated as one
property path. Without the correction, accessible names on device and mapping action
buttons disappear. The live browser verification exercises those named buttons.
