package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arcam/internal/core"
	"arcam/internal/execution"
	"arcam/internal/market"
	"arcam/internal/store"
)

func testApp(t *testing.T) *Service {
	t.Helper()
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(st)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close(); st.Close() })
	return s
}
func TestRunPersistenceAndWarmup(t *testing.T) {
	s := testApp(t)
	if e := s.Seed(); e != nil {
		t.Fatal(e)
	}
	from := time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC)
	r := core.BacktestRequest{StrategyID: "trend-demo", Version: 1, Source: "demo", Symbols: []string{"BTCUSDT", "ETHUSDT"}, Interval: "1h", From: from, To: from.Add(24 * time.Hour), Capital: 10000, FeeBPS: 4, SlippageBPS: 2}
	run, e := s.StartRun(r)
	if e != nil {
		t.Fatal(e)
	}
	if len(run.InputData) != 2 {
		t.Fatal("inputs not frozen")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e = s.Store.Get("runs", run.ID, &run); e != nil {
			t.Fatal(e)
		}
		if run.Status == "completed" {
			break
		}
		if run.Status == "failed" {
			t.Fatal(run.Error)
		}
		time.Sleep(time.Millisecond)
	}
	if run.Status != "completed" || len(run.Pairs) != 2 {
		t.Fatalf("run %+v", run)
	}
	if run.Metrics.Initial != 10000 {
		t.Fatal("capital was duplicated across pairs")
	}
	r.From = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, e = s.StartRun(r); e == nil {
		t.Fatal("missing warmup accepted")
	}
}

type fakeBroker struct {
	submit  int
	actual  float64
	fail    bool
	receipt execution.Receipt
	store   *store.Store
	id      string
	t       *testing.T
}

func (b *fakeBroker) Position(context.Context, string) (float64, error) { return b.actual, nil }
func (b *fakeBroker) Quote(context.Context, string) (float64, error)    { return 100, nil }
func (b *fakeBroker) Prepare(_ context.Context, o execution.Order, _ float64) (execution.Order, error) {
	return o, nil
}
func (b *fakeBroker) CheckAccount(context.Context, []string) error { return nil }
func (b *fakeBroker) Submit(_ context.Context, o execution.Order) (execution.Receipt, error) {
	b.submit++
	var saved Session
	if e := b.store.Get("sessions", b.id, &saved); e != nil || saved.Pending == nil {
		b.t.Fatal("order was sent before durable intent")
	}
	b.receipt = execution.Receipt{ClientID: o.ClientID, Status: "FILLED", ExecutedQty: o.Quantity, AvgPrice: "100"}
	if o.ReduceOnly {
		b.actual = 0
	} else {
		b.actual = 1
	}
	if b.fail {
		return execution.Receipt{}, fmt.Errorf("timeout")
	}
	return b.receipt, nil
}
func (b *fakeBroker) Lookup(context.Context, execution.Order) (execution.Receipt, error) {
	return b.receipt, nil
}
func TestUnknownOrderNoRetryAndReconciliation(t *testing.T) {
	s := testApp(t)
	m := &Sessions{App: s, LiveEnabled: true, workers: map[string]*sessionWorker{}, Brokers: map[string]execution.Broker{}}
	session := Session{ID: "session", Status: "running", Request: SessionRequest{Mode: "live", Exchange: "fake", Symbols: []string{"BTCUSDT"}, Interval: "1h", MaxOrderNotional: 100}, Strategy: core.Version{Strategy: core.DemoStrategy()}, Positions: map[string]core.Position{}, Balances: map[string]float64{"BTCUSDT": 1000}, LastCandle: map[string]time.Time{}, Previous: map[string]map[string]float64{}}
	b := &fakeBroker{fail: true, store: s.Store, id: session.ID, t: t}
	m.Brokers["fake"] = b
	if e := m.fill(context.Background(), &session, "BTCUSDT", "enter_long", 100, "test", time.Now()); e == nil {
		t.Fatal("timeout ignored")
	}
	if session.Pending == nil {
		t.Fatal("intent lost")
	}
	if e := m.tick(context.Background(), &session, nil); e == nil {
		t.Fatal("unresolved order not blocked")
	}
	if b.submit != 1 {
		t.Fatal("order retried")
	}
	recovered, e := m.Command(context.Background(), session.ID, "reconcile")
	if e != nil {
		t.Fatal(e)
	}
	if recovered.Pending != nil || recovered.Positions["BTCUSDT"].Quantity != 1 || b.submit != 1 {
		t.Fatalf("reconciliation %+v", recovered)
	}
	b.fail = false
	closed, e := m.Command(context.Background(), session.ID, "close")
	if e != nil {
		t.Fatal(e)
	}
	if closed.Positions["BTCUSDT"].Side != "" || closed.Status != "stopped" || b.submit != 2 {
		t.Fatal("close failed")
	}
}
func TestPaperNeverSubmits(t *testing.T) {
	s := testApp(t)
	b := &fakeBroker{t: t}
	m := &Sessions{App: s, Brokers: map[string]execution.Broker{"fake": b}}
	v := Session{ID: "paper", Request: SessionRequest{Mode: "paper", Exchange: "fake", MaxOrderNotional: 100, FeeBPS: 4, SlippageBPS: 2}, Strategy: core.Version{Strategy: core.DemoStrategy()}, Positions: map[string]core.Position{}, Balances: map[string]float64{"BTCUSDT": 1000}}
	if e := m.fill(context.Background(), &v, "BTCUSDT", "enter_long", 100, "test", time.Now()); e != nil {
		t.Fatal(e)
	}
	if b.submit != 0 || v.Positions["BTCUSDT"].Side != "long" {
		t.Fatal("paper executed on broker or failed")
	}
}

