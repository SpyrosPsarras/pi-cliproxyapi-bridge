package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginName    = "pi-bridge"
	pluginVersion = "0.6.6"

	routePanel        = "/panel"
	routeCapabilities = "/dev/capabilities"
	routeUsage        = "/dev/usage"
	routeWellKnown    = "/dev/well-known"

	// contractHeader selects the response contract. Absent means v1, which is
	// byte-compatible with the sidecar so an unmigrated client keeps working.
	contractHeader = "X-Pi-Contract"
	contractV1     = 1
	contractLatest = 2

	// documentationURL is surfaced to clients still on v1 so the warning they
	// show can point at the upgrade instructions.
	documentationURL = "https://github.com/abix5/pi-cliproxyapi#pi-bridge"

	refreshInterval = 30 * time.Second
)

// ---- host RPC envelope ----

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml,omitempty"`
	SchemaVersion uint32 `json:"schema_version,omitempty"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

type managementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

// ---- plugin state ----

var (
	stateMu    sync.RWMutex
	config     = defaultConfig()
	configErr  error
	usageCache = newTTLCache()
	capCache   = newTTLCache()
	refreshers = newRateLimiter()
)

func currentConfig() (pluginConfig, error) {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return config, configErr
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		applyLifecycleConfig(request)
		return okEnvelope(pluginRegistration())

	case pluginabi.MethodManagementRegister:
		// Routes are registered as resources: resource requests are not
		// management-authenticated, so the plugin can accept an ordinary
		// CLIProxyAPI API key instead of the CPA Management Key.
		//
		// Only the panel declares a Menu. A menu entry is opened by the
		// management UI with a plain browser navigation, which cannot carry
		// an Authorization header, so a bearer-protected API route listed as
		// a menu item would always render as 401 for a human clicking it.
		return okEnvelope(managementRegistrationResponse{
			Resources: []pluginapi.ResourceRoute{
				{
					Path:        routePanel,
					Menu:        "Pi Bridge",
					Description: "Provider quota for the Pi CLIProxyAPI extension.",
				},
				{
					Path:        routeCapabilities,
					Description: "Capability contract consumed by the Pi CLIProxyAPI plugin.",
				},
				{
					Path:        routeUsage,
					Description: "Cached provider quota for authorized Pi client keys.",
				},
				{
					Path:        routeWellKnown,
					Description: "Model catalogue served to the Pi extension.",
				},
			},
		})

	case pluginabi.MethodManagementHandle:
		return handleResourceRequest(request)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func applyLifecycleConfig(request []byte) {
	var lifecycle lifecycleRequest
	if len(request) > 0 {
		_ = json.Unmarshal(request, &lifecycle)
	}
	parsed, err := parseConfig(lifecycle.ConfigYAML)

	stateMu.Lock()
	defer stateMu.Unlock()
	if err != nil {
		// Fail closed: keep no client keys so every request is refused until
		// the operator fixes the configuration.
		config = defaultConfig()
		configErr = err
		return
	}
	config = parsed
	configErr = nil
	usageCache = newTTLCache()
	capCache = newTTLCache()
}

// keyCheckboxFields declares one checkbox per API key the proxy accepts, so the
// management UI renders a tickable list instead of a free-text field. The panel
// only turns enum fields into pickers and treats array fields as raw JSON, so
// booleans are the one control that gives a real choice.
//
// Registration is synchronous, so this is best effort: an unreachable
// management API yields no checkboxes rather than a failed plugin load.
func keyCheckboxFields(cfg pluginConfig) []pluginapi.ConfigField {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	keys, err := cfg.fetchAPIKeys(ctx)
	if err != nil {
		return nil
	}

	fields := make([]pluginapi.ConfigField, 0, len(keys))
	seen := map[string]bool{}
	for _, key := range keys {
		if key = strings.TrimSpace(key); key == "" {
			continue
		}
		name := keyFieldName(key)
		if seen[name] {
			continue
		}
		seen[name] = true
		fields = append(fields, pluginapi.ConfigField{
			Name:        name,
			Type:        pluginapi.ConfigFieldTypeBoolean,
			Description: "Let " + maskKey(key) + " read provider quota.",
		})
	}
	return fields
}

func pluginRegistration() registration {
	cfg, _ := currentConfig()

	fields := []pluginapi.ConfigField{
		{Name: "allow_all_api_keys", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Let every CLIProxyAPI API key read provider quota. Turn off to pick individual keys below. Default: on."},
	}
	fields = append(fields, keyCheckboxFields(cfg)...)
	fields = append(fields,
		pluginapi.ConfigField{Name: "show_extra_analytics", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Use CPA Manager Plus for curated model prices and extra analytics. Its admin key is read from /CLIProxyAPI/cpam-admin-key or $CPAM_ADMIN_KEY, never from this config."},
		pluginapi.ConfigField{Name: "advanced", Type: pluginapi.ConfigFieldTypeString, Description: `Optional JSON overriding defaults that rarely change, for example {"usage_ttl_seconds":60} or {"model_aliases":{"my-model":"gpt-5.6-sol"}}. Leave empty otherwise.`},
	)

	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "self-hosted",
			GitHubRepository: "https://github.com/abix5/pi-cliproxyapi",
			ConfigFields:     fields,
		},
		Capabilities: registrationCapabilities{ManagementAPI: true},
	}
}

// ---- request handling ----

func handleResourceRequest(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("decode management request: %w", err)
		}
	}

	// The panel carries no secrets and is reached by browser navigation, so it
	// is served before authentication and independently of configuration state.
	if strings.HasSuffix(req.Path, routePanel) {
		return okEnvelope(panelResponse())
	}

	cfg, cfgErr := currentConfig()
	if cfgErr != nil {
		return okEnvelope(jsonResponse(http.StatusServiceUnavailable, map[string]string{"error": "plugin configuration is invalid"}))
	}

	// Authorization is checked against the keys CLIProxyAPI itself accepts.
	// If that list cannot be read the plugin fails closed rather than serving
	// quota to an unverified caller.
	authCtx, cancelAuth := context.WithTimeout(context.Background(), 15*time.Second)
	knownKeys, keysErr := cfg.fetchAPIKeys(authCtx)
	cancelAuth()
	if keysErr != nil {
		return okEnvelope(jsonResponse(http.StatusServiceUnavailable, map[string]string{"error": "cannot verify credentials"}))
	}

	client, ok := authenticate(cfg, knownKeys, req.Headers)
	if !ok {
		return okEnvelope(jsonResponse(http.StatusUnauthorized, map[string]string{"error": "unauthorized"}))
	}

	switch {
	case strings.HasSuffix(req.Path, routeCapabilities):
		return handleCapabilities(cfg, client, contractFrom(req.Headers))
	case strings.HasSuffix(req.Path, routeUsage):
		return handleUsage(cfg, client, req.Query, contractFrom(req.Headers))
	case strings.HasSuffix(req.Path, routeWellKnown):
		return handleWellKnown(cfg, req.Query, contractFrom(req.Headers))
	default:
		return okEnvelope(jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"}))
	}
}

// contractFrom resolves the requested response contract. Anything absent,
// malformed, or older resolves to v1, so a client that knows nothing about
// contracts receives exactly what the sidecar served.
func contractFrom(headers http.Header) int {
	if headers == nil {
		return contractV1
	}
	requested, err := strconv.Atoi(strings.TrimSpace(headers.Get(contractHeader)))
	if err != nil || requested < contractV1 {
		return contractV1
	}
	if requested > contractLatest {
		return contractLatest
	}
	return requested
}

type capabilitiesDocument struct {
	SchemaVersion    int            `json:"schemaVersion"`
	Plugin           string         `json:"plugin"`
	Version          string         `json:"version"`
	Contract         int            `json:"contract"`
	LatestContract   int            `json:"latestContract"`
	Client           clientIdentity `json:"client"`
	Permissions      []string       `json:"permissions"`
	Upstream         upstreamInfo   `json:"upstream"`
	Endpoints        endpointInfo   `json:"endpoints"`
	DocumentationURL string         `json:"documentationUrl"`
}

type upstreamInfo struct {
	CPAVersion string `json:"cpaVersion,omitempty"`
	Panel      string `json:"panel"`
	CPAMMode   string `json:"cpamMode,omitempty"`
}

type endpointInfo struct {
	Usage string `json:"usage"`
}

func handleCapabilities(cfg pluginConfig, client authenticatedClient, contract int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	doc := capabilitiesDocument{
		SchemaVersion:    1,
		Plugin:           pluginName,
		Version:          pluginVersion,
		Contract:         contract,
		LatestContract:   contractLatest,
		Client:           clientIdentity{KeyHint: client.KeyHint},
		Permissions:      client.Permissions,
		Upstream:         upstreamInfo{Panel: "native"},
		Endpoints:        endpointInfo{Usage: routeUsage},
		DocumentationURL: documentationURL,
	}

	if version, err := cfg.cpaVersion(ctx); err == nil {
		doc.Upstream.CPAVersion = version
	}
	if info, ok := cfg.probeCPAM(ctx); ok {
		doc.Upstream.Panel = "cpam"
		doc.Upstream.CPAMMode = info.Mode
	}

	return okEnvelope(jsonResponse(http.StatusOK, doc))
}

func handleUsage(cfg pluginConfig, client authenticatedClient, query url.Values, contract int) ([]byte, error) {
	if !client.allows(permissionUsage) {
		return okEnvelope(jsonResponse(http.StatusForbidden, map[string]string{"error": "usage is not enabled for this key"}))
	}

	// The upstream document is identical for every authorized caller, so one
	// cache entry serves all of them and upstream is polled once per TTL.
	// Contract shaping happens after the cache read.
	const cacheKey = "usage"
	ttl := time.Duration(cfg.advanced.UsageTTLSeconds) * time.Second

	if isTruthy(query.Get("refresh")) {
		// Refresh is rate limited per client so a Pi UI cannot be used to
		// hammer the upstream provider quota endpoints.
		if refreshers.allow(client.KeyHint, refreshInterval) {
			usageCache.invalidate(cacheKey)
		}
	}

	if cached, storedAt, ok := usageCache.get(cacheKey); ok {
		return okEnvelope(rawJSONResponse(http.StatusOK,
			shapeForContract(cached, contract, client, storedAt, ttl), contract))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	doc, err := buildUsage(ctx, cfg)
	if err != nil {
		return okEnvelope(jsonResponse(http.StatusBadGateway, map[string]string{"error": "upstream usage is unavailable"}))
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	storedAt := usageCache.put(cacheKey, encoded, ttl)
	return okEnvelope(rawJSONResponse(http.StatusOK,
		shapeForContract(encoded, contract, client, storedAt, ttl), contract))
}

// shapeForContract renders the cached upstream document for one contract
// version. v1 is the sidecar's shape exactly; v2 adds cache provenance and the
// authenticated client hint.
func shapeForContract(encoded []byte, contract int, client authenticatedClient, storedAt time.Time, ttl time.Duration) []byte {
	if contract < contractLatest {
		return encoded
	}

	var doc usageDocument
	if err := json.Unmarshal(encoded, &doc); err != nil {
		return encoded
	}
	doc.Client = &clientIdentity{KeyHint: client.KeyHint}
	doc.Cache = &cacheInfo{
		UpdatedAt: storedAt.UTC().Format(time.RFC3339),
		Stale:     false,
		TTLMs:     int(ttl.Milliseconds()),
	}

	patched, err := json.Marshal(doc)
	if err != nil {
		return encoded
	}
	return patched
}

// handleWellKnown serves the model catalogue. Like usage, the document is
// identical for every authorized caller, so one cache entry serves all.
func handleWellKnown(cfg pluginConfig, query url.Values, contract int) ([]byte, error) {
	const cacheKey = "well-known"
	ttl := time.Duration(cfg.advanced.CapabilitiesTTLSeconds) * time.Second

	if isTruthy(query.Get("refresh")) {
		usageCache.invalidate(cacheKey)
	}
	if cached, _, ok := usageCache.get(cacheKey); ok {
		return okEnvelope(rawJSONResponse(http.StatusOK, shapeDiscovery(cached, contract), contract))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	doc, err := buildDiscovery(ctx, cfg, cfg.publicBaseURL())
	if err != nil {
		return okEnvelope(jsonResponse(http.StatusBadGateway, map[string]string{"error": "upstream model list is unavailable"}))
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	usageCache.put(cacheKey, encoded, ttl)
	return okEnvelope(rawJSONResponse(http.StatusOK, shapeDiscovery(encoded, contract), contract))
}

// shapeDiscovery strips the v2-only sections for v1 callers, so the document
// stays byte-compatible with what the sidecar served.
func shapeDiscovery(encoded []byte, contract int) []byte {
	if contract >= contractLatest {
		return encoded
	}
	var doc discoveryDocument
	if err := json.Unmarshal(encoded, &doc); err != nil {
		return encoded
	}
	doc.Upstream = nil
	doc.Catalog = nil
	doc.Providers = nil
	for i := range doc.CustomModelPool {
		doc.CustomModelPool[i].FromCatalog = false
		doc.CustomModelPool[i].MetadataSource = ""
	}
	patched, err := json.Marshal(doc)
	if err != nil {
		return encoded
	}
	return patched
}

// withCacheInfo stamps cache provenance onto an already-encoded document.
func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// ---- response helpers ----

func jsonResponse(status int, payload any) pluginapi.ManagementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
		status = http.StatusInternalServerError
	}
	return rawJSONResponse(status, body, contractV1)
}

// rawJSONResponse echoes the served contract so a client can detect which shape
// it received without parsing the body.
func rawJSONResponse(status int, body []byte, contract int) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":         []string{"application/json; charset=utf-8"},
			"Cache-Control":        []string{"no-store"},
			contractHeader:         []string{strconv.Itoa(contract)},
			"X-Pi-Contract-Latest": []string{strconv.Itoa(contractLatest)},
		},
		Body: body,
	}
}

func okEnvelope(result any) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
