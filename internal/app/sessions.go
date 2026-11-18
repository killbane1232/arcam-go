package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"arcam/internal/core"
	"arcam/internal/execution"
	"arcam/internal/market"
	"arcam/internal/store"
)

type SessionRequest struct {
	StrategyID       string   `json:"strategy_id"`
	Version          int      `json:"version"`
	Exchange         string   `json:"exchange"`
	Mode             string   `json:"mode"`
	Symbols          []string `json:"symbols"`
	Interval         string   `json:"interval"`
	Capital          float64  `json:"capital"`
	FeeBPS           float64  `json:"fee_bps"`
	SlippageBPS      float64  `json:"slippage_bps"`
	MaxOrderNotional float64  `json:"max_order_notional"`
	AllowLLM         bool     `json:"allow_llm"`
	MaxLLMCalls      int      `json:"max_llm_calls"`
}
type Intent struct {
	Plan          *core.EntryPlan `json:"plan,omitempty"`
	BalanceBefore float64         `json:"balance_before,omitempty"`
	SignalTime    time.Time       `json:"signal_time,omitempty"`
	Order         execution.Order `json:"order"`
	Action        string          `json:"action"`
	Reason        string          `json:"reason"`
	At            time.Time       `json:"at"`
}
type SessionEvent struct {
	Execution *core.ExecutionAudit `json:"execution,omitempty"`
	Trade     *core.Trade          `json:"trade,omitempty"`
	Symbol    string               `json:"symbol"`
	Time      time.Time            `json:"time"`
	Decision  *core.Decision       `json:"decision,omitempty"`
	Fill      *core.Fill           `json:"fill,omitempty"`
	Receipt   *execution.Receipt   `json:"receipt,omitempty"`
}
type Session struct {
	Setups          map[string]map[string]core.SetupState `json:"setups,omitempty"`
	Daily           map[string]DailyEntries               `json:"daily_entries,omitempty"`
	EntryViolations map[string]string                     `json:"entry_violations,omitempty"`
	InputData       map[string][]core.Candle              `json:"input_data,omitempty"`
	ID              string                                `json:"id"`
	Status          string                                `json:"status"`
	Error           string                                `json:"error,omitempty"`
	CreatedAt       time.Time                             `json:"created_at"`
	Request         SessionRequest                        `json:"request"`
	Strategy        core.Version                          `json:"strategy"`
	Positions       map[string]core.Position              `json:"positions"`
	Balances        map[string]float64                    `json:"balances"`
	LastCandle      map[string]time.Time                  `json:"last_candle"`
	Previous        map[string]map[string]float64         `json:"previous"`
	Pending         *Intent                               `json:"pending,omitempty"`
	Events          []SessionEvent                        `json:"events"`
}
type sessionWorker struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	enrich core.Enricher
}
type Sessions struct {
	now             func() time.Time // Injectable clock; nil uses the wall clock.
	App             *Service
	Brokers         map[string]execution.Broker
	LiveEnabled     bool
	MaxLiveNotional float64
	mu              sync.Mutex
	control         sync.Mutex
	workers         map[string]*sessionWorker
}

func (m *Sessions) nowUTC() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

