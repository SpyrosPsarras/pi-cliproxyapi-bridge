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
GET /v0/resource/plugins/pi-bridge/capabilities
GET /v0/resource/plugins/pi-bridge/usage
GET /v0/resource/plugins/pi-bridge/usage?refresh=1
GET /v0/resource/plugins/pi-bridge/well-known
Authorization: Bearer <ordinary CLIProxyAPI API key>
```

## Caching and rate limits

The plugin is the only component that calls the providers' quota endpoints, and
those endpoints are rate limited per account. Its cache is therefore what keeps
those limits satisfied, and clients are expected to poll the plugin freely
instead of reaching further upstream.

| Layer | Default | Purpose |
| --- | --- | --- |
| plugin `usage` | 120 s | the only thing that talks to providers |
| plugin `capabilities` | 300 s | the catalogue changes rarely |
| Pi extension | 15 s | re-reads the plugin; never reaches providers |

Raise `usage_ttl_seconds` if a provider tightens its limits; lowering it below
about a minute is not advisable.

## Model metadata

`well-known` serves the model catalogue. Metadata for each model is resolved
in order:

| Source | Notes |
| --- | --- |
| `model_overrides` | hand-written, wins outright, applied field by field |
| CPA Manager Plus | operator-curated prices; needs its admin key |
| models.dev | via `model_aliases` when the proxy id differs |
| defaults | 128k/16k, reported as `metadataSource: "default"` |

Where several providers publish the same id, the model's own vendor wins; with
no vendor entry, the metadata most hosts agree on wins. Ties break on provider
name so the same model never changes limits between restarts.

Every model reports `metadataSource`, and contract v2 lists `unmatchedIds`, so
it is visible which models still need an alias or override:

```yaml
      advanced: |
        {"model_aliases":   {"house-model": "gpt-5.6-sol"},
         "model_overrides": {"custom-llm": {"context_window": 262144}}}
```

The CPAM admin key is read from `/CLIProxyAPI/cpam-admin-key` or
`$CPAM_ADMIN_KEY`. It is deliberately not a config field: a plugin cannot
rewrite `config.yaml`, so anything typed into the panel would stay there in
clear text.

The same routes are also served under a `/dev/` prefix, which is how they were
addressed while the plugin was in testing. They remain for older clients and
will be dropped once none are left.

Plus the unauthenticated management UI page:

```http
GET /v0/resource/plugins/pi-bridge/panel
```

## Response contract

The response shape is negotiated with a request header, so the plugin can
replace the sidecar without touching the client first.

| Request | Response |
| --- | --- |
| no header | contract v1 |
| `X-Pi-Contract: 1` | contract v1 |
| `X-Pi-Contract: 2` | contract v2 |
| `X-Pi-Contract: 99` | contract v2 (newest available) |

**v1** is the `/api/usage` document the sidecar serves, field for field, so an
unmigrated client sees no change.

**v2** adds a `cache` section (`updatedAt`, `stale`, `ttlMs`) and a `client`
section (`keyHint`).

Every response echoes `X-Pi-Contract` and `X-Pi-Contract-Latest`, so a client
can detect that a newer contract exists and warn without parsing the body.

`usage` returns the same `schemaVersion: 1` document shape the Pi plugin
already renders, so the client contract does not change:

```json
{
  "schemaVersion": 1,
  "client": { "id": "abix", "keyHint": "sk-6cae…57a3" },
  "cache": { "updatedAt": "...", "stale": false, "ttlMs": 120000 },
  "accounts": [
    {
      "provider": "claude",
      "account": "d***@gmail.com",
      "supported": true,
      "groups": [
        { "id": "five-hour", "label": "5h Session", "remainingFraction": 0.83 },
        { "id": "seven-day", "label": "7d Weekly", "remainingFraction": 0.82 },
        { "id": "seven-day-fable", "label": "7d Fable", "remainingFraction": 0.91 }
      ]
    }
  ]
}
```

### Anthropic quota windows

Anthropic reports quota in a structured `limits` array where each entry is
self-describing, and the older flat `seven_day_*` fields are now `null`. The
plugin reads the array, so per-model weekly windows appear automatically: Opus
and Sonnet have been joined by **Fable**, which is a sub-cap on the weekly pool
rather than a separate allowance — up to half the weekly limit may go to Fable.

A model-scoped window becomes a group id derived from its name
(`Fable` → `seven-day-fable`), so a model added later needs no code change. The
flat fields are still read when the array is absent. A window the account does
not have carries a null percent and is omitted rather than reported as full.

## Configuration

The plugin works with no configuration: enable it and every CLIProxyAPI API key
can read quota. The management UI shows two everyday settings plus an optional
JSON field for deployment details that rarely change.

```yaml
plugins:
  configs:
    pi-bridge:
      enabled: true
      priority: 3
      store:
        version: 0.7.0
      allow_all_api_keys: true      # off = only ticked keys may read quota
      show_extra_analytics: false   # requires CPA Manager Plus
```

Callers are authorized against the keys CLIProxyAPI itself accepts, read from
the management API. The plugin declares one checkbox per key, named after its
masked form, so turning `allow_all_api_keys` off reveals a tickable list:

```yaml
      allow_all_api_keys: false
      key_sk_dac8_8038: true
      key_sk_213d_a9d5: false
