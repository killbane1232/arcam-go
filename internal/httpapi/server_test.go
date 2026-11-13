package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"arcam/internal/app"
	"arcam/internal/core"
	"arcam/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("ENABLE_LIVE_TRADING", "false")
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	s, e := app.New(st)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = app.NewSessions(s); e != nil {
		t.Fatal(e)
	}
	if e = s.Seed(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close(); st.Close() })
	return &Server{App: s}
}
func TestHTTPGuardsAndStrictJSON(t *testing.T) {
	s := testServer(t)
	s.Token = "secret"
	handler := s.Handler()
	for _, tt := range []struct {
		method, path, host, origin, token, body string
		want                                    int
	}{
		{"GET", "/api/catalog", "localhost:8080", "", "", "", 401},
		{"GET", "/api/catalog", "evil.example", "", "secret", "", 403},
		{"POST", "/api/strategies", "localhost:8080", "https://evil.example", "secret", "{}", 403},
		{"POST", "/api/strategies", "localhost:8080", "", "secret", `{"id":"x","unexpected":true}`, 400},
		{"GET", "/api/catalog", "localhost:8080", "", "secret", "", 200},
		{"GET", "/", "localhost:8080", "", "", "", 200},
	} {
		r := httptest.NewRequest(tt.method, "http://"+tt.host+tt.path, strings.NewReader(tt.body))
		r.Host = tt.host
		r.Header.Set("Content-Type", "application/json")
		if tt.origin != "" {
			r.Header.Set("Origin", tt.origin)
		}
		if tt.token != "" {
			r.Header.Set("Authorization", "Bearer "+tt.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tt.want {
			t.Fatalf("%s %s: %d expected %d: %s", tt.method, tt.path, w.Code, tt.want, w.Body.String())
		}
	}
}
func TestMCPResearchFlow(t *testing.T) {
	s := testServer(t)
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	tools, e := session.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(tools.Tools) < 10 {
		t.Fatal("missing research tools")
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "order") || strings.Contains(tool.Name, "session") {
			t.Fatalf("live tool exposed: %s", tool.Name)
		}
	}
	foundResume := false
	for _, tool := range tools.Tools {
		foundResume = foundResume || tool.Name == "resume_backtest"
	}
	if !foundResume {
		t.Fatal("missing resume_backtest tool")
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_catalog", Arguments: map[string]any{}})
	if e != nil || result.IsError {
		t.Fatalf("catalog %v %v", result, e)
	}
	strategy, _ := json.Marshal(core.DemoStrategy())
	result, e = session.CallTool(ctx, &mcp.CallToolParams{Name: "save_strategy", Arguments: map[string]any{"id": "mcp-hypothesis", "strategy_json": string(strategy)}})
	if e != nil || result.IsError {
		t.Fatalf("save %v %v", result, e)
	}
	if _, e = s.App.Store.Version("mcp-hypothesis", 1); e != nil {
		t.Fatal(e)
	}
	result, e = session.CallTool(ctx, &mcp.CallToolParams{Name: "run_backtest", Arguments: map[string]any{"strategy_id": "mcp-hypothesis", "version": 1, "source": "demo", "symbols": []string{"BTCUSDT"}, "interval": "1h", "from": "2025-01-05T00:00:00Z", "to": "2025-01-06T00:00:00Z", "capital": 1000, "fee_bps": 4, "slippage_bps": 2, "allow_llm": false, "max_llm_calls": 0}})
	if e != nil || result.IsError {
		t.Fatalf("run %v %v", result, e)
	}
}

func TestSettingsAndStorageAPI(t *testing.T) {
	s := testServer(t)
	s.Token = "owner"
	h := s.Handler()
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost:8080"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/settings/llm", "/api/storage"} {
		if w := request("GET", path, "", ""); w.Code != 401 {
			t.Fatal("unprotected", path, w.Code)
		}
	}
	if w := request("POST", "/api/settings/llm/openai", `{"api_key":"new-secret"}`, ""); w.Code != 401 {
		t.Fatal("settings write unprotected")
	}
	if w := request("POST", "/api/settings/llm/openai/check", `{}`, ""); w.Code != 401 {
		t.Fatal("settings check unprotected")
	}
	for _, tt := range []struct {
		body string
		code int
	}{{`{"api_key":"saved-secret","default_model":"model"}`, 200}, {`{"api_key":""}`, 200}, {`{"unknown":true}`, 400}, {`{"timeout_seconds":-1}`, 400}} {
		w := request("POST", "/api/settings/llm/openai", tt.body, "owner")
		if w.Code != tt.code || strings.Contains(w.Body.String(), "saved-secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := request("GET", "/api/settings/llm", "", "owner")
	if w.Code != 200 || strings.Contains(w.Body.String(), "saved-secret") || !strings.Contains(w.Body.String(), `"api_key_configured":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/api/storage", "", "owner")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "BTCUSDT.sqlite") || !strings.Contains(w.Body.String(), `"partition_by":"symbol"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/api/candles?source=demo&symbol=BTCUSDT&interval=1h&from=2025-01-05T00:00:00Z&to=2025-01-05T02:00:00Z", "", "owner")
	var d store.Dataset
	if e := json.Unmarshal(w.Body.Bytes(), &d); e != nil || w.Code != 200 || len(d.Candles) != 2 {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	w = request("GET", "/api/candles?source=demo&symbol=BTCUSDT&interval=1h&from=bad", "", "owner")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = request("GET", "/api/candles?source=demo&symbol=SOLUSDT&interval=1h", "", "owner")
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestCompactRunProgress(t *testing.T) {
	s := testServer(t)
	run := core.Run{ID: "progress", Status: "completed", BacktestProgress: &core.BacktestProgress{ProcessedCandles: 2, TotalCandles: 2}, LLMStats: &core.LLMStats{Responses: 1, CacheHits: 1, TotalResponseMS: 150, AverageResponseMS: 150}, InputData: map[string][]core.Candle{"BTCUSDT": {{Close: 100}}}, Pairs: []core.PairResult{{Symbol: "BTCUSDT"}}}
	if e := s.App.Store.Put("runs", run.ID, run); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost:8080/api/runs/progress/progress", nil))
	var out core.Run
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	if out.BacktestProgress.ProcessedCandles != 2 || out.LLMStats.AverageResponseMS != 150 || out.InputData != nil || out.Pairs != nil {
		t.Fatal("progress endpoint omitted counters or included large arrays")
	}
}

func TestRunUpdatesHTTP(t *testing.T) {
	s := testServer(t)
	r := core.Run{ID: "stream", Status: "running", InputData: map[string][]core.Candle{"BTCUSDT": {{Close: 99}}}, Pairs: []core.PairResult{{Symbol: "BTCUSDT", Candles: []core.Candle{{Close: 10}, {Close: 20}}, Decisions: []core.Decision{{Action: "hold"}, {Action: "enter_long"}}, Equity: []core.EquityPoint{{Value: 1000}, {Value: 1001}}}}}
	if e := s.App.Store.Put("runs", r.ID, r); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		cursor string
		status int
	}{{`{"BTCUSDT":{"candles":1,"fills":0,"trades":0}}`, 200}, {`{"BTCUSDT":{"candles":-1}}`, 400}, {`{} {}`, 400}, {`{"BTCUSDT":{"unexpected":1}}`, 400}} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost:8080/api/runs/stream/updates?after="+url.QueryEscape(tc.cursor), nil))
		if w.Code != tc.status {
			t.Fatal(tc.cursor, w.Code, w.Body.String())
		}
		if tc.status == 200 {
			var out app.RunUpdate
			if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil || out.Run.InputData != nil || out.Run.Pairs != nil || len(out.Pairs) != 1 || len(out.Pairs[0].Pair.Candles) != 1 || out.Pairs[0].Pair.Candles[0].Close != 20 || out.Pairs[0].Next.Candles != 2 {
				t.Fatal("invalid delta response", e, w.Body.String())
			}
		}
	}
}

func TestResumeHTTPKeepsRunAndRejectsConfigurationChanges(t *testing.T) {
	s := testServer(t)
	s.Token = "owner"
	v, e := s.App.Store.Version("trend-demo", 1)
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.App.Store.Dataset("demo", "BTCUSDT", "1h")
	if e != nil {
		t.Fatal(e)
	}
	r := core.BacktestRequest{StrategyID: v.ID, Version: v.Version, Source: "demo", Symbols: []string{"BTCUSDT"}, Interval: "1h", From: d.Candles[100].Time, To: d.Candles[104].Time, Capital: 1000, FeeBPS: 4, SlippageBPS: 2}
	input := d.Candles[100-v.Strategy.Warmup() : 104]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, e := core.BacktestWithUpdates(ctx, v.Strategy, r, "BTCUSDT", input, nil, func(p core.PairResult) {
		if len(p.Decisions) == 2 {
			cancel()
		}
	})
	if e == nil || len(p.Decisions) != 2 {
		t.Fatal("bad fixture", e)
	}
	run := core.Run{ID: "resume-http", Status: "failed", Error: "temporary failure", Request: r, Strategy: v, InputData: map[string][]core.Candle{"BTCUSDT": input}, Pairs: []core.PairResult{p}, BacktestProgress: &core.BacktestProgress{ProcessedCandles: 2, TotalCandles: 4}}
	if e = s.App.Store.Put("runs", run.ID, run); e != nil {
		t.Fatal(e)
	}
	h := s.Handler()
	for _, tc := range []struct {
		body, token string
		want        int
	}{{`{}`, "", 401}, {`{"capital":2000}`, "owner", 400}, {`{}`, "owner", 202}} {
		req := httptest.NewRequest("POST", "http://localhost:8080/api/runs/resume-http/resume", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
		if tc.want == 202 {
			var out core.Run
			if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil || out.ID != run.ID || out.Request.Capital != 1000 || out.Pairs != nil || out.InputData != nil || out.BacktestProgress.ProcessedCandles != 2 {
				t.Fatal("invalid continuation response", w.Body.String())
			}
		}
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		out, e := s.App.Run(run.ID)
		if e != nil {
			t.Fatal(e)
		}
		if out.Status == "completed" {
			if len(out.Pairs[0].Decisions) != 4 || out.BacktestProgress.ProcessedCandles != 4 {
				t.Fatal("duplicated results")
			}
			return
		}
		if out.Status == "failed" {
			t.Fatal(out.Error)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("resume did not finish")
}
