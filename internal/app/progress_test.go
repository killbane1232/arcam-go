package app

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arcam/internal/core"
	"arcam/internal/llm"
)

func progressRequest(t *testing.T, s *Service, handler http.HandlerFunc) core.BacktestRequest {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		handler(w, r)
	}))
	t.Cleanup(func() { s.Close(); server.CloseClientConnections(); server.Close() })
	s.LLM.Providers["ollama"] = llm.Endpoint{URL: server.URL + "/api/chat"}
	if e := s.Seed(); e != nil {
		t.Fatal(e)
	}
	strategy := core.DemoStrategy()
	strategy.LLM = &core.LLMConfig{Provider: "ollama", Model: "progress-test", Lookback: 2}
	// This fixture intentionally needs a fresh model verdict for every candle.
	strategy.Rules = core.Rules{
		EnterLong: &core.Rule{Left: core.Field("llm.enter_long"), Op: "gte", Right: core.Constant(0.5)},
		ExitLong:  &core.Rule{Left: core.Field("llm.exit_long"), Op: "gte", Right: core.Constant(0.5)},
	}
	if _, e := s.Store.SaveVersion("progress", strategy); e != nil {
		t.Fatal(e)
	}
	from := time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC)
	return core.BacktestRequest{StrategyID: "progress", Version: 1, Source: "demo", Symbols: []string{"BTCUSDT", "ETHUSDT"}, Interval: "1h", From: from, To: from.Add(3 * time.Hour), Capital: 1000, AllowLLM: true, MaxLLMCalls: 6}
}

func writeProgressVerdict(w http.ResponseWriter) {
	json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": `{"enter_long":0.8,"exit_long":0.1,"enter_short":0.2,"exit_short":0.1,"reason":"test"}`}})
}

func TestRunReportsSkippedLLMWithoutUsingBudget(t *testing.T) {
	s := testApp(t)
	var calls atomic.Int32
	r := progressRequest(t, s, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		http.Error(w, "model should not be called", 500)
	})
	v, e := s.Store.Version(r.StrategyID, r.Version)
	if e != nil {
		t.Fatal(e)
	}
	v.Strategy.Rules.EnterLong = &core.Rule{All: []core.Rule{*v.Strategy.Rules.EnterLong, {Left: core.Field("close"), Op: "lt", Right: core.Constant(0)}}}
	v, e = s.Store.SaveVersion(r.StrategyID, v.Strategy)
	if e != nil {
		t.Fatal(e)
	}
	r.Version, r.MaxLLMCalls = v.Version, 1
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	out := waitRun(t, s, run.ID)
	if out.Status != "completed" || out.LLMStats.Skipped != 6 || out.LLMStats.Responses != 0 || out.LLMStats.CacheHits != 0 || calls.Load() != 0 || out.BacktestProgress.ProcessedCandles != 6 {
		t.Fatalf("skips/budget/progress: %s %+v %+v calls=%d error=%s", out.Status, out.LLMStats, out.BacktestProgress, calls.Load(), out.Error)
	}
}

