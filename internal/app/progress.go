package app

import (
	"fmt"

	"arcam/internal/core"
	"arcam/internal/store"
)

// Published pairs are append-only prefixes owned by the worker. Header copies
// prevent subsequent pair updates from changing a snapshot. Public methods
// deep-copy returned data so callers cannot mutate the worker's nested values.
type runTelemetry struct {
	run      core.Run
	progress core.BacktestProgress
	llm      core.LLMStats
	hasLLM   bool
	pairs    []core.PairResult
}

func (s *Service) publishProgress(r core.Run) {
	v := runTelemetry{progress: *r.BacktestProgress, hasLLM: r.LLMStats != nil, pairs: append([]core.PairResult(nil), r.Pairs...)}
	v.run = r
	v.run.InputData, v.run.Pairs = nil, nil
	v.run.BacktestProgress, v.run.LLMStats = nil, nil
	if v.hasLLM {
		v.llm = *r.LLMStats
	}
	s.mu.Lock()
	s.runProgress[r.ID] = v
	s.mu.Unlock()
}

// runView is internal: consumers must not mutate or return shared slice views.
func (s *Service) runView(id string, includeInput bool) (core.Run, error) {
	if !includeInput {
		s.mu.Lock()
		v, ok := s.runProgress[id]
		s.mu.Unlock()
		if ok {
			r := v.run
			r.BacktestProgress, r.Pairs = &v.progress, v.pairs
			if v.hasLLM {
				r.LLMStats = &v.llm
			}
			return r, nil
		}
	}
	var r core.Run
	if e := s.Store.Get("runs", id, &r); e != nil {
		return r, e
	}
	s.mu.Lock()
	v, ok := s.runProgress[id]
	s.mu.Unlock()
	if ok && r.Status == "running" {
		r.LLMRequests = v.run.LLMRequests
		r.BacktestProgress = &v.progress
		r.Pairs = v.pairs
		if v.hasLLM {
			r.LLMStats = &v.llm
		}
	}
	return r, nil
}
func (s *Service) Run(id string) (core.Run, error) {
	r, e := s.runView(id, true)
	if e != nil {
		return r, e
	}
	// Clone only the shared results/telemetry; input_data was read from disk.
	input := r.InputData
	r.InputData = nil
	out, e := store.Clone(r)
	out.InputData = input
	return out, e
}
func (s *Service) RunSummary(id string) (core.Run, error) {
	r, e := s.runView(id, false)
	if e != nil {
		return r, e
	}
	r.InputData = nil
	r.Pairs = nil
	return store.Clone(r)
}

type PairCursor struct {
	Candles int `json:"candles"`
	Fills   int `json:"fills"`
	Trades  int `json:"trades"`
}
type PairUpdate struct {
	From  PairCursor      `json:"from"`
	Next  PairCursor      `json:"next"`
	Reset bool            `json:"reset,omitempty"`
	Pair  core.PairResult `json:"pair"`
}
type RunUpdate struct {
	Run   core.Run     `json:"run"`
	Pairs []PairUpdate `json:"pairs"`
}

// RunUpdates sends only unseen candles/decisions/equity and new fills/trades.
// A cursor beyond the available prefix requests a reset of that pair, which
// also lets clients recover cleanly after a stale or invalid local snapshot.
func (s *Service) RunUpdates(id string, after map[string]PairCursor) (RunUpdate, error) {
	if len(after) > 10 {
		return RunUpdate{}, fmt.Errorf("maximum 10 pair cursors")
	}
	for symbol, c := range after {
		if !core.SymbolPattern.MatchString(symbol) || c.Candles < 0 || c.Fills < 0 || c.Trades < 0 {
			return RunUpdate{}, fmt.Errorf("invalid cursor")
		}
	}
	r, e := s.runView(id, false)
	if e != nil {
		return RunUpdate{}, e
	}
	out := RunUpdate{Pairs: []PairUpdate{}}
	for _, p := range r.Pairs {
		next := PairCursor{len(p.Candles), len(p.Fills), len(p.Trades)}
		from := after[p.Symbol]
		reset := from.Candles > next.Candles || from.Fills > next.Fills || from.Trades > next.Trades
		if reset {
			from = PairCursor{}
		}
		// Older failed reports may contain a half-evaluated candle. Export only
		// aligned candle/decision/equity rows, just like the new worker does.
		count := min(len(p.Candles), len(p.Decisions), len(p.Equity))
		if from.Candles > count {
			from = PairCursor{}
			reset = true
		}
		next.Candles = count
		p.Candles = p.Candles[from.Candles:count]
		p.Decisions = p.Decisions[from.Candles:count]
		p.Equity = p.Equity[from.Candles:count]
		p.Fills = p.Fills[from.Fills:]
		p.Trades = p.Trades[from.Trades:]
		out.Pairs = append(out.Pairs, PairUpdate{From: from, Next: next, Reset: reset, Pair: p})
	}
	r.InputData = nil
	r.Pairs = nil
	out.Run = r
	return store.Clone(out)
}
