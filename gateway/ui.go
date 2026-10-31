package main

import (
	"html/template"
	"net/http"
)

// This original mark identifies rdsh; it does not imply DeepSeek endorsement.
const pageHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><title>{{if .Logout}}Sign out{{else}}Sign in{{end}} · rdsh</title><link rel="stylesheet" href="/_rdsh/style.css"></head>
<body><main><div class="brand"><svg width="48" height="48" viewBox="0 0 48 48" role="img" aria-label="rdsh"><rect x="3" y="3" width="42" height="42" rx="15" fill="currentColor"/><path d="M14 26c4 5 15 5 20-4M14 20c4-5 15-5 20 0" stroke="white" stroke-width="3" stroke-linecap="round" fill="none"/></svg><span>rdsh</span></div>
{{if .Logout}}<h1>Sign out</h1><p class="subtitle">Close this browser session.</p><form method="post" action="/_rdsh/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Sign out</button></form><a class="back" href="/">Back to DeepSeek Harness</a>
{{else}}<h1>Sign in</h1><p class="subtitle">Your DeepSeek Harness workspace</p><form method="post" action="/_rdsh/login"><input type="hidden" name="csrf" value="{{.CSRF}}"><label for="username">Username</label><input id="username" name="username" type="text" value="{{.Username}}" autocomplete="username" autocapitalize="none" spellcheck="false" required maxlength="128"><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required maxlength="72">{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}<button type="submit">Sign in</button></form>{{end}}
</main></body></html>`

const stylesheet = `:root{font-family:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#202127;background:#fff;font-synthesis:none;color-scheme:light dark}*{box-sizing:border-box}body{margin:0;min-height:100svh;display:grid;place-items:center;padding:32px 24px}main{width:100%;max-width:340px}.brand{display:flex;align-items:center;justify-content:center;gap:12px;color:#4d6bfe;font-size:28px;font-weight:650;letter-spacing:-1px;margin-bottom:32px}h1{text-align:center;font-size:24px;letter-spacing:-.5px;font-weight:600;margin:0 0 10px}.subtitle{text-align:center;color:#737580;font-size:14px;margin:0 0 32px}form{display:flex;flex-direction:column}label{font-size:13px;font-weight:550;margin-bottom:8px}input{font:inherit;font-size:16px;padding:13px 14px;border:1px solid #dedfe6;border-radius:10px;background:#fff;color:inherit;margin-bottom:20px;width:100%;min-height:48px}input:focus-visible,button:focus-visible,a:focus-visible{outline:3px solid #4d6bfe55;outline-offset:3px}input:focus{border-color:#4d6bfe}button{border:0;border-radius:10px;background:#4d6bfe;color:white;padding:14px;font:inherit;font-size:15px;font-weight:600;cursor:pointer;min-height:48px;margin-top:4px}button:hover{background:#3c59e8}.error{font-size:14px;line-height:1.5;color:#b62f3c;margin:0 0 16px}.back{display:block;margin-top:24px;text-align:center;color:#4d6bfe;font-size:14px;text-decoration:none}@media(prefers-color-scheme:dark){:root{background:#1a1a1a;color:#ececf0}input{background:#252525;border-color:#45454a}.subtitle{color:#a4a4ae}.error{color:#ff9ca5}}@media(max-height:550px){body{place-items:start center}.brand{margin-bottom:20px}.subtitle{margin-bottom:20px}}`

var page = template.Must(template.New("login").Parse(pageHTML))

type pageData struct {
	Username, CSRF, Error string
	Logout                bool
}

func render(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	_ = page.Execute(w, data)
}
func (g *gateway) renderLogin(w http.ResponseWriter, status int, csrf, message string) {
	render(w, status, pageData{Username: g.config.Username, CSRF: csrf, Error: message})
}
func (g *gateway) renderLogout(w http.ResponseWriter, csrf string) {
	render(w, http.StatusOK, pageData{Logout: true, CSRF: csrf})
}
