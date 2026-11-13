package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"arcam/internal/core"
	"arcam/internal/llm"
	"arcam/internal/store"
)

func (s *Service) ResumeRun(id string) (core.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; ok {
		return core.Run{}, fmt.Errorf("run is already active")
	}
	var run core.Run
	if e := s.Store.Get("runs", id, &run); e != nil {
		return run, e
	}
	if run.Status != "failed" {
		return run, fmt.Errorf("only failed runs can be resumed")
	}
	if e := core.ValidateBacktest(run.Request); e != nil {
		return run, e
	}
	if e := core.ValidateStrategy(run.Strategy.Strategy); e != nil {
		return run, e
	}
	r := run.Request
	step, _ := core.Interval(r.Interval)
	from := r.From.Add(-time.Duration(run.Strategy.Strategy.Warmup()) * step)
	if run.InputData == nil {
		return run, fmt.Errorf("saved input data is missing; cannot resume this run")
	}
	needsHistory := false
	for _, symbol := range r.Symbols {
		if len(missingRanges(run.InputData[symbol], from, r.To, step)) > 0 {
			needsHistory = true
		}
	}
	processed := 0
	for i := range run.Pairs {
		p := &run.Pairs[i]
		if i >= len(r.Symbols) || p.Symbol != r.Symbols[i] {
			return run, fmt.Errorf("saved pair order does not match configuration")
		}
		if p.DataHash != "" && p.DataHash != store.Hash(run.InputData[p.Symbol]) {
			return run, fmt.Errorf("%s: frozen data hash mismatch", p.Symbol)
		}
		if e := core.ValidateResume(r, p.Symbol, run.InputData[p.Symbol], *p); e != nil {
			return run, e
		}
		p.Complete = len(p.Candles) == int(r.To.Sub(r.From)/step)
		if !p.Complete && i != len(run.Pairs)-1 {
			return run, fmt.Errorf("unfinished pair before later results")
		}
		processed += len(p.Decisions)
	}
	if run.BacktestProgress == nil {
		run.BacktestProgress = &core.BacktestProgress{}
	}
	if processed != run.BacktestProgress.ProcessedCandles && run.BacktestProgress.ProcessedCandles != 0 {
		return run, fmt.Errorf("saved progress has no matching results; exact resume is unavailable")
	}
	run.BacktestProgress.ProcessedCandles = processed
	run.BacktestProgress.TotalCandles = int(r.To.Sub(r.From)/step) * len(r.Symbols)
	if run.Strategy.Strategy.LLM != nil {
		var endpoint llm.Endpoint
		if e := s.Store.Get("settings", "run-llm-"+id, &endpoint); os.IsNotExist(e) {
			if e = s.LLM.FreezeRun(id, *run.Strategy.Strategy.LLM); e != nil {
				return run, e
			}
			run.ResumeNote = "Legacy run: original LLM connection was not saved; current connection settings were frozen for continuation. Strategy and backtest configuration are unchanged."
		} else if e != nil {
			return run, e
		}
		if run.LLMStats == nil {
			run.LLMStats = &core.LLMStats{}
		}
		// Older reports tracked successful responses, but stopped on the first failed
		// network request. Preserve that consumed request rather than refunding it.
		if run.LLMRequests == 0 {
			run.LLMRequests = run.LLMStats.Responses
			if strings.Contains(run.Error, "LLM call budget") {
				run.LLMRequests = r.MaxLLMCalls
			} else if strings.HasPrefix(run.Error, "LLM ") || strings.HasPrefix(run.Error, "invalid LLM ") || strings.HasPrefix(run.Error, "Ollama response") {
				run.LLMRequests++
			}
		}
	}
	if len(run.Failures) == 0 && run.Error != "" {
		at := run.CreatedAt
		if run.CompletedAt != nil {
			at = *run.CompletedAt
		}
		run.Failures = append(run.Failures, core.RunFailure{At: at, Error: run.Error})
	}
	at := time.Now().UTC()
	run.ResumeCount++
	run.LastResumedAt = &at
	run.Status, run.Error, run.Progress = "queued", "", ""
	run.CompletedAt = nil
	return s.queueRunLocked(run, needsHistory)
}

