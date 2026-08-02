package main

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// panelHTML is the page behind the management UI menu entry.
//
// The panel is opened with a plain browser navigation, which cannot carry an
// Authorization header, and the resource route is publicly reachable. The page
// is therefore purely explanatory: it documents how to configure the plugin and
// connect the Pi extension, and contains no credentials, account data, or
// upstream state.
const panelHTML = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pi Bridge</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.6 ui-sans-serif, system-ui, sans-serif; margin: 0; padding: 32px 24px; }
  main { max-width: 720px; margin: 0 auto; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .sub { opacity: .7; margin: 0 0 28px; }
  h2 { font-size: 12px; margin: 28px 0 10px; text-transform: uppercase; letter-spacing: .05em; opacity: .6; }
  code { font-family: ui-monospace, monospace; background: rgba(128,128,128,.15); padding: 1px 5px; border-radius: 4px; word-break: break-all; }
  pre { font-family: ui-monospace, monospace; background: rgba(128,128,128,.12); padding: 12px 14px; border-radius: 8px; overflow-x: auto; }
  ol { padding-left: 20px; }
  li { margin-bottom: 8px; }
  a { color: inherit; }
  .note { opacity: .65; font-size: 13px; }
</style>
<main>
  <h1>Pi Bridge</h1>
  <p class="sub">Serves provider quota to the Pi extension using the same API key it already uses for model calls.</p>

  <h2>Configure the plugin</h2>
  <ol>
    <li>By default every CLIProxyAPI API key may read quota. To restrict it, turn off
        <strong>allow_all_api_keys</strong> and list the permitted keys in <strong>allowed_keys</strong>.</li>
    <li>Enable <strong>show_extra_analytics</strong> if CPA Manager Plus is installed.</li>
    <li>Leave <strong>advanced</strong> empty unless a URL, secret source, or cache TTL must differ from the defaults.</li>
  </ol>

  <h2>Connect Pi</h2>
  <p>Install the extension and point it at this server:</p>
  <pre>pi install npm:pi-cliproxyapi</pre>
  <p>Then run <code>/cliproxy-setup</code> in Pi. Use your normal API key — no separate usage key is
     needed. Quota is read from:</p>
  <pre>GET /v0/resource/plugins/pi-bridge/dev/usage
Authorization: Bearer &lt;your API key&gt;</pre>
  <p class="note">Responses are cached server-side, so provider limits are not refetched on every request.</p>

  <h2>Package</h2>
  <p><a href="https://www.npmjs.com/package/pi-cliproxyapi" target="_blank" rel="noreferrer noopener">npm: pi-cliproxyapi</a></p>
</main>
`

// panelResponse serves the menu page. It is intentionally independent of plugin
// configuration so the page still renders when configuration is invalid.
func panelResponse() pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
			"X-Robots-Tag":  []string{"noindex, nofollow"},
			"Content-Security-Policy": []string{
				"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'",
			},
		},
		Body: []byte(panelHTML),
	}
}
