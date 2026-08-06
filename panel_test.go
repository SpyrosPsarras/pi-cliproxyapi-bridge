package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func handleRequest(t *testing.T, req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	t.Helper()
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	raw, err := handleMethod(pluginabi.MethodManagementHandle, encoded)
	if err != nil {
		t.Fatalf("management.handle: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("handler failed: %+v", env.Error)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// The panel is reached by browser navigation, which cannot send an
// Authorization header, so it must render without credentials.
func TestPanelServedWithoutAuthorization(t *testing.T) {
	resp := handleRequest(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/pi-bridge" + routePanel,
	})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("panel status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Headers.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("panel content type = %q", ct)
	}
	if !strings.Contains(string(resp.Body), "Pi Bridge") {
		t.Fatal("panel body does not look like the expected page")
	}
}

// The page is publicly reachable, so it must carry no credentials, no account
// data, and no interactive key entry.
func TestPanelBodyCarriesNoSecrets(t *testing.T) {
	resp := handleRequest(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/pi-bridge" + routePanel,
	})

	body := string(resp.Body)
	if regexp.MustCompile(`sk-[A-Za-z0-9]{12,}`).MatchString(body) {
		t.Fatal("panel body embeds something shaped like an API key")
	}
	for _, forbidden := range []string{"MANAGEMENT_PASSWORD", "@gmail", "@icloud", "authIndex", "<script"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("panel body contains %q", forbidden)
		}
	}
	if resp.Headers.Get("Content-Security-Policy") == "" {
		t.Fatal("panel must send a content security policy")
	}
}

// The page exists to explain setup, so the essential instructions must be there.
func TestPanelExplainsSetup(t *testing.T) {
	resp := handleRequest(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/pi-bridge" + routePanel,
	})

	body := string(resp.Body)
	for _, expected := range []string{"pi install npm:pi-cliproxyapi", routeUsage, "allow_all_api_keys"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("panel should document %q", expected)
		}
	}
}

// Data endpoints must never be served without verifying the caller. In this
// test no management key is configured, so the plugin cannot check the key and
// must fail closed rather than fall back to serving quota.
func TestUsageIsRefusedWhenCredentialsCannotBeVerified(t *testing.T) {
	resp := handleRequest(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/pi-bridge" + routeUsage,
	})

	if resp.StatusCode == http.StatusOK {
		t.Fatal("usage must not be served when the caller cannot be verified")
	}
	if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("usage status = %d, want 401 or 503", resp.StatusCode)
	}
}

// The /dev/ prefix was only ever a testing convenience. Both spellings must
// resolve so a client can be updated after the server, not in lockstep.
func TestLegacyDevPathsStillResolve(t *testing.T) {
	base := "/v0/resource/plugins/pi-bridge"
	for _, path := range []string{
		base + routeUsage,
		base + legacyRoutePrefix + routeUsage,
		base + routeWellKnown,
		base + legacyRoutePrefix + routeWellKnown,
	} {
		resp := handleRequest(t, pluginapi.ManagementRequest{Method: http.MethodGet, Path: path})
		// Unauthenticated here, so anything except "not found" proves the route
		// was recognised.
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("route %s is not recognised", path)
		}
	}
}