func TestSessionUsesLazyLLM(t *testing.T) {
	for _, blocked := range []bool{true, false} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			s := testApp(t)
			s.Sources["fake"] = fakeSource{}
			m := &Sessions{App: s, Brokers: map[string]execution.Broker{"fake": &fakeBroker{t: t}}}
			gate := 50.0
			if blocked {
				gate = 150
			}
			strategy := core.Strategy{Name: "lazy session", LLM: &core.LLMConfig{Provider: "ollama", Model: "test", Lookback: 2}, Risk: core.Risk{PositionFraction: 0.2}, Rules: core.Rules{
				EnterLong: &core.Rule{All: []core.Rule{{Left: core.Field("llm.enter_long"), Op: "gte", Right: core.Constant(.7)}, {Left: core.Field("close"), Op: "gt", Right: core.Constant(gate)}}},
				ExitLong:  &core.Rule{Left: core.Field("close"), Op: "lt", Right: core.Constant(0)},
			}}
			state := Session{ID: "lazy-session", Status: "running", Request: SessionRequest{Mode: "paper", Exchange: "fake", Interval: "1h", Symbols: []string{"BTCUSDT"}, MaxOrderNotional: 100}, Strategy: core.Version{Strategy: strategy}, Positions: map[string]core.Position{}, Balances: map[string]float64{"BTCUSDT": 1000}, LastCandle: map[string]time.Time{}, Previous: map[string]map[string]float64{}}
			calls := 0
			err := m.tick(context.Background(), &state, func(context.Context, []core.Candle, map[string]float64, string, string) (*core.LLMAudit, error) {
				calls++
				return &core.LLMAudit{Verdict: core.Verdict{EnterLong: .1}}, nil
			})
			if err != nil || len(state.Events) != 1 {
				t.Fatal(err, state.Events)
			}
			if blocked && (calls != 0 || state.Events[0].Decision.LLMStatus != "skipped") {
				t.Fatal("session called LLM for a rejected branch")
			}
			if !blocked && (calls != 1 || state.Events[0].Decision.LLMStatus != "evaluated") {
				t.Fatal("session did not request required verdict")
			}
		})
	}
}

type fakeSource struct{}

func (fakeSource) Symbols(context.Context) ([]string, error) { return []string{"BTCUSDT"}, nil }
func (fakeSource) Candles(_ context.Context, _, interval string, from, to time.Time) ([]core.Candle, error) {
	d, _ := core.Interval(interval)
	var bars []core.Candle
	for at := from; at.Before(to); at = at.Add(d) {
		bars = append(bars, core.Candle{Time: at, Open: 100, High: 101, Low: 99, Close: 100, Volume: 10})
	}
	return bars, nil
}
func TestSessionWorkerOwnsReturnedData(t *testing.T) {
	s := testApp(t)
	if _, e := s.Store.SaveVersion("trend", core.DemoStrategy()); e != nil {
		t.Fatal(e)
	}
	s.Sources = map[string]market.DataSource{"fake": fakeSource{}}
	m := &Sessions{App: s, workers: map[string]*sessionWorker{}, Brokers: map[string]execution.Broker{"fake": &fakeBroker{t: t}}}
	s.Sessions = m
	result, e := m.Start(context.Background(), SessionRequest{StrategyID: "trend", Version: 1, Exchange: "fake", Mode: "paper", Symbols: []string{"BTCUSDT"}, Interval: "1h", Capital: 1000, MaxOrderNotional: 100})
	if e != nil {
		t.Fatal(e)
	}
	result.Balances["BTCUSDT"] = 0
	result.Strategy.Strategy.Rules.EnterLong.Op = "bad"
	if e = m.Pause(result.ID); e != nil {
		t.Fatal(e)
	}
	result, e = m.Command(context.Background(), result.ID, "resume")
	if e != nil {
		t.Fatal(e)
	}
	result.Balances["BTCUSDT"] = 0
	result.Previous["BTCUSDT"] = map[string]float64{"close": -1}
	if e = m.Pause(result.ID); e != nil {
		t.Fatal(e)
	}
	var saved Session
	if e = s.Store.Get("sessions", result.ID, &saved); e != nil {
		t.Fatal(e)
	}
	if saved.Balances["BTCUSDT"] != 1000 || saved.Strategy.Strategy.Rules.EnterLong.Op == "bad" {
		t.Fatal("caller mutated active worker")
	}
}
