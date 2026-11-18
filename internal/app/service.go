package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"arcam/internal/core"
	"arcam/internal/llm"
	"arcam/internal/market"
	"arcam/internal/store"
)

type Service struct {
	Store         *store.Store
	LLM           *llm.Client
	Sources       map[string]market.DataSource
	Sessions      *Sessions
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	jobs          map[string]context.CancelFunc
	runProgress   map[string]runTelemetry
	downloadLocks sync.Map
	slots         chan struct{}
	wg            sync.WaitGroup
}

func New(st *store.Store) (*Service, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{Store: st, LLM: llm.New(st), Sources: map[string]market.DataSource{"binance_spot": market.NewBinance(false), "binance_futures": market.NewBinance(true)}, ctx: ctx, cancel: cancel, jobs: map[string]context.CancelFunc{}, runProgress: map[string]runTelemetry{}, slots: make(chan struct{}, 2)}
	ids, e := st.IDs("runs")
	if e != nil {
		cancel()
		return nil, e
	}
	for _, id := range ids {
		if _, e = st.RecoverStatus("runs", id, []string{"running", "queued", "preparing"}, "interrupted", "service restarted; submit a new run"); e != nil {
			cancel()
			return nil, e
		}
	}
	return s, nil
}
func (s *Service) Close() {
	s.cancel()
	if s.Sessions != nil {
		s.Sessions.Close()
	}
	s.wg.Wait()
}
func ID() string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (s *Service) Seed() error {
	if _, e := s.Store.Version("trend-demo", 1); os.IsNotExist(e) {
		if _, e = s.Store.SaveVersion("trend-demo", core.DemoStrategy()); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	if _, e := s.Store.Version("trend-d1-2b", 1); os.IsNotExist(e) {
		if _, e = s.Store.SaveVersion("trend-d1-2b", core.D1Strategy()); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	for symbol, base := range map[string]float64{"BTCUSDT": 95000, "ETHUSDT": 3200} {
		if _, e := s.Store.Dataset("demo", symbol, "1h"); os.IsNotExist(e) {
			if _, e = s.Store.Merge(store.Dataset{Source: "demo", Symbol: symbol, Interval: "1h", Candles: core.DemoCandles(base)}); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
	}
	return nil
}

type ImportRequest struct {
	Source   string    `json:"source"`
	Symbols  []string  `json:"symbols"`
	Interval string    `json:"interval"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
}

func (s *Service) Import(ctx context.Context, r ImportRequest) ([]store.DatasetInfo, error) {
	source, ok := s.Sources[r.Source]
	if !ok {
		return nil, fmt.Errorf("unknown data source")
	}
	step, e := core.Interval(r.Interval)
	if e != nil {
		return nil, e
	}
	if r.From.IsZero() || !r.To.After(r.From) || r.From.UnixMilli()%step.Milliseconds() != 0 || r.To.UnixMilli()%step.Milliseconds() != 0 {
		return nil, fmt.Errorf("dates must align to interval boundaries")
	}
	if len(r.Symbols) == 0 || len(r.Symbols) > 10 {
		return nil, fmt.Errorf("1..10 symbols required")
	}
	out := []store.DatasetInfo{}
	for _, symbol := range r.Symbols {
		c, e := source.Candles(ctx, symbol, r.Interval, r.From, r.To)
		if e != nil {
			return out, e
		}
		v, e := s.Store.Merge(store.Dataset{Source: r.Source, Symbol: symbol, Interval: r.Interval, Candles: c})
		if e != nil {
			return out, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) StartRun(r core.BacktestRequest) (core.Run, error) {
	r.Symbols = append([]string(nil), r.Symbols...)
	if e := core.ValidateBacktest(r); e != nil {
		return core.Run{}, e
	}
	v, e := s.Store.Version(r.StrategyID, r.Version)
	if e != nil {
		return core.Run{}, e
	}
	if v.Strategy.LLM != nil && (!r.AllowLLM || r.MaxLLMCalls < 1) {
		return core.Run{}, fmt.Errorf("LLM strategy requires allow_llm=true and max_llm_calls>=1")
	}
	// Freeze available values now; missing exchange history is filled by the job.
	input := map[string][]core.Candle{}
	step, _ := core.Interval(r.Interval)
	warmup, e := v.Strategy.WarmupBars(r.Interval)
	if e != nil {
		return core.Run{}, e
	}
	from := r.From.Add(-time.Duration(warmup) * step)
	needsHistory := false
	for _, symbol := range r.Symbols {
		bars, e := s.cachedRange(s.ctx, r.Source, symbol, r.Interval, from, r.To)
		if e != nil {
			return core.Run{}, e
		}
		gaps := missingRanges(bars, from, r.To, step)
		if len(gaps) > 0 {
			if s.Sources[r.Source] == nil {
				return core.Run{}, s.coverageError(r, symbol, from, bars, gaps)
			}
			needsHistory = true
		}
		input[symbol] = bars
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	run := core.Run{ID: ID(), Status: "queued", CreatedAt: time.Now().UTC(), Request: r, Strategy: v, InputData: input}
	run.BacktestProgress = &core.BacktestProgress{TotalCandles: int(r.To.Sub(r.From)/step) * len(r.Symbols)}
	if v.Strategy.LLM != nil {
		run.LLMStats = &core.LLMStats{}
		if e := s.LLM.FreezeRun(run.ID, *v.Strategy.LLM); e != nil {
			return core.Run{}, e
		}
	}
	return s.queueRunLocked(run, needsHistory)
}

func (s *Service) CancelRun(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cancel, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("run is not active")
	}
	cancel()
	return nil
}
func (s *Service) Runs() ([]core.Run, error) {
	ids, e := s.Store.IDs("runs")
	if e != nil {
		return nil, e
	}
	out := []core.Run{}
	for _, id := range ids {
		r, e := s.RunSummary(id)
		if e != nil {
			return nil, e
		}
		r.Pairs = nil
		r.InputData = nil
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
