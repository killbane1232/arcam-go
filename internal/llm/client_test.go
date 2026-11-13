package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"arcam/internal/core"
	"arcam/internal/store"
)

func TestParseVerdictStrict(t *testing.T) {
	for _, s := range []string{`{}`, `{"enter_long":2,"exit_long":0,"enter_short":0,"exit_short":0,"reason":"x"}`, `{"enter_long":0,"exit_long":0,"enter_short":0,"exit_short":0,"reason":"x","order":"buy"}`, `{"enter_long":0,"exit_long":0,"enter_short":0,"exit_short":0,"reason":"x"} {}`} {
		if _, e := ParseVerdict(s); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestProvidersCacheAndBudget(t *testing.T) {
	for _, provider := range []string{"openai", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Error(e)
				}
				if body["stream"] != false {
					t.Error("streaming enabled")
				}
				verdict := `{"enter_long":0.9,"exit_long":0.1,"enter_short":0.2,"exit_short":0.3,"reason":"trend"}`
				var out any
				if provider == "openai" {
					if body["response_format"] == nil {
						t.Error("schema missing")
					}
					out = map[string]any{"model": "fixed", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": verdict}}}}
				} else {
					if body["format"] == nil {
						t.Error("schema missing")
					}
					out = map[string]any{"model": "fixed", "done": true, "message": map[string]string{"content": verdict}}
				}
				json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			st, e := store.Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer st.Close()
			c := &Client{Store: st, HTTP: server.Client(), Providers: map[string]Endpoint{provider: {URL: server.URL}}}
			fn := c.Enricher(core.LLMConfig{Provider: provider, Model: "test", Lookback: 2}, 1)
			bars := core.DemoCandles(100)[:5]
			features := map[string]float64{"close": 100}
			a, e := fn(context.Background(), bars, features, "BTCUSDT", "")
			if e != nil || a.Cached || a.ResponseMS <= 0 {
				t.Fatalf("first %v %v", a, e)
			}
			a, e = fn(context.Background(), bars, features, "BTCUSDT", "")
			if e != nil || !a.Cached || a.ResponseMS != 0 || calls != 1 {
				t.Fatalf("cached %v %v calls %d", a, e, calls)
			}
			_, e = fn(context.Background(), bars, features, "ETHUSDT", "")
			if e == nil || calls != 1 {
				t.Fatal("budget not enforced")
			}
		})
	}
}