```

The panel renders enum fields as pickers and array fields as raw JSON text, so
a checkbox per key is what makes this selectable by mouse. Only masked forms are
stored; no fingerprints, hashes, or usable key material reach the config file.

### Advanced (optional)

Leave `advanced` empty unless a URL, secret source, or cache TTL must differ:

```yaml
      advanced: '{"usage_ttl_seconds": 180}'
```

| Key | Default |
| --- | --- |
| `management_url` | `http://127.0.0.1:8317/v0/management` |
| `management_key_env` | `MANAGEMENT_PASSWORD` |
| `management_key_file` | unset (takes precedence over the env var) |
| `cpam_url` | `http://cpa-manager-plus:18317` |
| `cpam_admin_key_env` / `cpam_admin_key_file` | unset |
| `usage_ttl_seconds` | `120` |
| `capabilities_ttl_seconds` | `300` |

### Management UI

The plugin adds a **Pi Bridge** menu entry serving a static page that explains
how to configure the plugin and connect the Pi extension.

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

- Callers are authenticated against the API keys CLIProxyAPI itself accepts,
  read from the management API — no key material or hashes live in the plugin's
  own config.
- An unknown key **fails closed**, and all rejection paths return an identical
  `401`, so a caller cannot probe which keys exist.
- There is **no generic management proxy**. The plugin issues only fixed,
  read-only provider quota calls; client input never selects an upstream URL.
- `?refresh=1` is rate limited per client key, using the cache TTL as the floor,
  so it cannot be used to poll the providers faster than they allow.
- Account emails are masked (`d***@gmail.com`) before they leave the plugin.

## Replacing the wellknown sidecar

This plugin supersedes the standalone `pi-cliproxyapi-wellknown` container,
which served the same two documents on its own port behind a second API key.

| | Sidecar | pi-bridge |
| --- | --- | --- |
| Catalogue | `/.well-known/pi` | `…/pi-bridge/well-known` |
| Quota | `/api/usage` | `…/pi-bridge/usage` |
| Credential | separate usage key | the same API key used for model calls |
| Deployment | extra container + reverse-proxy routes | none |

To migrate:

1. Install the plugin (above) and confirm both endpoints answer.
2. Update the Pi extension to 0.4.0 or later.
3. Remove the sidecar's routes from your reverse proxy.
4. Stop the sidecar and drop its usage-key environment variable.

Order is not critical: contract v1 is byte-compatible with the sidecar's
`/api/usage`, so a client that has not migrated yet sees no change, and a client
that has keeps working against a server that still runs the sidecar.

## Install

The plugin is not in the official CLIProxyAPI store yet, so point your instance
at this repository's own registry. Add to `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: "/CLIProxyAPI/plugins"
  store-sources:
    - https://raw.githubusercontent.com/abix5/pi-cliproxyapi-bridge/main/registry.json
```

`pi-bridge` then appears in the management panel under **Plugins → Store**, with
the usual install and update buttons.

To install by hand instead, download the release archive and unpack the shared
object into the plugins directory:

```bash
VERSION=0.7.0
curl -sSLO https://github.com/abix5/pi-cliproxyapi-bridge/releases/download/v$VERSION/pi-bridge_${VERSION}_linux_amd64.zip
curl -sSLO https://github.com/abix5/pi-cliproxyapi-bridge/releases/download/v$VERSION/checksums.txt
sha256sum -c checksums.txt
unzip -j pi-bridge_${VERSION}_linux_amd64.zip -d /path/to/CLIProxyAPI/plugins/
mv /path/to/CLIProxyAPI/plugins/pi-bridge.so \
   /path/to/CLIProxyAPI/plugins/pi-bridge-v$VERSION.so
```

The versioned filename matters — see [Deploy](#deploy-without-restarting-cliproxyapi).
Then enable it:

```yaml
plugins:
  configs:
    pi-bridge:
      enabled: true
      priority: 3
      store:
        version: 0.7.0
      allow_all_api_keys: true
```

Verify with any ordinary API key:

```bash
curl -H "Authorization: Bearer <api key>" \
  https://your-proxy.example.com/v0/resource/plugins/pi-bridge/capabilities
```

## Build

```bash
./build.sh
```

Produces `dist/pi-bridge-v<version>.so` for `linux/amd64`, built against SDK
`v7.2.93`. The SDK version must match the running CLIProxyAPI image or
`cliproxy_plugin_init` will be rejected.

## Deploy without restarting CLIProxyAPI

CLIProxyAPI watches `config.yaml` (and the auth dir) with fsnotify. A reload
loads plugins and re-registers their routes, so a container restart is not
needed — and a restart would interrupt in-flight model traffic.

Two things govern whether a new build is picked up:

- The host only hot-reloads a plugin when its file **path** changes, so
  overwriting a `.so` in place has no effect. Ship each build under its own
  versioned name.
- The reload is gated on the **content hash** of `config.yaml`, not its mtime,
  so `touch` alone does nothing. Change something — in practice, the pinned
  version.

```bash
# 1. copy the new artifact alongside the current one
scp dist/pi-bridge-v0.7.1.so root@host:/root/projects/llm-proxy/CLIProxyAPI/plugins/

# 2. point store.version at it (an actual edit, not a touch)
```

With several versions present the host selects the highest one. Pin a specific
build — or roll back — with `store.version`:

```yaml
plugins:
  configs:
    pi-bridge:
      store:
        version: 0.6.6
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
