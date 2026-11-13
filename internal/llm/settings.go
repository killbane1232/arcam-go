package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
)

type connection struct {
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	DefaultModel   string `json:"default_model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	JSONMode       bool   `json:"json_mode"`
}
type ConnectionView struct {
	Provider         string `json:"provider"`
	BaseURL          string `json:"base_url"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	DefaultModel     string `json:"default_model"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	JSONMode         bool   `json:"json_mode"`
	Source           string `json:"source"`
}
type ConnectionUpdate struct {
	BaseURL        *string `json:"base_url,omitempty"`
	APIKey         *string `json:"api_key,omitempty"`
	ClearAPIKey    bool    `json:"clear_api_key,omitempty"`
	DefaultModel   *string `json:"default_model,omitempty"`
	TimeoutSeconds *int    `json:"timeout_seconds,omitempty"`
	JSONMode       *bool   `json:"json_mode,omitempty"`
}

func validProvider(provider string) error {
	if provider != "openai" && provider != "ollama" {
		return fmt.Errorf("provider must be openai or ollama")
	}
	return nil
}
func suffix(provider string) string {
	if provider == "ollama" {
		return "/api/chat"
	}
	return "/chat/completions"
}
func (c *Client) loadConnection(provider string) (connection, string, error) {
	if err := validProvider(provider); err != nil {
		return connection{}, "", err
	}
	var saved connection
	if err := c.Store.Get("settings", "llm-"+provider, &saved); err == nil {
		return saved, "saved", nil
	} else if !os.IsNotExist(err) {
		return saved, "", err
	}
	endpoint, ok := c.Providers[provider]
	if !ok {
		return saved, "", fmt.Errorf("provider not configured")
	}
	timeout := int(endpoint.Timeout / time.Second)
	if timeout == 0 {
		timeout = 60
	}
	return connection{BaseURL: strings.TrimSuffix(endpoint.URL, suffix(provider)), APIKey: endpoint.Key, DefaultModel: endpoint.DefaultModel, TimeoutSeconds: timeout, JSONMode: endpoint.JSONMode}, "environment", nil
}
func view(provider string, v connection, source string) ConnectionView {
	return ConnectionView{Provider: provider, BaseURL: v.BaseURL, APIKeyConfigured: v.APIKey != "", DefaultModel: v.DefaultModel, TimeoutSeconds: v.TimeoutSeconds, JSONMode: v.JSONMode, Source: source}
}
func (c *Client) Settings() ([]ConnectionView, error) {
	out := []ConnectionView{}
	for _, provider := range []string{"openai", "ollama"} {
		v, source, err := c.loadConnection(provider)
		if err != nil {
			return nil, err
		}
		out = append(out, view(provider, v, source))
	}
	return out, nil
}
func validateConnection(provider string, v *connection) error {
	v.BaseURL = strings.TrimRight(strings.TrimSpace(v.BaseURL), "/")
	u, err := url.Parse(v.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("base_url must be an HTTP(S) base URL without credentials, query or fragment")
	}
	if strings.HasSuffix(u.Path, suffix(provider)) || strings.HasSuffix(u.Path, "/api/tags") || strings.HasSuffix(u.Path, "/models") || strings.HasSuffix(u.Path, "/systemone") {
		return fmt.Errorf("enter the API base URL, not a chat or models endpoint")
	}
	if v.TimeoutSeconds < 1 || v.TimeoutSeconds > 300 {
		return fmt.Errorf("timeout_seconds must be 1..300")
	}
	v.DefaultModel = strings.TrimSpace(v.DefaultModel)
	if len(v.DefaultModel) > 200 || strings.IndexFunc(v.DefaultModel, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid default_model")
	}
	if len(v.APIKey) > 8192 || strings.IndexFunc(v.APIKey, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid API key")
	}
	if provider == "ollama" && v.JSONMode {
		return fmt.Errorf("json_mode is only available for OpenAI-compatible endpoints")
	}
	return nil
}
func (c *Client) candidate(provider string, patch ConnectionUpdate) (connection, error) {
	v, _, err := c.loadConnection(provider)
	if err != nil {
		return v, err
	}
	oldURL := v.BaseURL
	if patch.BaseURL != nil {
		v.BaseURL = *patch.BaseURL
	}
	if patch.DefaultModel != nil {
		v.DefaultModel = *patch.DefaultModel
	}
	if patch.TimeoutSeconds != nil {
		v.TimeoutSeconds = *patch.TimeoutSeconds
	}
	if patch.JSONMode != nil {
		v.JSONMode = *patch.JSONMode
	}
	if patch.ClearAPIKey && patch.APIKey != nil && strings.TrimSpace(*patch.APIKey) != "" {
		return v, fmt.Errorf("cannot replace and clear API key at the same time")
	}
	replacement := patch.APIKey != nil && strings.TrimSpace(*patch.APIKey) != ""
	if replacement {
		v.APIKey = strings.TrimSpace(*patch.APIKey)
	}
	if patch.ClearAPIKey {
		v.APIKey = ""
	}
	if err = validateConnection(provider, &v); err != nil {
		return v, err
	}
	// A destination change must not silently forward the old credential elsewhere.
	if v.BaseURL != strings.TrimRight(oldURL, "/") && v.APIKey != "" && !replacement && !patch.ClearAPIKey {
		return v, fmt.Errorf("when changing base_url, enter a new API key or select clear_api_key")
	}
	return v, nil
}
func (c *Client) SaveSettings(provider string, patch ConnectionUpdate) (ConnectionView, error) {
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	v, err := c.candidate(provider, patch)
	if err != nil {
		return ConnectionView{}, err
	}
	if err = c.Store.Put("settings", "llm-"+provider, v); err != nil {
		return ConnectionView{}, err
	}
	return view(provider, v, "saved"), nil
}
func (c *Client) endpoint(provider string) (Endpoint, error) {
	v, source, err := c.loadConnection(provider)
	if err != nil {
		return Endpoint{}, err
	}
	if err = validateConnection(provider, &v); err != nil {
		return Endpoint{}, err
	}
	endpoint := Endpoint{URL: v.BaseURL + suffix(provider), Key: v.APIKey, JSONMode: v.JSONMode, DefaultModel: v.DefaultModel, Timeout: time.Duration(v.TimeoutSeconds) * time.Second}
	// Preserve injectable complete endpoints used by the adapter's tests.
	if source == "environment" {
		endpoint.URL = c.Providers[provider].URL
	}
	return endpoint, nil
}

type ConnectionCheck struct {
	Provider              string   `json:"provider"`
	Models                []string `json:"models"`
	LatencyMS             int64    `json:"latency_ms"`
	DefaultModelAvailable bool     `json:"default_model_available"`
}

// CheckConnection only lists models; it performs no paid inference or downloads.
func (c *Client) CheckConnection(ctx context.Context, provider string, patch ConnectionUpdate) (ConnectionCheck, error) {
	out := ConnectionCheck{Provider: provider, Models: []string{}}
	v, err := c.candidate(provider, patch)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(v.TimeoutSeconds)*time.Second)
	defer cancel()
	path := "/models"
	if provider == "ollama" {
		path = "/api/tags"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", v.BaseURL+path, nil)
	if err != nil {
		return out, fmt.Errorf("invalid provider request")
	}
	req.Header.Set("Accept", "application/json")
	if v.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+v.APIKey)
	}
	// Never follow redirects with credentials, including to another path/host.
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	start := time.Now()
	res, err := client.Do(req)
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return out, fmt.Errorf("provider connection failed; check URL, network and timeout")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return out, fmt.Errorf("provider HTTP %d while listing models", res.StatusCode)
	}
	var wire struct {
		Data *[]struct {
			ID string `json:"id"`
		} `json:"data"`
		Models *[]struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&wire); err != nil {
		return out, fmt.Errorf("provider returned an invalid models response")
	}
	seen := map[string]bool{}
	if provider == "openai" {
		if wire.Data == nil {
			return out, fmt.Errorf("provider response has no data array")
		}
		for _, m := range *wire.Data {
			if m.ID != "" {
				seen[m.ID] = true
			}
		}
	} else {
		if wire.Models == nil {
			return out, fmt.Errorf("provider response has no models array")
		}
		for _, m := range *wire.Models {
			if m.Name != "" {
				seen[m.Name] = true
			}
		}
	}
	for model := range seen {
		out.Models = append(out.Models, model)
	}
	sort.Strings(out.Models)
	out.DefaultModelAvailable = seen[v.DefaultModel]
	return out, nil
}
