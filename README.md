# pi-bridge

A native CLIProxyAPI plugin that lets the Pi `pi-cliproxyapi` extension read
cached provider quota using **the same ordinary API key it already uses for
model calls** — no second key, no extra container.

## Why resource routes

CLIProxyAPI plugins can register two kinds of routes:

| Kind | Mounted under | Authentication |
| --- | --- | --- |
| `ManagementRoute` | `/v0/management/` | Always requires the **CPA Management Key** |
| `ResourceRoute` | `/v0/resource/plugins/<id>/` | **Not** management-authenticated |

Giving Pi the CPA Management Key would hand every model client full
administrative access. So `pi-bridge` registers **resource routes** and performs
its own authentication against an explicit allow-list of ordinary API keys.

## Endpoints

Both are served under `/v0/resource/plugins/pi-bridge/` and take the normal
CLIProxyAPI API key:

```http
GET /v0/resource/plugins/pi-bridge/dev/capabilities
GET /v0/resource/plugins/pi-bridge/dev/usage
GET /v0/resource/plugins/pi-bridge/dev/usage?refresh=1
Authorization: Bearer <ordinary CLIProxyAPI API key>
```

Plus the unauthenticated management UI page:

```http
GET /v0/resource/plugins/pi-bridge/panel
```

`/dev/usage` returns the same `schemaVersion: 1` document shape the Pi plugin
already renders, so the client contract does not change:

```json
{
  "schemaVersion": 1,
  "client": { "id": "abix", "keyHint": "sk-6cae…57a3" },
  "cache": { "updatedAt": "...", "stale": false, "ttlMs": 60000 },
  "accounts": [
    {
      "provider": "claude",
      "account": "d***@gmail.com",
      "supported": true,
      "groups": [
        { "id": "five-hour", "label": "5h Session", "remainingFraction": 0.83 }
      ]
    }
  ]
}
```

## Configuration

Under `plugins.configs.pi-bridge` in the CLIProxyAPI `config.yaml`. Every field is
a flat scalar or a list, so the management UI renders real inputs:

```yaml
plugins:
  configs:
    pi-bridge:
      enabled: true
      priority: 3
      store:
        version: 0.1.2
      client_keys:
        - abix:<64-hex sha256 of the API key>
        - team:<64-hex>:usage+analytics
      management_url: http://127.0.0.1:8317/v0/management
      management_key_env: MANAGEMENT_PASSWORD
      cpam_enabled: auto
      cpam_url: http://cpa-manager-plus:18317
      cpam_admin_key_file: /run/secrets/cpam_admin_key
      usage_ttl_seconds: 60
      capabilities_ttl_seconds: 300
```

`client_keys` entries are `alias:fingerprint[:permissions]`. The alias is a safe
label shown in responses, the fingerprint is the SHA-256 of the API key (an
optional `sha256:` prefix is accepted), and permissions are `+`-separated,
defaulting to `usage`.

Generate a fingerprint without printing the key:

```bash
printf '%s' "$API_KEY" | shasum -a 256 | awk '{print $1}'
```

### Management UI

The plugin adds a **Pi Bridge** menu entry serving a static page that documents
the endpoints and can display quota. The page holds no credentials: a key typed
there stays in the browser tab and is sent directly to the usage endpoint.

Only this HTML page declares a menu. The JSON endpoints deliberately do not: the
UI opens menu entries with `window.open`, a plain navigation that cannot carry an
`Authorization` header, so a bearer-protected route listed as a menu item would
always render as `401`.

### Secret handling

- Raw API keys are **never** stored in configuration — only SHA-256 fingerprints.
- The CPA Management Key is read from `MANAGEMENT_PASSWORD`, which the
  CLIProxyAPI container already has; no new secret is introduced.
- The CPAM admin key is read from a file (Docker secret) or env var.
- No key, token, or `Authorization` header is ever logged or returned.
- Responses carry only a masked hint (`sk-6cae…57a3`) and masked account emails.

## Security model

- Authentication is a constant-time comparison against the fingerprint allow-list.
- An empty or invalid allow-list **fails closed** — every request is refused.
- All rejection paths return an identical `401`, so a caller cannot probe which
  fingerprints exist.
- There is **no generic management proxy**. The plugin issues only fixed,
  read-only provider quota calls; client input never selects an upstream URL.
- `?refresh=1` is rate limited per client key.

## Build

```bash
./build.sh
```

Produces `dist/pi-bridge-v<version>.so` for `linux/amd64`, built against SDK
`v7.2.93`. The SDK version must match the running CLIProxyAPI image or
`cliproxy_plugin_init` will be rejected.

## Deploy without restarting CLIProxyAPI

CLIProxyAPI watches `config.yaml` (and the auth dir) with fsnotify. Touching the
config triggers a reload that loads plugins and re-registers their routes, so a
container restart is not needed — and a restart would interrupt in-flight model
traffic.

The host only hot-reloads a plugin when its file **path** changes, so overwriting
a `.so` in place has no effect. Ship each build under its own versioned name:

```bash
# 1. copy the new artifact alongside the current one
scp dist/pi-bridge-v0.1.1.so root@host:/root/projects/llm-proxy/CLIProxyAPI/plugins/

# 2. touch the config to trigger the watcher
ssh root@host "touch /root/projects/llm-proxy/CLIProxyAPI/data/config.yaml"
```

With several versions present the host selects the highest one. Pin a specific
build — or roll back — with `store.version`:

```yaml
plugins:
  configs:
    pi-bridge:
      store:
        version: 0.1.0
```

Confirm the swap in the logs:

```
pluginhost: plugin hot reloaded plugin_id=pi-bridge
```

Keep the previous `.so` in place as a rollback target. Old files are pruned only
once per process start, never during a reload.

## Test

```bash
go vet ./...
go test ./...
```
