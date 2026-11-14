package api

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
)

var paramRe = regexp.MustCompile(`\{(\w+)\}`)

var tagTitles = map[string]string{
	"auth": "Console authentication", "device": "Devices (client API)", "messaging": "Messaging, files and P2P",
	"access": "Access and approvals", "overview": "Overview", "devices": "Device management", "comms": "Communication",
	"frp": "FRP", "security": "Security", "monitor": "Monitoring and logs", "system": "System and API",
}

var authText = map[string]string{
	AuthPublic: "none", AuthDevice: "device token (mpd_)", AuthViewer: "console viewer+ / API token read",
	AuthOperator: "console operator+ / API token write", AuthAdmin: "console admin / API token admin",
	AuthAny: "device token or console viewer+", AuthSession: "any console user",
}

// OpenAPI builds an OpenAPI 3.1 document from the route table.
func (s *Server) OpenAPI() map[string]any {
	paths := map[string]map[string]any{}
	for _, r := range s.routes {
		op := map[string]any{
			"summary":     r.Summary,
			"tags":        []string{r.Tag},
			"operationId": strings.ToLower(r.Method) + strings.ReplaceAll(strings.ReplaceAll(paramRe.ReplaceAllString(r.Path, "By_$1"), "/", "_"), "-", "_"),
			"responses": map[string]any{
				"200": map[string]any{"description": "OK"},
				"4XX": map[string]any{"description": "Error", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}},
			},
			"x-auth": authText[r.Auth],
		}
		if r.Auth == AuthPublic {
			op["security"] = []any{}
		}
		params := []any{}
		for _, m := range paramRe.FindAllStringSubmatch(r.Path, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]string{"type": "string"}})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if r.Body != "" {
			op["requestBody"] = map[string]any{"description": r.Body, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}
		}
		if paths[r.Path] == nil {
			paths[r.Path] = map[string]any{}
		}
		paths[r.Path][strings.ToLower(r.Method)] = op
	}
	tags := []map[string]string{}
	for _, t := range []string{"auth", "device", "messaging", "access", "overview", "devices", "comms", "frp", "security", "monitor", "system"} {
		tags = append(tags, map[string]string{"name": t, "description": tagTitles[t]})
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title": "Meshport API", "version": Version,
			"description": "Unified API for the Meshport management console, desktop and mobile clients. All bodies are JSON. Errors are {\"error\":{\"code\",\"message\"}}. The device WebSocket protocol is documented in docs/API.md.",
		},
		"servers":  []map[string]string{{"url": strings.TrimSuffix(s.cfg.PublicURL, "/")}},
		"tags":     tags,
		"paths":    paths,
		"security": []map[string][]string{{"bearer": {}}},
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearer": map[string]string{"type": "http", "scheme": "bearer", "description": "mps_ console session, mpd_ device token, mpt_ personal API token, mpk_ service key"}},
			"schemas": map[string]any{"Error": map[string]any{"type": "object", "properties": map[string]any{
				"error": map[string]any{"type": "object", "properties": map[string]any{"code": map[string]string{"type": "string"}, "message": map[string]string{"type": "string"}}},
			}}},
		},
	}
}

func (s *Server) hOpenAPI(c *Ctx) error { return c.OK(s.OpenAPI()) }

func (s *Server) hDocsHTML(c *Ctx) error {
	byTag := map[string][]Route{}
	for _, r := range s.routes {
		byTag[r.Tag] = append(byTag[r.Tag], r)
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Meshport API</title>
<style>:root{--bg:#fff;--fg:#1d1d1b;--mut:#6b6b66;--line:#e7e5df;--acc:#12a383}@media(prefers-color-scheme:dark){:root{--bg:#141413;--fg:#ecebe6;--mut:#a3a29c;--line:#2c2b29;--acc:#14a07f}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif}main{max-width:980px;margin:0 auto;padding:32px 16px}
h1{font-size:26px;margin:0 0 4px}h2{font-size:18px;margin:32px 0 8px;border-bottom:1px solid var(--line);padding-bottom:6px}
table{width:100%;border-collapse:collapse}td{padding:6px 8px;border-bottom:1px solid var(--line);vertical-align:top}
code{font:13px ui-monospace,monospace}.m{font-weight:600;color:var(--acc);width:64px}.a,.d{color:var(--mut);font-size:13px}p{color:var(--mut)}
@media(max-width:640px){td{display:block;border:0;padding:2px 0}tr{display:block;border-bottom:1px solid var(--line);padding:6px 0}}</style><main>`)
	fmt.Fprintf(&b, `<h1>Meshport API</h1><p>Version %s · <a href="/api/v1/openapi.json">openapi.json</a> · Errors: <code>{"error":{"code","message"}}</code> · Auth: <code>Authorization: Bearer &lt;token&gt;</code></p>`, html.EscapeString(Version))
	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		tags = append(tags, t)
	}
	order := map[string]int{"auth": 0, "device": 1, "messaging": 2, "access": 3, "overview": 4, "devices": 5, "comms": 6, "frp": 7, "security": 8, "monitor": 9, "system": 10}
	sort.Slice(tags, func(i, j int) bool { return order[tags[i]] < order[tags[j]] })
	for _, t := range tags {
		fmt.Fprintf(&b, "<h2>%s</h2><table>", html.EscapeString(tagTitles[t]))
		for _, r := range byTag[t] {
			body := ""
			if r.Body != "" {
				body = "<div class=d>body <code>" + html.EscapeString(r.Body) + "</code></div>"
			}
			fmt.Fprintf(&b, `<tr><td class=m>%s</td><td><code>%s</code><div>%s</div>%s</td><td class=a>%s</td></tr>`,
				r.Method, html.EscapeString(r.Path), html.EscapeString(r.Summary), body, html.EscapeString(authText[r.Auth]))
		}
		b.WriteString("</table>")
	}
	b.WriteString("</main></html>")
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := c.W.Write([]byte(b.String()))
	return err
}
