package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arcam/internal/core"
	"arcam/internal/store"
)

func ptr[T any](v T) *T { return &v }
func settingsClient(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	s, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return New(s), dir
}
func TestSettingsPersistenceAndSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-secret")
	t.Setenv("OPENAI_MODEL", "environment-model")
	c, dir := settingsClient(t)
	all, e := c.Settings()
	if e != nil || all[0].Source != "environment" || !all[0].APIKeyConfigured {
		t.Fatal(all, e)
	}
	v, e := c.SaveSettings("openai", ConnectionUpdate{DefaultModel: ptr("fixed-model"), TimeoutSeconds: ptr(23)})
	if e != nil || v.Source != "saved" {
		t.Fatal(v, e)
	}
	encoded, _ := json.Marshal(v)
	if strings.Contains(string(encoded), "environment-secret") || strings.Contains(string(encoded), `"api_key":`) {
		t.Fatal("secret leaked")
	}
	fi, e := os.Stat(filepath.Join(dir, "settings", "llm-openai.json"))
	if e != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("settings permissions", e)
	}
	c.Store.Close()
	st, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	c = New(st)
	ep, e := c.endpoint("openai")
	if e != nil || ep.Key != "environment-secret" || ep.DefaultModel != "fixed-model" || ep.Timeout != 23*time.Second {
		t.Fatalf("reopen: %+v %v", ep, e)
	}
	if _, e = c.SaveSettings("openai", ConnectionUpdate{APIKey: ptr("")}); e != nil {
		t.Fatal(e)
	}
	if _, e = c.SaveSettings("openai", ConnectionUpdate{BaseURL: ptr("http://localhost:1234/v1")}); e == nil {
		t.Fatal("old credential forwarded to new destination")
	}
	if _, e = c.SaveSettings("openai", ConnectionUpdate{BaseURL: ptr("http://localhost:1234/v1"), ClearAPIKey: true}); e != nil {
		t.Fatal(e)
	}
	ep, e = c.endpoint("openai")
	if e != nil || ep.Key != "" || ep.URL != "http://localhost:1234/v1/chat/completions" {
		t.Fatal(ep, e)
	}
	for _, patch := range []ConnectionUpdate{{BaseURL: ptr("file:///tmp/key")}, {BaseURL: ptr("https://user:secret@example.org/v1")}, {BaseURL: ptr("https://example.org/v1?key=secret")}, {BaseURL: ptr("https://example.org/v1/chat/completions")}, {TimeoutSeconds: ptr(0)}, {TimeoutSeconds: ptr(301)}, {APIKey: ptr("secret\nheader")}, {APIKey: ptr("new"), ClearAPIKey: true}} {
		if _, e = c.SaveSettings("openai", patch); e == nil {
			t.Fatalf("accepted invalid settings %+v", patch)
		}
	}
	if _, e = c.SaveSettings("unknown", ConnectionUpdate{}); e == nil {
		t.Fatal("unknown provider accepted")
	}
}
func TestConnectionCheckAndFrozenEnricher(t *testing.T) {
	for _, provider := range []string{"openai", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			c, _ := settingsClient(t)
			requests := make(chan string, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
				if r.Method == "GET" {
					if provider == "openai" {
						w.Write([]byte(`{"data":[{"id":"fixed"},{"id":"another"},{"id":"fixed"}]}`))
					} else {
						w.Write([]byte(`{"models":[{"name":"fixed"},{"name":"another"}]}`))
					}
					return
				}
				var payload struct {
					Model string `json:"model"`
				}
				json.NewDecoder(r.Body).Decode(&payload)
				if payload.Model != "strategy-model" {
					t.Errorf("model overwritten: %s", payload.Model)
				}
				verdict := `{"enter_long":0.9,"exit_long":0,"enter_short":0,"exit_short":0.1,"reason":"test"}`
				if provider == "openai" {
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": verdict}}}})
				} else {
					json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": verdict}})
				}
			}))
			defer server.Close()
			patch := ConnectionUpdate{BaseURL: ptr(server.URL), APIKey: ptr("old-key"), DefaultModel: ptr("fixed")}
			check, e := c.CheckConnection(context.Background(), provider, patch)
			if e != nil || len(check.Models) != 2 || !check.DefaultModelAvailable {
				t.Fatal(check, e)
			}
			path := "/models"
			if provider == "ollama" {
				path = "/api/tags"
			}
			if got := <-requests; got != "GET "+path+" Bearer old-key" {
				t.Fatal(got)
			}
			v, _, _ := c.loadConnection(provider)
			if v.BaseURL == server.URL {
				t.Fatal("check saved draft")
			}
			if _, e = c.SaveSettings(provider, patch); e != nil {
				t.Fatal(e)
			}
			config := core.LLMConfig{Provider: provider, Model: "strategy-model", Lookback: 2}
			frozen := c.Enricher(config, 2)
			if _, e = c.SaveSettings(provider, ConnectionUpdate{APIKey: ptr("new-key")}); e != nil {
				t.Fatal(e)
			}
			bars := core.DemoCandles(100)[:3]
			if _, e = frozen(context.Background(), bars, nil, "BTCUSDT", ""); e != nil {
				t.Fatal(e)
			}
			if got := <-requests; got != "POST "+suffix(provider)+" Bearer old-key" {
				t.Fatalf("connection changed mid-run: %s", got)
			}
			if _, e = c.Enricher(config, 1)(context.Background(), bars, nil, "ETHUSDT", ""); e != nil {
				t.Fatal(e)
			}
			if got := <-requests; got != "POST "+suffix(provider)+" Bearer new-key" {
				t.Fatal(got)
			}
		})
	}
}
func TestConnectionErrorsAndRedirects(t *testing.T) {
	c, _ := settingsClient(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect with credentials") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer server.Close()
	_, e := c.CheckConnection(context.Background(), "openai", ConnectionUpdate{BaseURL: ptr(server.URL), APIKey: ptr("secret-key")})
	if e == nil || strings.Contains(e.Error(), "secret-key") {
		t.Fatal("unsafe error", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.CheckConnection(ctx, "openai", ConnectionUpdate{BaseURL: ptr(server.URL)}); e == nil {
		t.Fatal("ignored cancellation")
	}
}