func NewSessions(s *Service) (*Sessions, error) {
	limit := 100.0
	if raw := os.Getenv("MAX_LIVE_ORDER_NOTIONAL"); raw != "" {
		v, e := strconv.ParseFloat(raw, 64)
		if e != nil || !core.Finite(v) || v <= 0 {
			return nil, fmt.Errorf("invalid MAX_LIVE_ORDER_NOTIONAL")
		}
		limit = v
	}
	base := os.Getenv("BINANCE_FUTURES_BASE_URL")
	if base == "" {
		base = "https://fapi.binance.com"
	}
	if os.Getenv("BINANCE_FUTURES_BASE_URL") != "" {
		feed := market.NewBinance(true)
		feed.BaseURL = strings.TrimRight(base, "/")
		s.Sources["binance_futures"] = feed
	}
	enabled := os.Getenv("ENABLE_LIVE_TRADING") == "true"
	if enabled && os.Getenv("ARCAM_API_TOKEN") == "" {
		return nil, fmt.Errorf("live trading requires ARCAM_API_TOKEN")
	}
	m := &Sessions{App: s, LiveEnabled: enabled, MaxLiveNotional: limit, workers: map[string]*sessionWorker{}, Brokers: map[string]execution.Broker{"binance_futures": &execution.Binance{BaseURL: strings.TrimRight(base, "/"), Key: os.Getenv("BINANCE_API_KEY"), Secret: os.Getenv("BINANCE_API_SECRET"), Client: &http.Client{Timeout: 15 * time.Second}, MaxNotional: limit}}}
	// No automatic resumption of real orders after a crash.
	ids, e := s.Store.IDs("sessions")
	if e != nil {
		return nil, e
	}
	for _, id := range ids {
		if _, e = s.Store.RecoverStatus("sessions", id, []string{"running"}, "paused", "service restarted; reconcile and resume explicitly"); e != nil {
			return nil, e
		}
	}
	s.Sessions = m
	return m, nil
}
func (m *Sessions) List() ([]Session, error) {
	ids, e := m.App.Store.IDs("sessions")
	if e != nil {
		return nil, e
	}
	out := []Session{}
	for _, id := range ids {
		var s Session
		if e = m.App.Store.Get("sessions", id, &s); e != nil {
			return nil, e
		}
		s.Events = nil
		s.InputData = nil
		s.Previous = nil
		out = append(out, s)
	}
	return out, nil
}
func (m *Sessions) Start(ctx context.Context, r SessionRequest) (Session, error) {
	m.control.Lock()
	defer m.control.Unlock()
	r.Symbols = append([]string(nil), r.Symbols...)
	if r.Mode != "paper" && r.Mode != "live" {
		return Session{}, fmt.Errorf("mode must be paper or live")
	}
	if r.Mode == "live" && !m.LiveEnabled {
		return Session{}, fmt.Errorf("live execution disabled; configure ENABLE_LIVE_TRADING and credentials")
	}
	broker, ok := m.Brokers[r.Exchange]
	if !ok || m.App.Sources[r.Exchange] == nil {
		return Session{}, fmt.Errorf("exchange adapter unavailable")
	}
	step, e := core.Interval(r.Interval)
	if e != nil {
		return Session{}, e
	}
	now := m.nowUTC().Truncate(step)
	check := core.BacktestRequest{StrategyID: r.StrategyID, Version: r.Version, Source: r.Exchange, Symbols: r.Symbols, Interval: r.Interval, From: now.Add(-step), To: now, Capital: r.Capital, FeeBPS: r.FeeBPS, SlippageBPS: r.SlippageBPS, MaxLLMCalls: r.MaxLLMCalls}
	if e = core.ValidateBacktest(check); e != nil {
		return Session{}, e
	}
	v, e := m.App.Store.Version(r.StrategyID, r.Version)
	if e != nil {
		return Session{}, e
	}
	if _, e := v.Strategy.WarmupBars(r.Interval); e != nil {
		return Session{}, e
	}
	if v.Strategy.LLM != nil && (!r.AllowLLM || r.MaxLLMCalls < 1) {
		return Session{}, fmt.Errorf("LLM requires allow_llm and positive max_llm_calls")
	}
	if !core.Finite(r.MaxOrderNotional) || r.MaxOrderNotional <= 0 {
		return Session{}, fmt.Errorf("positive max_order_notional required")
	}
	if r.Mode == "live" && r.MaxOrderNotional > m.MaxLiveNotional {
		return Session{}, fmt.Errorf("max_order_notional exceeds server limit")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.workers) >= 10 {
		return Session{}, fmt.Errorf("maximum 10 running sessions")
	}
	sessions, e := m.List()
	if e != nil {
		return Session{}, e
	}
	if r.Mode == "live" {
		for _, old := range sessions {
			if old.Request.Mode != "live" || old.Request.Exchange != r.Exchange {
				continue
			}
			for _, sym := range r.Symbols {
				for _, osym := range old.Request.Symbols {
					if sym == osym && (old.Status != "stopped" || old.Positions[sym].Side != "" || old.Pending != nil) {
						return Session{}, fmt.Errorf("%s reserved by session %s", sym, old.ID)
					}
				}
			}
		}
		if e = broker.CheckAccount(ctx, r.Symbols); e != nil {
			return Session{}, e
		}
		for _, sym := range r.Symbols {
			qty, e := broker.Position(ctx, sym)
			if e != nil {
				return Session{}, e
			}
			if qty != 0 {
				return Session{}, fmt.Errorf("%s has an existing position", sym)
			}
		}
	}
	s := Session{ID: ID(), Status: "running", CreatedAt: m.nowUTC(), Request: r, Strategy: v, Positions: map[string]core.Position{}, Balances: map[string]float64{}, LastCandle: map[string]time.Time{}, Previous: map[string]map[string]float64{}, Events: []SessionEvent{}}
	for _, sym := range r.Symbols {
		s.Balances[sym] = r.Capital / float64(len(r.Symbols))
	}
	if e = m.App.Store.Put("sessions", s.ID, s); e != nil {
		return Session{}, e
	}
	initial, e := store.Clone(s)
	if e != nil {
		return Session{}, e
	}
	m.launch(s)
	return initial, nil
}

