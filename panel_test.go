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

// The route is publicly reachable, so the page itself must never embed
// credentials or account data.
func TestPanelBodyCarriesNoSecrets(t *testing.T) {
	resp := handleRequest(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/pi-bridge" + routePanel,
	})

	body := string(resp.Body)
	// A realistic key, not the input placeholder, is what must never appear.
	if regexp.MustCompile(`sk-[A-Za-z0-9]{12,}`).MatchString(body) {
		t.Fatal("panel body embeds something shaped like an API key")
	}
	for _, forbidden := range []string{"MANAGEMENT_PASSWORD", "@gmail", "@icloud", "authIndex"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("panel body leaks %q", forbidden)
		}
	}
	if resp.Headers.Get("Content-Security-Policy") == "" {
		t.Fatal("panel must send a content security policy")
	}
}

// Data endpoints must stay behind the allow-list even though the panel is open.
func TestUsageStillRequiresAuthorization(t *testing.T) {
	resp := handleRequest(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/pi-bridge" + routeUsage,
	})

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("usage status = %d, want 401", resp.StatusCode)
	}
}
