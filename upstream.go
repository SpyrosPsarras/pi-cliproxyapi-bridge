package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// getJSON performs an authenticated read-only upstream GET.
// Errors never include the response body so upstream payloads cannot leak
// into client-visible messages or logs.
func getJSON(ctx context.Context, url, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode upstream response: %w", err)
	}
	return nil
}

// postJSON performs an authenticated upstream POST used only for the fixed
// management api-call passthrough.
func postJSON(ctx context.Context, url, bearer string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode upstream response: %w", err)
	}
	return nil
}

// ---- CPA management ----

// fetchAPIKeys reads the API keys CLIProxyAPI itself accepts. The plugin
// authorizes callers against this list so operators never copy key material
// or fingerprints into the plugin configuration.
func (c pluginConfig) fetchAPIKeys(ctx context.Context) ([]string, error) {
	key := c.managementKey()
	if key == "" {
		return nil, fmt.Errorf("management key is not configured")
	}
	var resp struct {
		APIKeys []string `json:"api-keys"`
	}
	if err := getJSON(ctx, c.advanced.ManagementURL+"/api-keys", key, &resp); err != nil {
		return nil, err
	}
	return resp.APIKeys, nil
}

type authFilesResponse struct {
	Files []authFile `json:"files"`
}

type authFile struct {
	AuthIndex     string `json:"auth_index"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Type          string `json:"type"`
	Label         string `json:"label"`
	Email         string `json:"email"`
	Account       string `json:"account"`
	ProjectID     string `json:"project_id"`
	Status        string `json:"status"`
	StatusMessage string `json:"status_message"`
	Disabled      bool   `json:"disabled"`
	Unavailable   bool   `json:"unavailable"`
	Success       int    `json:"success"`
	Failed        int    `json:"failed"`
	LastRefresh   string `json:"last_refresh"`
	UpdatedAt     string `json:"updated_at"`
}

func (c pluginConfig) fetchAuthFiles(ctx context.Context) ([]authFile, error) {
	key := c.managementKey()
	if key == "" {
		return nil, fmt.Errorf("management key is not configured")
	}
	var resp authFilesResponse
	if err := getJSON(ctx, c.advanced.ManagementURL+"/auth-files", key, &resp); err != nil {
		return nil, err
	}
	return resp.Files, nil
}

// apiCallRequest is the fixed management passthrough shape. The plugin only
// ever issues provider quota reads through it; it is never driven by client input.
type apiCallRequest struct {
	AuthIndex string            `json:"auth_index,omitempty"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header,omitempty"`
	Data      string            `json:"data,omitempty"`
}

type apiCallResponse struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

func (c pluginConfig) managementAPICall(ctx context.Context, req apiCallRequest) (apiCallResponse, error) {
	key := c.managementKey()
	if key == "" {
		return apiCallResponse{}, fmt.Errorf("management key is not configured")
	}
	var resp apiCallResponse
	if err := postJSON(ctx, c.advanced.ManagementURL+"/api-call", key, req, &resp); err != nil {
		return apiCallResponse{}, err
	}
	return resp, nil
}

// cpaVersion reads the build version exposed on management responses.
func (c pluginConfig) cpaVersion(ctx context.Context) (string, error) {
	key := c.managementKey()
	if key == "" {
		return "", fmt.Errorf("management key is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.advanced.ManagementURL+"/latest-version", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
	return resp.Header.Get("X-CPA-VERSION"), nil
}

// ---- CPAM ----

type cpamInfo struct {
	Service    string `json:"service"`
	Mode       string `json:"mode"`
	Configured bool   `json:"configured"`
}

// probeCPAM detects a CPA Manager Plus deployment through its unauthenticated
// mode-detection endpoint.
func (c pluginConfig) probeCPAM(ctx context.Context) (cpamInfo, bool) {
	if !c.ShowExtraAnalytics || c.advanced.CPAMURL == "" {
		return cpamInfo{}, false
	}
	var info cpamInfo
	if err := getJSON(ctx, c.advanced.CPAMURL+"/usage-service/info", "", &info); err != nil {
		return cpamInfo{}, false
	}
	if info.Service != "cpa-manager-plus" {
		return cpamInfo{}, false
	}
	return info, true
}
