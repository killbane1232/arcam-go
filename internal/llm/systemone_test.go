package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arcam/internal/core"
)

func TestSystemOneRequestScoresCacheAndBudget(t *testing.T) {
	for _, provider := range []string{"ollama", "openai"} {
		t.Run(provider, func(t *testing.T) {
			requests := make(chan bool, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- true
				if r.URL.Path != "/v1/systemone" || r.Method != "POST" {
					t.Errorf("wrong endpoint: %s %s", r.Method, r.URL.Path)
				}
				var body map[string]json.RawMessage
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Error(e)
				}
				for _, key := range []string{"messages", "stream", "response_format", "format"} {
					if _, ok := body[key]; ok {
						t.Errorf("chat field sent: %s", key)
					}
				}
				var model string
				json.Unmarshal(body["model"], &model)
				if model != "d1:2b" {
					t.Error(model)
				}
				var state struct {
					Candles    []core.Candle      `json:"candles"`
					Indicators map[string]float64 `json:"indicators"`
					Position   string             `json:"position"`
				}
				json.Unmarshal(body["state"], &state)
				if len(state.Candles) != 2 || state.Indicators["fast.value"] != 101 || state.Position != "long" {
					t.Errorf("state lost: %+v", state)
				}
				var questions map[string]struct {
					Type         string            `json:"type"`
					Instructions string            `json:"instructions"`
					Criteria     map[string]string `json:"criteria"`
				}
				json.Unmarshal(body["questions"], &questions)
				if len(questions) != 4 {
					t.Error("wrong question count")
				}
				for _, key := range []string{"enter_long", "exit_long", "enter_short", "exit_short"} {
					q := questions[key]
					if q.Type != "noul" || q.Criteria["true"] == "" || q.Criteria["false"] == "" || !strings.Contains(q.Instructions, "CUSTOM RULE") {
						t.Errorf("question missing %s: %+v", key, q)
					}
				}
				w.Write([]byte(`{"model":"d1:2b","answers":{"enter_long":{"type":"noul","noul":0.91},"exit_long":{"type":"noul","noul":0.12},"enter_short":{"type":"noul","noul":0.03},"exit_short":{"type":"noul","noul":0.82}}}`))
			}))
			defer server.Close()
			c, _ := settingsClient(t)
			base := server.URL
			if provider == "openai" {
				base += "/v1"
			}
			if _, e := c.SaveSettings(provider, ConnectionUpdate{BaseURL: &base, ClearAPIKey: true}); e != nil {
				t.Fatal(e)
			}
			enrich := c.Enricher(core.LLMConfig{Provider: provider, Protocol: "systemone", Model: "d1:2b", Lookback: 2, Instructions: "CUSTOM RULE"}, 1)
			bars := core.DemoCandles(100)[:3]
			f := map[string]float64{"fast.value": 101}
			a, e := enrich(context.Background(), bars, f, "BTCUSDT", "long")
			if e != nil {
				t.Fatal(e)
			}
			if a.Verdict.EnterLong != 0.91 || a.Verdict.ExitLong != 0.12 || a.Verdict.EnterShort != 0.03 || a.Verdict.ExitShort != 0.82 || !strings.Contains(a.Verdict.Reason, "no generated explanation") {
				t.Fatalf("bad scores: %+v", a)
			}
			a, e = enrich(context.Background(), bars, f, "BTCUSDT", "long")
			if e != nil || !a.Cached || len(requests) != 1 {
				t.Fatal("cache failed", e)
			}
			if _, e = enrich(context.Background(), bars, f, "ETHUSDT", "long"); e == nil || len(requests) != 1 {
				t.Fatal("budget bypass")
			}
		})
	}
}
func TestSystemOneRejectsIncompleteOrInvalidAnswers(t *testing.T) {
	valid := map[string]systemOneAnswer{}
	for _, key := range []string{"enter_long", "exit_long", "enter_short", "exit_short"} {
		valid[key] = systemOneAnswer{Type: "noul", Noul: ptr(0.5)}
	}
	for _, bad := range []systemOneAnswer{{}, {Type: "noul"}, {Type: "choice", Noul: ptr(0.5)}, {Type: "noul", Noul: ptr(-0.1)}, {Type: "noul", Noul: ptr(1.1)}} {
		valid["enter_long"] = bad
		if _, e := parseSystemOne(valid); e == nil {
			t.Fatal("invalid answer accepted", bad)
		}
	}
	delete(valid, "enter_long")
	if _, e := parseSystemOne(valid); e == nil {
		t.Fatal("missing answer accepted")
	}
}
func TestD1PresetValidationAndUnknownProtocol(t *testing.T) {
	s := core.D1Strategy()
	if e := core.ValidateStrategy(s); e != nil {
		t.Fatal(e)
	}
	s.LLM.Protocol = "unknown"
	if e := core.ValidateStrategy(s); e == nil {
		t.Fatal("unknown protocol accepted")
	}
}
