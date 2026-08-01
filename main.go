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
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginName    = "pi-bridge"
	pluginVersion = "0.1.0"

	routeCapabilities = "/dev/capabilities"
	routeUsage        = "/dev/usage"

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
		// No Menu is declared. A menu entry is opened by the panel with a
		// plain browser navigation, which cannot carry an Authorization
		// header, so an API endpoint listed as a menu item would always
		// render as 401 for a human clicking it.
		return okEnvelope(managementRegistrationResponse{
			Resources: []pluginapi.ResourceRoute{
				{
					Path:        routeCapabilities,
					Description: "Capability contract consumed by the Pi CLIProxyAPI plugin.",
				},
				{
					Path:        routeUsage,
					Description: "Cached provider quota for authorized Pi client keys.",
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

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "self-hosted",
			GitHubRepository: "https://github.com/abix5/pi-cliproxyapi",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "client-auth", Type: pluginapi.ConfigFieldTypeObject, Description: "Allow-listed Pi client key fingerprints and their permissions."},
				{Name: "management", Type: pluginapi.ConfigFieldTypeObject, Description: "CPA Management API base URL and the env/file holding its key."},
				{Name: "cpam", Type: pluginapi.ConfigFieldTypeObject, Description: "Optional CPA Manager Plus base URL and admin key source."},
				{Name: "cache", Type: pluginapi.ConfigFieldTypeObject, Description: "TTL for cached quota and capability documents."},
			},
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

	cfg, cfgErr := currentConfig()
	if cfgErr != nil {
		return okEnvelope(jsonResponse(http.StatusServiceUnavailable, map[string]string{"error": "plugin configuration is invalid"}))
	}

	client, hint, ok := authenticate(cfg, req.Headers)
	if !ok {
		return okEnvelope(jsonResponse(http.StatusUnauthorized, map[string]string{"error": "unauthorized"}))
	}

	switch {
	case strings.HasSuffix(req.Path, routeCapabilities):
		return handleCapabilities(cfg, client, hint)
	case strings.HasSuffix(req.Path, routeUsage):
		return handleUsage(cfg, client, hint, req.Query)
	default:
		return okEnvelope(jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"}))
	}
}

type capabilitiesDocument struct {
	SchemaVersion int            `json:"schemaVersion"`
	Plugin        string         `json:"plugin"`
	Version       string         `json:"version"`
	Client        clientIdentity `json:"client"`
	Permissions   []string       `json:"permissions"`
	Upstream      upstreamInfo   `json:"upstream"`
	Endpoints     endpointInfo   `json:"endpoints"`
}

type upstreamInfo struct {
	CPAVersion string `json:"cpaVersion,omitempty"`
	Panel      string `json:"panel"`
	CPAMMode   string `json:"cpamMode,omitempty"`
}

type endpointInfo struct {
	Usage string `json:"usage"`
}

func handleCapabilities(cfg pluginConfig, client clientKey, hint string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	doc := capabilitiesDocument{
		SchemaVersion: 1,
		Plugin:        pluginName,
		Version:       pluginVersion,
		Client:        clientIdentity{ID: client.ID, KeyHint: hint},
		Permissions:   client.Permissions,
		Upstream:      upstreamInfo{Panel: "native"},
		Endpoints:     endpointInfo{Usage: routeUsage},
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

func handleUsage(cfg pluginConfig, client clientKey, hint string, query url.Values) ([]byte, error) {
	if !client.allows(permissionUsage) {
		return okEnvelope(jsonResponse(http.StatusForbidden, map[string]string{"error": "usage is not enabled for this key"}))
	}

	cacheKey := "usage:" + client.ID
	ttl := time.Duration(cfg.Cache.UsageTTLSeconds) * time.Second

	if isTruthy(query.Get("refresh")) {
		// Refresh is rate limited per client so a Pi UI cannot be used to
		// hammer the upstream provider quota endpoints.
		if refreshers.allow(cacheKey, refreshInterval) {
			usageCache.invalidate(cacheKey)
		}
	}

	if cached, storedAt, ok := usageCache.get(cacheKey); ok {
		return okEnvelope(rawJSONResponse(http.StatusOK, withCacheInfo(cached, storedAt, ttl, false)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	doc, err := buildUsage(ctx, cfg, client, hint)
	if err != nil {
		return okEnvelope(jsonResponse(http.StatusBadGateway, map[string]string{"error": "upstream usage is unavailable"}))
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	storedAt := usageCache.put(cacheKey, encoded, ttl)
	return okEnvelope(rawJSONResponse(http.StatusOK, withCacheInfo(encoded, storedAt, ttl, false)))
}

// withCacheInfo stamps cache provenance onto an already-encoded document.
func withCacheInfo(encoded []byte, storedAt time.Time, ttl time.Duration, stale bool) []byte {
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		return encoded
	}
	doc["cache"] = cacheInfo{
		UpdatedAt: storedAt.UTC().Format(time.RFC3339),
		Stale:     stale,
		TTLMs:     int(ttl.Milliseconds()),
	}
	patched, err := json.Marshal(doc)
	if err != nil {
		return encoded
	}
	return patched
}

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
	return rawJSONResponse(status, body)
}

func rawJSONResponse(status int, body []byte) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
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