// Caller owns s.mu. Persist/re-register the same ID atomically, so concurrent
// resume requests cannot create duplicate workers for one run.
func (s *Service) queueRunLocked(run core.Run, needsHistory bool) (core.Run, error) {
	if e := s.ctx.Err(); e != nil {
		return core.Run{}, e
	}
	if len(s.jobs) >= 8 {
		return core.Run{}, fmt.Errorf("job queue is full")
	}
	initial, e := store.Clone(run)
	if e != nil {
		return core.Run{}, e
	}
	if e = s.Store.Put("runs", run.ID, run); e != nil {
		return core.Run{}, e
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.jobs[run.ID] = cancel
	s.wg.Add(1)
	go func() { defer s.wg.Done(); defer cancel(); s.executeRun(ctx, run, needsHistory) }()
	return initial, nil
}
func (s *Service) executeRun(ctx context.Context, run core.Run, needsHistory bool) {
	// Final persistence and unregistering share a lock with ResumeRun. There is
	// no old deferred cleanup left that could accidentally delete a new worker.
	defer func() {
		if ctx.Err() != nil {
			run.Status = "cancelled"
			run.Error = ctx.Err().Error()
		}
		run.Progress = ""
		at := time.Now().UTC()
		run.CompletedAt = &at
		if run.Status == "failed" {
			run.Failures = append(run.Failures, core.RunFailure{At: at, Error: run.Error})
		}
		s.mu.Lock()
		if e := s.Store.Put("runs", run.ID, run); e != nil {
			fmt.Fprintf(os.Stderr, "persist run %s: %v\n", run.ID, e)
		}
		delete(s.jobs, run.ID)
		delete(s.runProgress, run.ID)
		s.mu.Unlock()
	}()
	fail := func(e error) { run.Status = "failed"; run.Error = e.Error() }
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return
	}
	r, v, input := run.Request, run.Strategy, run.InputData
	step, _ := core.Interval(r.Interval)
	from := r.From.Add(-time.Duration(v.Strategy.Warmup()) * step)
	if needsHistory {
		run.Status = "preparing"
		run.Progress = "Downloading missing history"
		if e := s.Store.Put("runs", run.ID, run); e != nil {
			fail(e)
			return
		}
		for _, symbol := range r.Symbols {
			bars, e := s.prepareHistory(ctx, r, symbol, from, input[symbol], func(load core.DataLoad, done bool) error {
				run.Progress = fmt.Sprintf("%s: loading %s — %s", symbol, load.From.Format(time.RFC3339), load.To.Format(time.RFC3339))
				if done {
					run.DataLoads = append(run.DataLoads, load)
				}
				return s.Store.Put("runs", run.ID, run)
			})
			if e != nil {
				fail(e)
				return
			}
			input[symbol] = bars
		}
	}
	run.InputData = input
	run.Progress = ""
	run.Status = "running"
	if e := s.Store.Put("runs", run.ID, run); e != nil {
		fail(e)
		return
	}
	var enrich core.Enricher
	if v.Strategy.LLM != nil {
		var e error
		enrich, e = s.LLM.RunEnricher(run.ID, *v.Strategy.LLM, r.MaxLLMCalls, run.LLMRequests, func() { run.LLMRequests++ })
		if e != nil {
			fail(e)
			return
		}
	}
	for pairIndex, symbol := range r.Symbols {
		if pairIndex < len(run.Pairs) && run.Pairs[pairIndex].Complete {
			continue
		}
		dataHash := store.Hash(input[symbol])
		if pairIndex == len(run.Pairs) {
			run.Pairs = append(run.Pairs, core.PairResult{Symbol: symbol, DataHash: dataHash})
		}
		saved := run.Pairs[pairIndex]
		run.BacktestProgress.CurrentSymbol = symbol
		run.BacktestProgress.LastCandleTime = nil
		if n := len(saved.Decisions); n > 0 {
			at := saved.Decisions[n-1].Time
			run.BacktestProgress.LastCandleTime = &at
		}
		s.publishProgress(run)
		p, e := core.ResumeBacktest(ctx, v.Strategy, r, symbol, input[symbol], saved, enrich, func(partial core.PairResult) {
			partial.DataHash = dataHash
			run.Pairs[pairIndex] = partial
			d := partial.Decisions[len(partial.Decisions)-1]
			run.BacktestProgress.ProcessedCandles++
			run.BacktestProgress.LastCandleTime = &d.Time
			if d.LLMStatus == "skipped" && run.LLMStats != nil {
				run.LLMStats.Skipped++
			}
			if d.LLM != nil && run.LLMStats != nil {
				if d.LLM.Cached {
					run.LLMStats.CacheHits++
				} else {
					run.LLMStats.Responses++
					run.LLMStats.TotalResponseMS += d.LLM.ResponseMS
					run.LLMStats.AverageResponseMS = run.LLMStats.TotalResponseMS / float64(run.LLMStats.Responses)
				}
			}
			s.publishProgress(run)
		})
		p.DataHash = dataHash
		run.Pairs[pairIndex] = p
		if e != nil {
			fail(e)
			return
		}
	}
	run.Status = "completed"
	run.Metrics = core.Aggregate(run.Pairs, r.Capital)
}
