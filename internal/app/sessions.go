package app

import (
	"context"
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
	Order  execution.Order `json:"order"`
	Action string          `json:"action"`
	Reason string          `json:"reason"`
	At     time.Time       `json:"at"`
}
type SessionEvent struct {
	Symbol   string             `json:"symbol"`
	Time     time.Time          `json:"time"`
	Decision *core.Decision     `json:"decision,omitempty"`
	Fill     *core.Fill         `json:"fill,omitempty"`
	Receipt  *execution.Receipt `json:"receipt,omitempty"`
}
type Session struct {
	ID         string                        `json:"id"`
	Status     string                        `json:"status"`
	Error      string                        `json:"error,omitempty"`
	CreatedAt  time.Time                     `json:"created_at"`
	Request    SessionRequest                `json:"request"`
	Strategy   core.Version                  `json:"strategy"`
	Positions  map[string]core.Position      `json:"positions"`
	Balances   map[string]float64            `json:"balances"`
	LastCandle map[string]time.Time          `json:"last_candle"`
	Previous   map[string]map[string]float64 `json:"previous"`
	Pending    *Intent                       `json:"pending,omitempty"`
	Events     []SessionEvent                `json:"events"`
}
type sessionWorker struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	enrich core.Enricher
}
type Sessions struct {
	App             *Service
	Brokers         map[string]execution.Broker
	LiveEnabled     bool
	MaxLiveNotional float64
	mu              sync.Mutex
	control         sync.Mutex
	workers         map[string]*sessionWorker
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
		var v Session
		if e = s.Store.Get("sessions", id, &v); e != nil {
			return nil, e
		}
		if v.Status == "running" {
			v.Status = "paused"
			v.Error = "service restarted; reconcile and resume explicitly"
			if e = s.Store.Put("sessions", id, v); e != nil {
				return nil, e
			}
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
	now := time.Now().UTC().Truncate(step)
	check := core.BacktestRequest{StrategyID: r.StrategyID, Version: r.Version, Source: r.Exchange, Symbols: r.Symbols, Interval: r.Interval, From: now.Add(-step), To: now, Capital: r.Capital, FeeBPS: r.FeeBPS, SlippageBPS: r.SlippageBPS, MaxLLMCalls: r.MaxLLMCalls}
	if e = core.ValidateBacktest(check); e != nil {
		return Session{}, e
	}
	v, e := m.App.Store.Version(r.StrategyID, r.Version)
	if e != nil {
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
	s := Session{ID: ID(), Status: "running", CreatedAt: time.Now().UTC(), Request: r, Strategy: v, Positions: map[string]core.Position{}, Balances: map[string]float64{}, LastCandle: map[string]time.Time{}, Previous: map[string]map[string]float64{}, Events: []SessionEvent{}}
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
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
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
			select {
			case <-ctx.Done():
				s.Status = "paused"
				s.Error = "session paused"
				_ = m.App.Store.Put("sessions", s.ID, s)
				return
			case <-ticker.C:
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
		if position.Side != "" {
			bar := core.Candle{Open: quote, High: quote, Low: quote, Close: quote}
			if _, reason := core.RiskExit(position, bar, strategy.Risk); reason != "" {
				if e = m.fill(ctx, s, symbol, "exit_"+position.Side, quote, reason, time.Now().UTC()); e != nil {
					return e
				}
				continue
			}
		}
		end := time.Now().UTC().Truncate(step)
		last := end.Add(-step)
		if !last.After(s.LastCandle[symbol]) {
			continue
		}
		bars, e := m.App.Sources[s.Request.Exchange].Candles(ctx, symbol, s.Request.Interval, end.Add(-time.Duration(strategy.Warmup()+2)*step), end)
		if e != nil {
			return e
		}
		if len(bars) < strategy.Warmup()+2 || !bars[len(bars)-1].Time.Equal(last) {
			return fmt.Errorf("%s: incomplete warmup or stale market data", symbol)
		}
		for i := 1; i < len(bars); i++ {
			if !bars[i].Time.Equal(bars[i-1].Time.Add(step)) {
				return fmt.Errorf("%s: market data gap", symbol)
			}
		}
		if _, e = m.App.Store.Merge(store.Dataset{Source: s.Request.Exchange, Symbol: symbol, Interval: s.Request.Interval, Candles: bars}); e != nil {
			return e
		}
		features := core.Features(bars, strategy.Indicators)
		current := features[len(features)-1]
		previous := s.Previous[symbol]
		if previous == nil || !s.LastCandle[symbol].Equal(last.Add(-step)) {
			previous = features[len(features)-2]
		}
		decision, e := core.ResolveDecision(ctx, strategy, bars, current, previous, symbol, position.Side, enrich)
		if e != nil {
			return e
		}
		decision.Time = end
		s.LastCandle[symbol] = last
		s.Previous[symbol] = current
		if time.Since(end) > 30*time.Second {
			decision.Action = "hold"
			decision.Reason = "signal older than 30 seconds; waiting for the next candle"
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
			if e = m.fill(ctx, s, symbol, decision.Action, quote, "strategy", end); e != nil {
				return e
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
	p := s.Positions[symbol]
	entry := strings.HasPrefix(action, "enter_")
	buy := action == "enter_long" || action == "exit_short"
	qty := p.Quantity
	if entry {
		if p.Side != "" {
			return fmt.Errorf("position already open")
		}
		notional := math.Min(s.Balances[symbol]*s.Strategy.Strategy.Risk.PositionFraction, s.Request.MaxOrderNotional)
		qty = notional / (quote * (1 + s.Request.FeeBPS/10000))
		if qty <= 0 {
			return fmt.Errorf("insufficient paper capital")
		}
	} else if p.Side == "" {
		return nil
	}
	side := "SELL"
	if buy {
		side = "BUY"
	}
	intent := Intent{Order: execution.Order{Symbol: symbol, Side: side, Quantity: strconv.FormatFloat(qty, 'f', 12, 64), ReduceOnly: !entry, ClientID: "arc-" + store.Hash([]string{s.ID, symbol, action, signalTime.Format(time.RFC3339Nano)})[:28]}, Action: action, Reason: reason, At: time.Now().UTC()}
	if s.Request.Mode == "paper" {
		price := core.Slipped(quote, s.Request.SlippageBPS, buy)
		if entry {
			qty = math.Min(s.Balances[symbol]*s.Strategy.Strategy.Risk.PositionFraction, s.Request.MaxOrderNotional) / (price * (1 + s.Request.FeeBPS/10000))
		}
		m.applyFill(s, intent, qty, price, nil)
		return m.App.Store.Put("sessions", s.ID, s)
	}
	if !m.LiveEnabled {
		return fmt.Errorf("live execution is disabled")
	}
	broker := m.Brokers[s.Request.Exchange]
	if e := m.matchPosition(ctx, s, symbol); e != nil {
		return e
	}
	if e := broker.CheckAccount(ctx, []string{symbol}); e != nil {
		return e
	}
	order, e := broker.Prepare(ctx, intent.Order, quote)
	if e != nil {
		return e
	}
	intent.Order = order
	s.Pending = &intent
	if e = m.App.Store.Put("sessions", s.ID, s); e != nil {
		return e
	}
	receipt, e := broker.Submit(ctx, order)
	if e != nil {
		return e
	}
	return m.complete(s, receipt)
}
func (m *Sessions) complete(s *Session, r execution.Receipt) error {
	if s.Pending == nil {
		return fmt.Errorf("no pending order")
	}
	if r.ClientID != s.Pending.Order.ClientID {
		return fmt.Errorf("order response client ID mismatch")
	}
	if r.Status != "FILLED" {
		return fmt.Errorf("order %s is %s; reconcile before resuming", r.ClientID, r.Status)
	}
	qty, e := strconv.ParseFloat(r.ExecutedQty, 64)
	if e != nil || !core.Finite(qty) || qty <= 0 {
		return fmt.Errorf("invalid executed quantity")
	}
	price, e := strconv.ParseFloat(r.AvgPrice, 64)
	if e != nil || !core.Finite(price) || price <= 0 {
		return fmt.Errorf("invalid fill price")
	}
	wanted, e := strconv.ParseFloat(s.Pending.Order.Quantity, 64)
	if e != nil || math.Abs(wanted-qty) > math.Max(1e-10, wanted*1e-8) {
		return fmt.Errorf("filled quantity differs from requested quantity")
	}
	m.applyFill(s, *s.Pending, qty, price, &r)
	s.Pending = nil
	return m.App.Store.Put("sessions", s.ID, s)
}
func (m *Sessions) applyFill(s *Session, in Intent, qty, price float64, receipt *execution.Receipt) {
	symbol := in.Order.Symbol
	fee := qty * price * s.Request.FeeBPS / 10000
	if strings.HasPrefix(in.Action, "enter_") {
		side := strings.TrimPrefix(in.Action, "enter_")
		s.Positions[symbol] = core.Position{Side: side, Entry: price, Quantity: qty, EntryFee: fee, Time: in.At}
		s.Balances[symbol] -= fee
	} else {
		p := s.Positions[symbol]
		s.Balances[symbol] += p.PnL(price) - fee
		s.Positions[symbol] = core.Position{}
	}
	s.Events = append(s.Events, SessionEvent{Symbol: symbol, Time: in.At, Fill: &core.Fill{Time: in.At, Action: in.Action, Price: price, Quantity: qty, Reason: in.Reason}, Receipt: receipt})
	trimEvents(s)
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
				if e = m.fill(ctx, &s, sym, "exit_"+p.Side, q, "manual_close", time.Now().UTC()); e != nil {
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
		s.Previous = map[string]map[string]float64{}
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
