package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func registeredResources(t *testing.T) managementRegistrationResponse {
	t.Helper()
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("registration failed: %+v", env.Error)
	}
	var resp managementRegistrationResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	return resp
}

// The panel opens a menu entry with a plain browser navigation, which cannot
// send an Authorization header. Only the HTML panel may carry a Menu; the
// bearer-protected JSON endpoints must not, or clicking them in the UI would
// always render a 401.
func TestOnlyPanelDeclaresMenu(t *testing.T) {
	resp := registeredResources(t)

	if len(resp.Resources) == 0 {
		t.Fatal("expected resource routes to be registered")
	}
	menus := 0
	for _, route := range resp.Resources {
		if route.Menu == "" {
			continue
		}
		menus++
		if route.Path != routePanel {
			t.Fatalf("API route %s must not declare a menu entry, got %q", route.Path, route.Menu)
		}
	}
	if menus != 1 {
		t.Fatalf("expected exactly one menu entry, got %d", menus)
	}
}

// Management routes would be gated by the CPA Management Key, defeating the
// single-ordinary-key design.
func TestNoManagementRoutesAreRegistered(t *testing.T) {
	if resp := registeredResources(t); len(resp.Routes) != 0 {
		t.Fatalf("expected no management routes, got %d", len(resp.Routes))
	}
}

func TestRegisteredRoutePaths(t *testing.T) {
	resp := registeredResources(t)
	found := map[string]bool{}
	for _, route := range resp.Resources {
		found[route.Path] = true
	}
	for _, want := range []string{routePanel, routeCapabilities, routeUsage} {
		if !found[want] {
			t.Fatalf("expected route %s to be registered", want)
		}
	}
}
