package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"arcam/internal/core"
	"arcam/internal/store"
)

type rangeSource struct {
	mu    sync.Mutex
	calls []candleRange
	fetch func(context.Context, string, string, time.Time, time.Time) ([]core.Candle, error)
}

func (s *rangeSource) Symbols(context.Context) ([]string, error) { return []string{"BTCUSDT"}, nil }
func (s *rangeSource) Candles(ctx context.Context, symbol, interval string, from, to time.Time) ([]core.Candle, error) {
	s.mu.Lock()
	s.calls = append(s.calls, candleRange{from, to})
	s.mu.Unlock()
	if s.fetch != nil {
		return s.fetch(ctx, symbol, interval, from, to)
	}
	return fakeSource{}.Candles(ctx, symbol, interval, from, to)
}
func (s *rangeSource) requests() []candleRange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]candleRange(nil), s.calls...)
}
func historyRequest(t *testing.T, s *Service) core.BacktestRequest {
	t.Helper()
	strategy := core.Strategy{Name: "history", Rules: core.Rules{EnterLong: &core.Rule{Left: core.Field("close"), Op: "gt", Right: core.Constant(0)}, ExitLong: &core.Rule{Left: core.Field("close"), Op: "lt", Right: core.Constant(0)}}, Risk: core.Risk{PositionFraction: 0.2}}
	if _, e := s.Store.SaveVersion("history", strategy); e != nil {
		t.Fatal(e)
	}
	from := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	return core.BacktestRequest{StrategyID: "history", Version: 1, Source: "exchange", Symbols: []string{"BTCUSDT"}, Interval: "1h", From: from, To: from.Add(8 * time.Hour), Capital: 1000}
}
func waitRun(t *testing.T, s *Service, id string) core.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var r core.Run
		if e := s.Store.Get("runs", id, &r); e != nil {
			t.Fatal(e)
		}
		if r.Status != "queued" && r.Status != "preparing" && r.Status != "running" {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("run timeout")
	return core.Run{}
}
func TestAutomaticHistoryFillsOnlyGapsAndPersists(t *testing.T) {
	s := testApp(t)
	r := historyRequest(t, s)
	source := &rangeSource{}
	s.Sources[r.Source] = source
	start := r.From.Add(-2 * time.Hour)
	all, _ := fakeSource{}.Candles(context.Background(), "BTCUSDT", "1h", start, r.To)
	cached := []core.Candle{all[1], all[3], all[4], all[7]}
	for i := range cached {
		cached[i].Volume = 777
	}
	if _, e := s.Store.Merge(store.Dataset{Source: r.Source, Symbol: "BTCUSDT", Interval: "1h", Candles: cached}); e != nil {
		t.Fatal(e)
	}
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	run = waitRun(t, s, run.ID)
	if run.Status != "completed" {
		t.Fatal(run.Error)
	}
	calls := source.requests()
	want := []candleRange{{start, start.Add(time.Hour)}, {start.Add(2 * time.Hour), start.Add(3 * time.Hour)}, {start.Add(5 * time.Hour), start.Add(7 * time.Hour)}, {start.Add(8 * time.Hour), r.To}}
	if len(calls) != len(want) {
		t.Fatal(calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("range %d: %+v want %+v", i, calls[i], want[i])
		}
	}
	if len(run.InputData["BTCUSDT"]) != 10 || len(run.DataLoads) != 4 {
		t.Fatal("incomplete snapshot/log")
	}
	for _, i := range []int{1, 3, 4, 7} {
		if run.InputData["BTCUSDT"][i].Volume != 777 {
			t.Fatal("cached values overwritten")
		}
	}
	saved, e := s.Store.Dataset(r.Source, "BTCUSDT", "1h")
	if e != nil || len(saved.Candles) != 10 {
		t.Fatal("downloads not persisted", e)
	}
	repeat, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	if out := waitRun(t, s, repeat.ID); out.Status != "completed" {
		t.Fatal(out.Error)
	}
	if len(source.requests()) != 4 {
		t.Fatal("complete history downloaded twice")
	}
}
func TestAutoHistoryAbsentDatasetAndBatching(t *testing.T) {
	s := testApp(t)
	r := historyRequest(t, s)
	r.To = r.From.Add((historyBatch + 1) * time.Hour)
	source := &rangeSource{}
	s.Sources[r.Source] = source
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	out := waitRun(t, s, run.ID)
	if out.Status != "completed" {
		t.Fatal(out.Error)
	}
	if len(source.requests()) != 2 || len(out.InputData["BTCUSDT"]) != historyBatch+3 {
		t.Fatal("warmup/batching lost")
	}
	for _, c := range source.requests() {
		if c.to.Sub(c.from) > historyBatch*time.Hour {
			t.Fatal("oversized download")
		}
	}
}
func TestUnavailableHistoryFailsWithoutInventingCandles(t *testing.T) {
	for _, mode := range []string{"partial", "empty", "error", "out_of_range"} {
		t.Run(mode, func(t *testing.T) {
			s := testApp(t)
			r := historyRequest(t, s)
			source := &rangeSource{fetch: func(ctx context.Context, symbol, interval string, from, to time.Time) ([]core.Candle, error) {
				if mode == "error" {
					return nil, fmt.Errorf("HTTP 429")
				}
				if mode == "empty" {
					return nil, nil
				}
				bars, _ := fakeSource{}.Candles(ctx, symbol, interval, from, to)
				if mode == "partial" {
					return bars[1:], nil
				}
				bars[0].Time = from.Add(-time.Hour)
				return bars, nil
			}}
			s.Sources[r.Source] = source
			run, e := s.StartRun(r)
			if e != nil {
				t.Fatal(e)
			}
			out := waitRun(t, s, run.ID)
			if out.Status != "failed" || len(out.Pairs) != 0 || out.Error == "" {
				t.Fatal(out.Status, out.Error)
			}
			if len(source.requests()) != 1 {
				t.Fatal("unbounded retries")
			}
			if mode == "partial" {
				d, e := s.Store.Dataset(r.Source, "BTCUSDT", "1h")
				if e != nil || len(d.Candles) != 9 {
					t.Fatal("valid partial data not retained", e)
				}
			}
		})
	}
}
func TestDemoMissingTestPeriodIsNotReportedAsWarmup(t *testing.T) {
	s := testApp(t)
	r := historyRequest(t, s)
	r.Source = "demo"
	start := r.From.Add(-2 * time.Hour)
	bars, _ := fakeSource{}.Candles(context.Background(), "BTCUSDT", "1h", start, r.To)
	for source, data := range map[string][]core.Candle{"demo": bars[:4], "binance_futures": bars} {
		if _, e := s.Store.Merge(store.Dataset{Source: source, Symbol: "BTCUSDT", Interval: "1h", Candles: data}); e != nil {
			t.Fatal(e)
		}
	}
	_, e := s.StartRun(r)
	if e == nil || !strings.Contains(e.Error(), "first gap in test period") || !strings.Contains(e.Error(), "DEMO is synthetic") || !strings.Contains(e.Error(), "Other source covering these dates: binance_futures") {
		t.Fatal(e)
	}
}
func TestCancelHistoryDownload(t *testing.T) {
	s := testApp(t)
	r := historyRequest(t, s)
	started := make(chan struct{})
	source := &rangeSource{fetch: func(ctx context.Context, _, _ string, _, _ time.Time) ([]core.Candle, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	s.Sources[r.Source] = source
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not start")
	}
	var active core.Run
	s.Store.Get("runs", run.ID, &active)
	if active.Status != "preparing" {
		t.Fatal(active.Status)
	}
	if e = s.CancelRun(run.ID); e != nil {
		t.Fatal(e)
	}
	out := waitRun(t, s, run.ID)
	if out.Status != "cancelled" {
		t.Fatal(out.Status, out.Error)
	}
}
func TestConcurrentPreparationsReuseHistoryAndFreezeCachedValues(t *testing.T) {
	s := testApp(t)
	r := historyRequest(t, s)
	start := r.From.Add(-2 * time.Hour)
	all, _ := fakeSource{}.Candles(context.Background(), "BTCUSDT", "1h", start, r.To)
	if _, e := s.Store.Merge(store.Dataset{Source: r.Source, Symbol: "BTCUSDT", Interval: "1h", Candles: all[:1]}); e != nil {
		t.Fatal(e)
	}
	started, release := make(chan struct{}), make(chan struct{})
	source := &rangeSource{fetch: func(ctx context.Context, symbol, interval string, from, to time.Time) ([]core.Candle, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return fakeSource{}.Candles(ctx, symbol, interval, from, to)
	}}
	s.Sources[r.Source] = source
	a, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	b, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	all[0].Volume = 999
	if _, e = s.Store.Merge(store.Dataset{Source: r.Source, Symbol: "BTCUSDT", Interval: "1h", Candles: all[:1]}); e != nil {
		t.Fatal(e)
	}
	close(release)
	for _, run := range []core.Run{a, b} {
		out := waitRun(t, s, run.ID)
		if out.Status != "completed" || out.InputData["BTCUSDT"][0].Volume != 10 {
			t.Fatal("snapshot changed", out.Status, out.Error)
		}
	}
	if len(source.requests()) != 1 {
		t.Fatal("duplicate download", len(source.requests()))
	}
}

func TestContextHistoryWarmupFrozenAndHashed(t *testing.T) {
	s := testApp(t)
	strategy := core.LevelsStrategy()
	v, e := s.Store.SaveVersion("levels", strategy)
	if e != nil {
		t.Fatal(e)
	}
	from := time.Date(2025, 2, 1, 10, 0, 0, 0, time.UTC)
	r := core.BacktestRequest{StrategyID: v.ID, Version: v.Version, Source: "exchange", Symbols: []string{"BTCUSDT"}, Interval: "1h", From: from, To: from.Add(8 * time.Hour), Capital: 1000}
	source := &rangeSource{}
	s.Sources[r.Source] = source
	started, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	out := waitRun(t, s, started.ID)
	if out.Status != "completed" {
		t.Fatal(out.Error)
	}
	warmup, e := strategy.WarmupBars("1h")
	if e != nil {
		t.Fatal(e)
	}
	input := out.InputData["BTCUSDT"]
	if len(input) != warmup+8 || !input[0].Time.Equal(from.Add(-time.Duration(warmup)*time.Hour)) {
		t.Fatal("context history was not loaded")
	}
	if out.Pairs[0].DataHash != store.Hash(input) {
		t.Fatal("context source missing from hash")
	}
	f := out.Pairs[0].Decisions[0].Features
	for _, key := range []string{"atr14.value", "adr5.value", "previous_day.high", "day.high"} {
		if _, ok := f[key]; !ok {
			t.Fatalf("missing warmed context %s", key)
		}
	}
	r.Interval = "1d"
	strategy.Indicators[0].Timeframe = "4h"
	v, e = s.Store.SaveVersion("bad-context", strategy)
	if e != nil {
		t.Fatal(e)
	}
	r.StrategyID = v.ID
	r.Version = v.Version
	r.From = from.Truncate(24 * time.Hour)
	r.To = r.From.Add(24 * time.Hour)
	if _, e = s.StartRun(r); e == nil {
		t.Fatal("incompatible timeframe accepted")
	}
}