// Caller holds m.mu. Each worker owns a session's mutations.
func (m *Sessions) launch(s Session) {
	ctx, cancel := context.WithCancel(m.App.ctx)
	w := &sessionWorker{cancel: cancel, done: make(chan struct{})}
	if s.Strategy.Strategy.LLM != nil {
		w.enrich = m.App.LLM.Enricher(*s.Strategy.Strategy.LLM, s.Request.MaxLLMCalls)
	}
	m.workers[s.ID] = w
	go func() {
		defer close(w.done)
		defer cancel()
		defer func() { m.mu.Lock(); delete(m.workers, s.ID); m.mu.Unlock() }()
		for {
			w.mu.Lock()
			e := m.tick(ctx, &s, w.enrich)
			if e != nil {
				s.Status = "paused"
				s.Error = e.Error()
			}
			if ctx.Err() != nil {
				s.Status = "paused"
				s.Error = "session paused"
			}
			if pe := m.App.Store.Put("sessions", s.ID, s); pe != nil {
				fmt.Fprintf(os.Stderr, "session persistence error: %v\n", pe)
				w.mu.Unlock()
				return
			}
			w.mu.Unlock()
			if e != nil || ctx.Err() != nil {
				return
			}
			now := m.nowUTC()
			timer := time.NewTimer(nextSessionPoll(now).Sub(now))
			select {
			case <-ctx.Done():
				timer.Stop()
				s.Status = "paused"
				s.Error = "session paused"
				_ = m.App.Store.Put("sessions", s.ID, s)
				return
			case <-timer.C:
			}
		}
	}()
}
func (m *Sessions) tick(ctx context.Context, s *Session, enrich core.Enricher) error {
	if s.Pending != nil {
		return fmt.Errorf("unresolved order; reconcile before resuming")
	}
	step, _ := core.Interval(s.Request.Interval)
	broker := m.Brokers[s.Request.Exchange]
	strategy := s.Strategy.Strategy
	warmup, e := strategy.WarmupBars(s.Request.Interval)
	if e != nil {
		return e
	}
	// Freeze one information boundary for the whole tick, before any I/O.
	end := m.nowUTC().Truncate(step)
	for _, symbol := range s.Request.Symbols {
		if e := ctx.Err(); e != nil {
			return e
		}
		quote, e := broker.Quote(ctx, symbol)
		if e != nil {
			return e
		}
		if s.Request.Mode == "live" {
			if e = m.matchPosition(ctx, s, symbol); e != nil {
				return e
			}
		}
		position := s.Positions[symbol]
		if position.Side != "" && s.EntryViolations[symbol] != "" {
			if e = m.closeEntryViolation(ctx, s, symbol); e != nil {
				return e
			}
			continue
		}
		if position.Side != "" {
			bar := core.Candle{Open: quote, High: quote, Low: quote, Close: quote}
			if _, reason := core.RiskExit(position, bar, strategy.Risk); reason != "" {
				if e = m.fill(ctx, s, symbol, "exit_"+position.Side, quote, reason, m.nowUTC()); e != nil {
					return e
				}
				continue
			}
		}
		last := end.Add(-step)
		if !last.After(s.LastCandle[symbol]) {
			continue
		}
		bars, e := m.closedSessionHistory(ctx, s, symbol, end, warmup)
		if e != nil {
			return e
		}
		features, e := core.BuildFeatures(bars, strategy, s.Request.Interval)
		if e != nil {
			return e
		}
		current, previous, states := sessionSetupFeatures(s, symbol, features, bars, step, position.Side)
		decision, e := core.ResolveDecision(ctx, strategy, bars, current, previous, symbol, position.Side, enrich)
		if e != nil {
			return e
		}
		decision.Time = end
		daily := sessionDay(s, symbol, end)
		decision.EntryDay, decision.EntriesToday = daily.Day, daily.Count
		core.PrepareEntryDecision(strategy, &decision, s.Balances[symbol], states, daily.Day, daily.Count)
		decision.Setups = states
		if s.Setups == nil {
			s.Setups = map[string]map[string]core.SetupState{}
		}
		s.Setups[symbol] = states
		s.LastCandle[symbol] = last
		s.Previous[symbol] = current
		if !m.withinSessionFrame(end) {
			decision.Action = "hold"
			decision.Reason = missedSessionFrameReason
			if decision.Plan != nil {
				decision.Execution = &core.ExecutionAudit{Action: "enter_" + decision.Plan.Side, Status: "skipped", Reason: missedSessionFrameReason, Plan: decision.Plan}
			}
		}
		s.Events = append(s.Events, SessionEvent{Symbol: symbol, Time: end, Decision: &decision})
		trimEvents(s)
		// Persist the consumed candle before any order; a restart never replays it.
		if e = m.App.Store.Put("sessions", s.ID, s); e != nil {
			return e
		}
		if decision.Action != "hold" {
			quote, e = broker.Quote(ctx, symbol)
			if e != nil {
				return e
			}
			if e = m.fillWithPlan(ctx, s, symbol, decision.Action, quote, "strategy", end, decision.Plan); e != nil {
				if !errors.Is(e, errMissedSessionFrame) {
					return e
				}
				decision.Action = "hold"
				decision.Reason = missedSessionFrameReason
				if decision.Plan != nil {
					decision.Execution = &core.ExecutionAudit{Action: "enter_" + decision.Plan.Side, Status: "skipped", Reason: missedSessionFrameReason, Plan: decision.Plan}
				}
				if e = m.App.Store.Put("sessions", s.ID, s); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func (m *Sessions) matchPosition(ctx context.Context, s *Session, symbol string) error {
	actual, e := m.Brokers[s.Request.Exchange].Position(ctx, symbol)
	if e != nil {
		return e
	}
	p := s.Positions[symbol]
	expected := p.Quantity
	if p.Side == "short" {
		expected = -expected
	}
	if math.Abs(actual-expected) > math.Max(1e-10, math.Abs(expected)*1e-8) {
		return fmt.Errorf("%s: exchange position differs from recorded position; manual reconciliation required", symbol)
	}
	return nil
}
func (m *Sessions) fill(ctx context.Context, s *Session, symbol, action string, quote float64, reason string, signalTime time.Time) error {
	return m.fillWithPlan(ctx, s, symbol, action, quote, reason, signalTime, nil)
}

func (m *Sessions) complete(s *Session, r execution.Receipt) error {
	if s.Pending == nil {
		return fmt.Errorf("no pending order")
	}
	in := *s.Pending
	if r.ClientID != in.Order.ClientID {
		return fmt.Errorf("order response client ID mismatch")
	}
	terminal := r.Status == "CANCELED" || r.Status == "EXPIRED" || r.Status == "REJECTED" || r.Status == "EXPIRED_IN_MATCH"
	if r.Status != "FILLED" && !terminal {
		return fmt.Errorf("order %s is %s; reconcile before resuming", r.ClientID, r.Status)
	}
	qty, e := strconv.ParseFloat(r.ExecutedQty, 64)
	if e != nil || !core.Finite(qty) || qty < 0 || !terminal && qty == 0 {
		return fmt.Errorf("invalid executed quantity")
	}
	wanted, e := strconv.ParseFloat(in.Order.Quantity, 64)
	if e != nil || !core.Finite(wanted) || wanted <= 0 || qty > wanted+math.Max(1e-10, wanted*1e-8) || !terminal && math.Abs(wanted-qty) > math.Max(1e-10, wanted*1e-8) {
		return fmt.Errorf("filled quantity differs from requested quantity")
	}
	if terminal && qty == 0 {
		audit := &core.ExecutionAudit{Action: in.Action, Status: "skipped", Reason: "order " + r.Status, Plan: in.Plan}
		decisionExecution(s, in.Order.Symbol, in.SignalTime, audit)
		s.Events = append(s.Events, SessionEvent{Symbol: in.Order.Symbol, Time: m.nowUTC(), Execution: audit, Receipt: &r})
		trimEvents(s)
		s.Pending = nil
		if e = m.App.Store.Put("sessions", s.ID, s); e != nil {
			return e
		}
		if in.Order.ReduceOnly {
			return fmt.Errorf("exit order %s is %s without execution; position retained", r.ClientID, r.Status)
		}
		return nil
	}
	price, e := strconv.ParseFloat(r.AvgPrice, 64)
	if e != nil || !core.Finite(price) || price <= 0 {
		return fmt.Errorf("invalid fill price")
	}
	if strings.HasPrefix(in.Action, "enter_") && s.Strategy.Strategy.RequiresPlannedExecution() && in.Plan == nil {
		return fmt.Errorf("missing frozen plan for confirmed entry")
	}
	if r.UpdateTime > 0 {
		in.At = time.UnixMilli(r.UpdateTime).UTC()
	}
	if !strings.HasPrefix(in.Action, "enter_") && qty < s.Positions[in.Order.Symbol].Quantity-math.Max(1e-10, wanted*1e-8) {
		return fmt.Errorf("partial exit requires reconciliation; protection and intent retained")
	}
	m.applyFill(s, in, qty, price, &r)
	s.Pending = nil
	return m.App.Store.Put("sessions", s.ID, s)
}
func trimEvents(s *Session) {
	if len(s.Events) > 5000 {
		s.Events = s.Events[len(s.Events)-5000:]
	}
}
func (m *Sessions) Pause(id string) error {
	m.mu.Lock()
	w := m.workers[id]
	m.mu.Unlock()
	if w != nil {
		w.cancel()
		<-w.done
	}
	var s Session
	if e := m.App.Store.Get("sessions", id, &s); e != nil {
		return e
	}
	s.Status = "paused"
	return m.App.Store.Put("sessions", id, s)
}
func (m *Sessions) Command(ctx context.Context, id, action string) (Session, error) {
	m.control.Lock()
	defer m.control.Unlock()
	if action != "pause" && action != "resume" && action != "reconcile" && action != "close" {
		return Session{}, fmt.Errorf("unknown session command")
	}
	if e := m.Pause(id); e != nil {
		return Session{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var s Session
	if e := m.App.Store.Get("sessions", id, &s); e != nil {
		return s, e
	}
	if action == "pause" {
		return s, nil
	}
	if s.Request.Mode == "live" && !m.LiveEnabled {
		return s, fmt.Errorf("live execution disabled")
	}
	if s.Pending != nil {
		if action != "reconcile" {
			return s, fmt.Errorf("pending order requires reconcile")
		}
		r, e := m.Brokers[s.Request.Exchange].Lookup(ctx, s.Pending.Order)
		if e != nil {
			return s, e
		}
		if e = m.complete(&s, r); e != nil {
			return s, e
		}
	}
	if action == "reconcile" && s.Pending == nil {
		for _, sym := range s.Request.Symbols {
			if s.EntryViolations[sym] != "" {
				if e := m.closeEntryViolation(ctx, &s, sym); e != nil {
					return s, e
				}
			}
		}
	}
	if s.Request.Mode == "live" {
		for _, sym := range s.Request.Symbols {
			if e := m.matchPosition(ctx, &s, sym); e != nil {
				return s, e
			}
		}
	}
	s.Error = ""
	if action == "close" {
		for _, sym := range s.Request.Symbols {
			if p := s.Positions[sym]; p.Side != "" {
				q, e := m.Brokers[s.Request.Exchange].Quote(ctx, sym)
				if e != nil {
					return s, e
				}
				if e = m.fill(ctx, &s, sym, "exit_"+p.Side, q, "manual_close", m.nowUTC()); e != nil {
					s.Error = e.Error()
					_ = m.App.Store.Put("sessions", s.ID, s)
					return s, e
				}
			}
		}
		s.Status = "stopped"
	}
	if action == "resume" {
		if len(m.workers) >= 10 {
			return s, fmt.Errorf("maximum 10 running sessions")
		}
		s.Status = "running"
	}
	if e := m.App.Store.Put("sessions", id, s); e != nil {
		return s, e
	}
	response, err := store.Clone(s)
	if err != nil {
		return Session{}, err
	}
	if action == "resume" {
		m.launch(s)
	}
	return response, nil
}
func (m *Sessions) Close() {
	m.mu.Lock()
	workers := []*sessionWorker{}
	for _, w := range m.workers {
		w.cancel()
		workers = append(workers, w)
	}
	m.mu.Unlock()
	for _, w := range workers {
		<-w.done
	}
}
