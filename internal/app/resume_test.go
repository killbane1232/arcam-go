package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arcam/internal/core"
	"arcam/internal/llm"
	"arcam/internal/store"
)

func TestResumeSameIDAfterRestartWithFrozenConfiguration(t *testing.T) {
	s := testApp(t)
	var calls atomic.Int32
	waiting, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	r := progressRequest(t, s, func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer frozen-secret" {
			t.Error("connection credentials changed")
		}
		call := calls.Add(1)
		if call == 5 {
			http.Error(w, "temporary failure", 500)
			return
		}
		if call == 6 {
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
	ep := s.LLM.Providers["ollama"]
	ep.Key = "frozen-secret"
	s.LLM.Providers["ollama"] = ep
	r.MaxLLMCalls = 10
	started, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	failed := waitRun(t, s, started.ID)
	if failed.Status != "failed" || failed.LLMRequests != 5 || len(failed.Pairs) != 2 || !failed.Pairs[0].Complete || len(failed.Pairs[1].Decisions) != 1 {
		t.Fatal("unexpected failure checkpoint", failed.Status, failed.LLMRequests)
	}
	before, _ := json.Marshal(failed.Pairs[0])
	// Both a new strategy version and changed local candle data must be ignored.
	strategy, e := store.Clone(failed.Strategy.Strategy)
	if e != nil {
		t.Fatal(e)
	}
	strategy.Name = "changed"
	strategy.LLM.Model = "different-model"
	if _, e = s.Store.SaveVersion(r.StrategyID, strategy); e != nil {
		t.Fatal(e)
	}
	data, e := s.Store.Dataset(r.Source, "BTCUSDT", r.Interval)
	if e != nil {
		t.Fatal(e)
	}
	data.Candles[100].Volume += 999
	if _, e = s.Store.Merge(data); e != nil {
		t.Fatal(e)
	}
	s.Close()
	resumedService, e := New(s.Store)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(resumedService.Close)
	resumedService.LLM.Providers["ollama"] = llm.Endpoint{URL: "http://127.0.0.1:1/changed", Key: "different-secret"}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, e := resumedService.ResumeRun(failed.ID); e == nil {
				success.Add(1)
				if out.ID != failed.ID {
					t.Error("new run created")
				}
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal("duplicate resume workers", success.Load())
	}
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("resume did not reach frozen endpoint")
	}
	active, e := resumedService.Run(started.ID)
	if e != nil {
		t.Fatal(e)
	}
	if active.BacktestProgress.ProcessedCandles != 4 {
		t.Fatal("progress reset or replayed", active.BacktestProgress)
	}
	unblock()
	out := waitRun(t, resumedService, started.ID)
	if out.Status != "completed" || out.ID != failed.ID || out.CreatedAt != failed.CreatedAt || out.ResumeCount != 1 || out.LLMRequests != 7 || out.LLMStats.Responses != 6 || out.LLMStats.CacheHits != 0 || out.BacktestProgress.ProcessedCandles != 6 {
		t.Fatalf("resume: status=%s error=%s count=%d requests=%d stats=%+v progress=%+v", out.Status, out.Error, out.ResumeCount, out.LLMRequests, out.LLMStats, out.BacktestProgress)
	}
	if !reflect.DeepEqual(out.Request, failed.Request) || !reflect.DeepEqual(out.Strategy, failed.Strategy) || !reflect.DeepEqual(out.InputData, failed.InputData) {
		t.Fatal("frozen configuration changed")
	}
	after, _ := json.Marshal(out.Pairs[0])
	if string(before) != string(after) {
		t.Fatal("completed pair was modified")
	}
	if len(out.Failures) != 1 || out.Error != "" || out.LastResumedAt == nil {
		t.Fatal("failure audit lost", out.Failures)
	}
	ids, _ := s.Store.IDs("runs")
	if len(ids) != 1 {
		t.Fatal("new experiment created")
	}
	exported, _ := json.Marshal(out)
	if strings.Contains(string(exported), "frozen-secret") {
		t.Fatal("credentials leaked into run")
	}
	if _, e = resumedService.ResumeRun(out.ID); e == nil {
		t.Fatal("completed run resumed")
	}
}
func TestResumeDoesNotResetLLMBudget(t *testing.T) {
	s := testApp(t)
	var calls atomic.Int32
	r := progressRequest(t, s, func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 2 {
			http.Error(w, "down", 500)
			return
		}
		writeProgressVerdict(w)
	})
	r.MaxLLMCalls = 2
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	out := waitRun(t, s, run.ID)
	if out.Status != "failed" || out.LLMRequests != 2 {
		t.Fatal(out.Status, out.LLMRequests)
	}
	if _, e = s.ResumeRun(run.ID); e != nil {
		t.Fatal(e)
	}
	out = waitRun(t, s, run.ID)
	if out.Status != "failed" || !strings.Contains(out.Error, "budget") || out.LLMRequests != 2 || calls.Load() != 2 || out.BacktestProgress.ProcessedCandles != 1 {
		t.Fatal("budget was refunded", out.Status, out.Error, calls.Load())
	}
}
func TestResumeHistoryPreparation(t *testing.T) {
	s := testApp(t)
	r := historyRequest(t, s)
	var calls atomic.Int32
	s.Sources[r.Source] = &rangeSource{fetch: func(ctx context.Context, symbol, interval string, from, to time.Time) ([]core.Candle, error) {
		if calls.Add(1) == 1 {
			return nil, fmt.Errorf("temporary source failure")
		}
		return fakeSource{}.Candles(ctx, symbol, interval, from, to)
	}}
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	if out := waitRun(t, s, run.ID); out.Status != "failed" {
		t.Fatal(out.Status)
	}
	if _, e = s.ResumeRun(run.ID); e != nil {
		t.Fatal(e)
	}
	out := waitRun(t, s, run.ID)
	if out.Status != "completed" || out.ID != run.ID || out.ResumeCount != 1 || out.BacktestProgress.ProcessedCandles != 8 {
		t.Fatal(out.Status, out.Error)
	}
}
