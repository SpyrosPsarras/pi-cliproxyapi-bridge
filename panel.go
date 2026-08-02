package main

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// panelHTML is the page behind the management UI menu entry.
//
// The panel is opened with a plain browser navigation, which cannot carry an
// Authorization header, and the resource route is publicly reachable. The page
// therefore serves no account data, no key material and no upstream state: it
// documents the endpoints and, once an operator supplies a key, fetches quota
// from the browser so the credential is never handled server-side here.
//
// All rendering uses textContent rather than innerHTML: the host only escapes
// JSON-like bodies, so an HTML response must be safe by construction.
const panelHTML = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pi Bridge</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.5 ui-sans-serif, system-ui, sans-serif; margin: 0; padding: 24px; }
  main { max-width: 880px; margin: 0 auto; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .sub { opacity: .7; margin: 0 0 20px; }
  section { border: 1px solid rgba(128,128,128,.3); border-radius: 8px; padding: 16px; margin-bottom: 16px; }
  h2 { font-size: 12px; margin: 0 0 12px; text-transform: uppercase; letter-spacing: .05em; opacity: .6; }
  code { font-family: ui-monospace, monospace; background: rgba(128,128,128,.15); padding: 1px 5px; border-radius: 4px; }
  input { font: inherit; padding: 7px 10px; border-radius: 6px; border: 1px solid rgba(128,128,128,.4); background: transparent; color: inherit; min-width: 300px; }
  button { font: inherit; padding: 7px 14px; border-radius: 6px; border: 1px solid rgba(128,128,128,.4); background: transparent; color: inherit; cursor: pointer; }
  button:hover { border-color: rgba(128,128,128,.7); }
  table { border-collapse: collapse; width: 100%; margin-top: 14px; }
  th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid rgba(128,128,128,.2); vertical-align: top; }
  th { font-weight: 600; opacity: .6; font-size: 12px; }
  .bar { display: inline-block; width: 84px; height: 6px; border-radius: 3px; background: rgba(128,128,128,.25); vertical-align: middle; margin-right: 8px; overflow: hidden; }
  .bar i { display: block; height: 100%; background: currentColor; }
  .muted { opacity: .55; }
  .err { color: #c0392b; }
  .row { white-space: nowrap; }
</style>
<main>
  <h1>Pi Bridge</h1>
  <p class="sub">Cached provider quota for the Pi CLIProxyAPI extension.</p>

  <section>
    <h2>Endpoints</h2>
    <p>Each one takes the ordinary CLIProxyAPI API key already used for model calls:</p>
    <p>
      <code>GET dev/capabilities</code><br>
      <code>GET dev/usage</code><br>
      <code>GET dev/usage?refresh=1</code>
    </p>
    <p class="muted">Send <code>Authorization: Bearer &lt;api key&gt;</code>. Only allow-listed key fingerprints are accepted; the management key is never required.</p>
  </section>

  <section>
    <h2>Quota</h2>
    <p class="muted">This page stores no credentials. The key stays in this browser tab and is sent straight to the endpoint above.</p>
    <p>
      <input id="key" type="password" placeholder="sk-..." autocomplete="off" spellcheck="false">
      <button id="load">Load</button>
      <button id="refresh">Refresh</button>
    </p>
    <div id="out"></div>
  </section>
</main>
<script>
(function () {
  var base = location.pathname.replace(/\/panel$/, "");
  var keyEl = document.getElementById("key");
  var out = document.getElementById("out");
  var saved = sessionStorage.getItem("pi-bridge-key");
  if (saved) { keyEl.value = saved; }

  function el(tag, text) {
    var node = document.createElement(tag);
    if (text !== undefined) { node.textContent = String(text); }
    return node;
  }
  function clear() { while (out.firstChild) { out.removeChild(out.firstChild); } }
  function say(message, cls) {
    clear();
    var p = el("p", message);
    if (cls) { p.className = cls; }
    out.appendChild(p);
  }

  function windowCell(groups) {
    var td = el("td");
    if (!groups || !groups.length) {
      var dash = el("span", "no quota reported");
      dash.className = "muted";
      td.appendChild(dash);
      return td;
    }
    groups.forEach(function (g) {
      var fraction = Math.max(0, Math.min(1, g.remainingFraction));
      var row = el("div");
      row.className = "row";
      var bar = el("span");
      bar.className = "bar";
      var fill = el("i");
      fill.style.width = (fraction * 100) + "%";
      bar.appendChild(fill);
      row.appendChild(bar);
      row.appendChild(el("span", g.label + " " + Math.round(fraction * 100) + "% left"));
      td.appendChild(row);
    });
    return td;
  }

  function render(doc) {
    clear();
    var head = el("p", "client " + doc.client.id + " (" + doc.client.keyHint + ")"
      + " \u00b7 updated " + doc.cache.updatedAt
      + " \u00b7 ttl " + Math.round(doc.cache.ttlMs / 1000) + "s");
    head.className = "muted";
    out.appendChild(head);

    var table = el("table");
    var headRow = el("tr");
    ["Provider", "Account", "Status", "Windows"].forEach(function (label) {
      headRow.appendChild(el("th", label));
    });
    var thead = el("thead");
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = el("tbody");
    doc.accounts.forEach(function (account) {
      var tr = el("tr");
      tr.appendChild(el("td", account.provider));
      tr.appendChild(el("td", account.account));
      tr.appendChild(el("td", account.supported ? account.status : "unsupported"));
      tr.appendChild(windowCell(account.groups));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    out.appendChild(table);
  }

  function load(force) {
    var key = keyEl.value.trim();
    if (!key) { say("Enter an API key.", "err"); return; }
    sessionStorage.setItem("pi-bridge-key", key);
    say("Loading\u2026", "muted");
    fetch(base + "/dev/usage" + (force ? "?refresh=1" : ""), {
      headers: { "Authorization": "Bearer " + key, "Accept": "application/json" }
    }).then(function (response) {
      if (response.status === 401) {
        throw new Error("Unauthorized: this key is not allow-listed for usage.");
      }
      if (!response.ok) {
        throw new Error("Request failed with status " + response.status + ".");
      }
      return response.json();
    }).then(render).catch(function (error) {
      say(error.message, "err");
    });
  }

  document.getElementById("load").addEventListener("click", function () { load(false); });
  document.getElementById("refresh").addEventListener("click", function () { load(true); });
  keyEl.addEventListener("keydown", function (event) {
    if (event.key === "Enter") { load(false); }
  });
  if (saved) { load(false); }
})();
</script>
`

// panelResponse serves the menu page. It is intentionally independent of plugin
// configuration so the page still renders when the allow-list is misconfigured.
func panelResponse() pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
			"X-Robots-Tag":  []string{"noindex, nofollow"},
			"Content-Security-Policy": []string{
				"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'",
			},
		},
		Body: []byte(panelHTML),
	}
}