func TestRunProgressLLMTimingAndCache(t *testing.T) {
	s := testApp(t)
	var calls atomic.Int32
	waiting, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	r := progressRequest(t, s, func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 2 {
			close(waiting)
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		writeProgressVerdict(w)
	})
	t.Cleanup(unblock)
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("model not called")
	}
	active, e := s.Run(run.ID)
	if e != nil || active.Status != "running" || active.BacktestProgress.ProcessedCandles != 1 || active.BacktestProgress.TotalCandles != 6 || active.BacktestProgress.CurrentSymbol != "BTCUSDT" {
		t.Fatalf("active progress: %+v, %v", active.BacktestProgress, e)
	}
	if active.LLMStats.Responses != 1 || active.LLMStats.CacheHits != 0 || active.LLMStats.AverageResponseMS <= 0 || !active.BacktestProgress.LastCandleTime.Equal(r.From.Add(time.Hour)) {
		t.Fatalf("active statistics: %+v", active.LLMStats)
	}
	// API callers must not mutate counters or the last candle in the worker.
	active.BacktestProgress.ProcessedCandles = 999
	*active.BacktestProgress.LastCandleTime = time.Time{}
	active.LLMStats.Responses = 999
	active.Pairs[0].Candles[0].Close = -1
	active.Pairs[0].Decisions[0].Features["close"] = -1
	unchanged, e := s.Run(run.ID)
	if e != nil || unchanged.Pairs[0].Candles[0].Close <= 0 || unchanged.Pairs[0].Decisions[0].Features["close"] <= 0 {
		t.Fatal("caller mutated the live result", e)
	}
	listed, e := s.Runs()
	if e != nil || len(listed) != 1 || listed[0].BacktestProgress.ProcessedCandles != 1 || listed[0].LLMStats.Responses != 1 || listed[0].BacktestProgress.LastCandleTime.IsZero() {
		t.Fatal("live snapshots shared mutable data", e)
	}
	unblock()
	finished := waitRun(t, s, run.ID)
	if finished.Status != "completed" || finished.BacktestProgress.ProcessedCandles != 6 || finished.LLMStats.Responses != 6 || finished.LLMStats.CacheHits != 0 {
		t.Fatalf("completed statistics: %+v, %+v, %s", finished.BacktestProgress, finished.LLMStats, finished.Error)
	}
	total := 0.0
	for _, p := range finished.Pairs {
		for _, d := range p.Decisions {
			total += d.LLM.ResponseMS
		}
	}
	if math.Abs(total/6-finished.LLMStats.AverageResponseMS) > 1e-9 {
		t.Fatal("average must include successful responses of both pairs")
	}
	r.MaxLLMCalls = 1
	repeat, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	cached := waitRun(t, s, repeat.ID)
	if cached.Status != "completed" || cached.BacktestProgress.ProcessedCandles != 6 || cached.LLMStats.Responses != 0 || cached.LLMStats.CacheHits != 6 || cached.LLMStats.AverageResponseMS != 0 || calls.Load() != 6 {
		t.Fatalf("cache should not count as model latency: %+v, %s", cached.LLMStats, cached.Error)
	}
}

func TestProgressRetainedOnFailureAndCancellation(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			s := testApp(t)
			var calls atomic.Int32
			waiting := make(chan struct{})
			r := progressRequest(t, s, func(w http.ResponseWriter, req *http.Request) {
				if calls.Add(1) == 2 {
					close(waiting)
					if status == "cancelled" {
						<-req.Context().Done()
						return
					}
					http.Error(w, "failed", 500)
					return
				}
				writeProgressVerdict(w)
			})
			run, e := s.StartRun(r)
			if e != nil {
				t.Fatal(e)
			}
			select {
			case <-waiting:
			case <-time.After(5 * time.Second):
				t.Fatal("second request not reached")
			}
			if status == "cancelled" {
				if e = s.CancelRun(run.ID); e != nil {
					t.Fatal(e)
				}
			}
			out := waitRun(t, s, run.ID)
			if out.Status != status || out.BacktestProgress.ProcessedCandles != 1 || out.BacktestProgress.TotalCandles != 6 || out.LLMStats.Responses != 1 || out.LLMStats.AverageResponseMS <= 0 {
				t.Fatalf("partial progress lost: %s %+v %+v", out.Status, out.BacktestProgress, out.LLMStats)
			}
			if len(out.Pairs) != 1 || len(out.Pairs[0].Candles) != 1 || len(out.Pairs[0].Decisions) != 1 || len(out.Pairs[0].Equity) != 1 || len(out.Pairs[0].Fills) != 0 || out.Pairs[0].OpenPosition != nil || out.Pairs[0].Complete {
				t.Fatal("partial result lost or uncommitted next candle leaked", out.Pairs)
			}
		})
	}
}
