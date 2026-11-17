package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"arcam/internal/core"
	"arcam/internal/execution"
)

type plannedBroker struct {
	*frameBroker
	step       string
	increase   bool
	fillPrice  float64
	fillTime   time.Time
	fail       bool
	failExit   bool
	status     string
	exitStatus string
	fraction   float64
	last       execution.Receipt
	lookups    int
}

func (b *plannedBroker) Prepare(ctx context.Context, o execution.Order, quote float64) (execution.Order, error) {
	o, e := b.frameBroker.Prepare(ctx, o, quote)
	if e != nil {
		return o, e
	}
	if !o.ReduceOnly && b.step != "" {
		o.Quantity, e = execution.Quantize(o.Quantity, b.step)
	}
	if !o.ReduceOnly && b.increase {
		q, _ := strconv.ParseFloat(o.Quantity, 64)
		o.Quantity = strconv.FormatFloat(q*2, 'f', -1, 64)
	}
	return o, e
}
func (b *plannedBroker) Submit(ctx context.Context, o execution.Order) (execution.Receipt, error) {
	status := b.status
	if o.ReduceOnly {
		status = b.exitStatus
	}
	if (status == "REJECTED" || status == "CANCELED") && (o.ReduceOnly || b.fraction == 0) {
		if b.beforeSubmit != nil {
			b.beforeSubmit(o)
		}
		b.orders = append(b.orders, o)
		b.last = execution.Receipt{ClientID: o.ClientID, Status: status, ExecutedQty: "0", AvgPrice: "0"}
		return b.last, nil
	}
	r, e := b.frameBroker.Submit(ctx, o)
	if e != nil {
		return r, e
	}
	if !o.ReduceOnly {
		if b.fillPrice > 0 {
			r.AvgPrice = strconv.FormatFloat(b.fillPrice, 'f', -1, 64)
		}
		if b.fraction > 0 {
			requested, _ := strconv.ParseFloat(o.Quantity, 64)
			executed := requested * b.fraction
			direction := 1.0
			if o.Side == "SELL" {
				direction = -1
			}
			b.actual -= direction * (requested - executed)
			r.ExecutedQty = strconv.FormatFloat(executed, 'f', -1, 64)
		}
	}
	if status != "" {
		r.Status = status
	}
	at := b.clock.at
	if !b.fillTime.IsZero() {
		at = b.fillTime
	}
	r.UpdateTime = at.UnixMilli()
	b.last = r
	if b.fail || o.ReduceOnly && b.failExit {
		b.fail = false
		b.failExit = false
		return execution.Receipt{}, fmt.Errorf("receipt lost after exchange fill")
	}
	return r, nil
}
func (b *plannedBroker) Lookup(context.Context, execution.Order) (execution.Receipt, error) {
	b.lookups++
	return b.last, nil
}

func plannedStrategy(side string) core.Strategy {
	s := core.Strategy{Name: "planned retest", Indicators: []core.Indicator{}, Risk: core.Risk{PositionFraction: 1, RiskFraction: .01, MaxEntriesPerDay: 2}}
	stop, target := 90.0, 120.0
	if side == "short" {
		stop, target = 110, 80
	}
	s.Setups = []core.Setup{{ID: "event", When: &core.Rule{Left: core.Field("close"), Op: "gt", Right: core.Constant(0)}, Key: core.Field("volume"), Capture: map[string]core.Operand{"level": *core.Field("close"), "stop": *core.Constant(stop)}, ExpiresAfter: 4}}
	entry := &core.Rule{All: []core.Rule{{Setup: "event"}, {Left: core.Field("setup.event.age"), Op: "gte", Right: core.Constant(1)}}}
	// A dynamic offset verifies that captured state history also survives resume.
	exit := &core.Rule{Left: &core.Operand{Field: "setup.event.age", Offset: 1}, Op: "gte", Right: core.Constant(2)}
	protection := &core.Protection{StopPrice: core.Field("setup.event.stop"), TargetPrice: core.Constant(target)}
	if side == "long" {
		s.Rules = core.Rules{EnterLong: entry, ExitLong: exit}
		s.Risk.Long = protection
	} else {
		s.Rules = core.Rules{EnterShort: entry, ExitShort: exit}
		s.Risk.Short = protection
	}
	return s
}
func plannedBars(n int) []core.Candle {
	c := frameBars(n)
	for i := range c {
		c[i].Open = 100
		c[i].High = 101
		c[i].Low = 99
		c[i].Close = 100
		c[i].Volume = 1
	}
	return c
}
func plannedHarness(t *testing.T, mode, side string) (*Sessions, *Session, *frameClock, *frameFeed, *plannedBroker) {
	t.Helper()
	c := plannedBars(900)
	m, s, clock, feed, broker := newFrameSession(t, mode, plannedStrategy(side), c, c[601].Time)
	wrapped := &plannedBroker{frameBroker: broker}
	m.Brokers["frames"] = wrapped
	return m, s, clock, feed, wrapped
}
func nextPlannedFrame(t *testing.T, m *Sessions, s *Session, clock *frameClock) {
	t.Helper()
	clock.at = clock.at.Add(15 * time.Minute)
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
}
func jsonSession(t *testing.T, s *Session) {
	t.Helper()
	encoded, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	var copy Session
	if e = json.Unmarshal(encoded, &copy); e != nil {
		t.Fatal(e)
	}
	*s = copy
}
func sessionTrades(s *Session) []core.Trade {
	out := []core.Trade{}
	for _, event := range s.Events {
		if event.Trade != nil {
			out = append(out, *event.Trade)
		}
	}
	return out
}

func TestPlannedSessionMatchesBacktestAndRestoresEveryFrame(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		for _, side := range []string{"long", "short"} {
			t.Run(mode+"/"+side, func(t *testing.T) {
				m, s, clock, feed, _ := plannedHarness(t, mode, side)
				for i := 606; i < len(feed.bars); i++ {
					feed.bars[i].Volume = 2
				}
				strategy := s.Strategy.Strategy
				warmup, _ := strategy.WarmupBars("15m")
				request := core.BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "15m", From: feed.bars[600].Time, To: feed.bars[616].Time, Capital: 1000, FeeBPS: 4}
				want, e := core.Backtest(context.Background(), strategy, request, "BTCUSDT", feed.bars[600-warmup:], nil)
				if e != nil {
					t.Fatal(e)
				}
				for j := 0; j < 12; j++ {
					clock.at = feed.bars[601+j].Time
					if e = m.tick(context.Background(), s, nil); e != nil {
						t.Fatalf("frame %d: %v", j, e)
					}
					d := sessionDecisions(s)[j]
					expected := want.Decisions[j]
					if d.Action != expected.Action || !reflect.DeepEqual(d.Setups, expected.Setups) || !reflect.DeepEqual(d.Features, expected.Features) {
						t.Fatalf("frame %d differs: session=%+v backtest=%+v", j, d, expected)
					}
					if d.Plan != nil && (d.Plan.StopPrice != expected.Plan.StopPrice || math.Abs(d.Plan.RiskBudget-expected.Plan.RiskBudget) > 1e-8) {
						t.Fatal("frozen protection differs")
					}
					jsonSession(t, s)
				}
				got := sessionTrades(s)
				if len(got) != 2 || len(want.Trades) != 2 {
					t.Fatal("expected two one-shot events", len(got), len(want.Trades))
				}
				for i, tr := range got {
					expected := want.Trades[i]
					if tr.Side != expected.Side || tr.EventID != expected.EventID || tr.StopPrice != expected.StopPrice || tr.TargetPrice != expected.TargetPrice || !tr.EntryTime.Equal(expected.EntryTime) || !tr.ExitTime.Equal(expected.ExitTime) || math.Abs(tr.PnLR-expected.PnLR) > 1e-9 || math.Abs(tr.Quantity-expected.Quantity) > 1e-9 {
						t.Fatalf("trade differs: %+v vs %+v", tr, expected)
					}
				}
				if s.Daily["BTCUSDT"].Count != 2 || s.Setups["BTCUSDT"]["event"].Status != "consumed" {
					t.Fatal("lifecycle/counter lost")
				}
				if math.Abs(s.Balances["BTCUSDT"]-want.Balance) > 1e-8 {
					t.Fatal("balance diverged")
				}
			})
		}
	}
}

func TestPlannedSessionSkipsInvalidEntryWithoutReusingEvent(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		for _, side := range []string{"long", "short"} {
			for _, kind := range []string{"stop_gap", "target_gap", "notional", "distance", "late", "quote_moved"} {
				if mode == "paper" && kind == "quote_moved" {
					continue
				}
				t.Run(mode+"/"+side+"/"+kind, func(t *testing.T) {
					m, s, clock, feed, broker := plannedHarness(t, mode, side)
					switch kind {
					case "notional":
						s.Request.MaxOrderNotional = 50
					case "distance":
						s.Strategy.Strategy.Risk.MaxStopFraction = .05
					}
					if e := m.tick(context.Background(), s, nil); e != nil {
						t.Fatal(e)
					}
					stopGap, targetGap := 85.0, 125.0
					if side == "short" {
						stopGap, targetGap = 115, 75
					}
					if kind == "stop_gap" {
						feed.bars[602].Open = stopGap
					}
					if kind == "target_gap" {
						feed.bars[602].Open = targetGap
					}
					if kind == "quote_moved" {
						broker.onPrepare = func() { feed.bars[602].Open = stopGap }
					}
					clock.at = feed.bars[602].Time
					if kind == "late" {
						clock.at = clock.at.Add(30 * time.Second)
					}
					if e := m.tick(context.Background(), s, nil); e != nil {
						t.Fatal(e)
					}
					d := sessionDecisions(s)[1]
					if len(sessionFills(s)) != 0 || len(broker.orders) != 0 || s.Daily["BTCUSDT"].Count != 0 || d.Execution == nil || d.Execution.Status != "skipped" {
						t.Fatal("invalid entry was not audited/skipped", d)
					}
					if s.Setups["BTCUSDT"]["event"].Status != "consumed" {
						t.Fatal("attempt not consumed")
					}
					// Restore accepted inputs and move to the next frame: the old event must
					// not be reused even if its trigger remains true and the gap disappears.
					feed.bars[602].Open = 100
					broker.onPrepare = nil
					clock.at = feed.bars[603].Time
					if e := m.tick(context.Background(), s, nil); e != nil {
						t.Fatal(e)
					}
					if len(sessionFills(s)) != 0 {
						t.Fatal("skipped event entered twice")
					}
				})
			}
		}
	}
}

func TestPlannedSessionBrokerRoundingAndActualFill(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			m, s, clock, _, broker := plannedHarness(t, "live", side)
			broker.step = "0.3"
			broker.fillPrice = 99
			if side == "short" {
				broker.fillPrice = 101
			}
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			nextPlannedFrame(t, m, s, clock)
			p := s.Positions["BTCUSDT"]
			if p.Entry != broker.fillPrice || math.Abs(p.Quantity-.9) > 1e-9 || p.InitialRisk >= 10 || p.InitialRisk <= 0 {
				t.Fatal("planned values replaced actual fill", p)
			}
			if p.StopPrice != sessionDecisions(s)[1].Plan.StopPrice || p.EventID == "" {
				t.Fatal("protection/event lost")
			}
			jsonSession(t, s)
			nextPlannedFrame(t, m, s, clock)
			if s.Positions["BTCUSDT"].StopPrice != p.StopPrice {
				t.Fatal("stop changed after entry")
			}
		})
	}
	m, s, clock, _, broker := plannedHarness(t, "live", "long")
	broker.increase = true
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
	clock.at = clock.at.Add(15 * time.Minute)
	if e := m.tick(context.Background(), s, nil); e == nil || len(broker.orders) != 0 || s.Pending != nil {
		t.Fatal("broker increased risk without rejection", e)
	}
}

func TestPlannedSessionRecordsAndClosesFillRiskViolation(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		for _, kind := range []string{"budget", "beyond_stop", "beyond_target"} {
			t.Run(side+"/"+kind, func(t *testing.T) {
				m, s, clock, _, broker := plannedHarness(t, "live", side)
				broker.fillPrice = 102
				if side == "short" {
					broker.fillPrice = 98
				}
				if kind == "beyond_stop" {
					broker.fillPrice = 89
					if side == "short" {
						broker.fillPrice = 111
					}
				}
				if kind == "beyond_target" {
					broker.fillPrice = 121
					if side == "short" {
						broker.fillPrice = 79
					}
				}
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				nextPlannedFrame(t, m, s, clock)
				fills := sessionFills(s)
				if len(fills) != 2 || fills[0].Price != broker.fillPrice || fills[1].Reason != "entry_risk_violation" || !broker.orders[1].ReduceOnly || s.Positions["BTCUSDT"].Side != "" {
					t.Fatal("actual risk violation not recorded/closed", fills)
				}
				if sessionDecisions(s)[1].Execution.Status != "risk_breached" || s.Daily["BTCUSDT"].Count != 1 || len(sessionTrades(s)) != 1 {
					t.Fatal("risk violation audit/count missing")
				}
			})
		}
	}
}

func TestPlannedSessionUnknownFillReconcilesOnceWithFrozenProtection(t *testing.T) {
	m, s, clock, _, broker := plannedHarness(t, "live", "long")
	broker.fail = true
	broker.fillPrice = 99
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
	clock.at = clock.at.Add(15 * time.Minute)
	if e := m.tick(context.Background(), s, nil); e == nil || s.Pending == nil {
		t.Fatal("expected unknown fill", e)
	}
	var saved Session
	if e := m.App.Store.Get("sessions", s.ID, &saved); e != nil {
		t.Fatal(e)
	}
	if saved.Pending.Plan == nil || saved.Pending.Plan.StopPrice != 90 || saved.Setups["BTCUSDT"]["event"].Status != "consumed" {
		t.Fatal("intent lost frozen plan")
	}
	if e := m.tick(context.Background(), &saved, nil); e == nil || len(broker.orders) != 1 {
		t.Fatal("unknown order retried")
	}
	clock.at = clock.at.Add(30 * time.Second)
	recovered, e := m.Command(context.Background(), s.ID, "reconcile")
	if e != nil {
		t.Fatal(e)
	}
	if recovered.Pending != nil || recovered.Positions["BTCUSDT"].StopPrice != 90 || recovered.Positions["BTCUSDT"].Entry != 99 || recovered.Daily["BTCUSDT"].Count != 1 {
		t.Fatal("reconciliation lost protection/count")
	}
	again, e := m.Command(context.Background(), s.ID, "reconcile")
	if e != nil {
		t.Fatal(e)
	}
	if broker.lookups != 1 || len(broker.orders) != 1 || again.Daily["BTCUSDT"].Count != 1 || len(sessionFills(&again)) != 1 {
		t.Fatal("reconciliation duplicated entry")
	}
}

func TestPlannedSessionRejectedAndUnfinishedOrders(t *testing.T) {
	for _, status := range []string{"REJECTED", "CANCELED", "PARTIALLY_FILLED"} {
		t.Run(status, func(t *testing.T) {
			m, s, clock, _, broker := plannedHarness(t, "live", "long")
			broker.status = status
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			clock.at = clock.at.Add(15 * time.Minute)
			e := m.tick(context.Background(), s, nil)
			if status == "PARTIALLY_FILLED" {
				if e == nil || s.Pending == nil || s.Pending.Plan.StopPrice != 90 {
					t.Fatal("unfinished order lost plan")
				}
				broker.last.Status = "FILLED"
				if e = m.complete(s, broker.last); e != nil {
					t.Fatal(e)
				}
				if s.Positions["BTCUSDT"].StopPrice != 90 || s.Daily["BTCUSDT"].Count != 1 {
					t.Fatal("completion lost protection")
				}
			} else {
				if e != nil || s.Pending != nil || s.Daily["BTCUSDT"].Count != 0 || len(sessionFills(s)) != 0 {
					t.Fatal("rejected order counted as fill", e)
				}
				if sessionDecisions(s)[1].Execution.Status != "skipped" {
					t.Fatal("missing rejection audit")
				}
			}
		})
	}
}

func TestPlannedSessionDailyLimitUsesFillDayAndSurvivesResume(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		t.Run(mode, func(t *testing.T) {
			c := plannedBars(400)
			strategy := plannedStrategy("long")
			strategy.Risk.MaxEntriesPerDay = 1
			m, s, clock, feed, _ := newFrameSession(t, mode, strategy, c, c[95].Time) // 23:45, awaiting retest.
			s.Daily = map[string]DailyEntries{"BTCUSDT": {Day: c[94].Time.UTC().Format("2006-01-02"), Count: 1}}
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			jsonSession(t, s)
			nextPlannedFrame(t, m, s, clock) // midnight fill belongs to the new day.
			if len(sessionFills(s)) != 1 || s.Daily["BTCUSDT"].Count != 1 || s.Daily["BTCUSDT"].Day != clock.at.Format("2006-01-02") {
				t.Fatal("midnight quota used the signal candle date")
			}
			nextPlannedFrame(t, m, s, clock)
			nextPlannedFrame(t, m, s, clock) // rule exit.
			for i := 99; i < len(feed.bars); i++ {
				feed.bars[i].Volume = 2
			}
			nextPlannedFrame(t, m, s, clock)
			jsonSession(t, s)
			nextPlannedFrame(t, m, s, clock)
			nextPlannedFrame(t, m, s, clock) // retest of the new event.
			d := sessionDecisions(s)
			if d[len(d)-1].Reason != "daily entry limit" || len(sessionFills(s)) != 2 || s.Daily["BTCUSDT"].Count != 1 {
				t.Fatal("daily quota reset on restore or counted exits")
			}
		})
	}
}

func TestPlannedSessionRestoresWaitingExpiryAndCancellationAcrossGap(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		for _, kind := range []string{"expiry", "cancel", "new_event"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				m, s, clock, feed, broker := plannedHarness(t, mode, "long")
				if kind == "expiry" || kind == "new_event" {
					s.Strategy.Strategy.Setups[0].ExpiresAfter = 1
				}
				if kind == "cancel" {
					s.Strategy.Strategy.Setups[0].Cancel = &core.Rule{Left: core.Field("low"), Op: "lt", Right: core.Field("setup.event.stop")}
				}
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				jsonSession(t, s)
				if kind == "cancel" {
					feed.bars[601].Low = 85
				} // violation while paused, then recovery.
				if kind == "new_event" {
					for i := 601; i < len(feed.bars); i++ {
						feed.bars[i].Volume = 2
					}
				}
				clock.at = feed.bars[605].Time
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				state := s.Setups["BTCUSDT"]["event"]
				if len(broker.orders) != 0 || len(sessionFills(s)) != 0 {
					t.Fatal("missed/expired event executed retrospectively")
				}
				expected := "expired"
				if kind == "cancel" {
					expected = "cancelled"
				}
				if kind == "new_event" {
					expected = "missed"
				}
				if state.Status != expected {
					t.Fatalf("got %s want %s", state.Status, expected)
				}
				if kind != "new_event" && !state.ReferenceTime.Equal(feed.bars[601].Time) {
					t.Fatal("waiting reference was replaced")
				}
			})
		}
	}
}

func TestPlannedSessionFailureDoesNotCommitHalfEvaluatedSetup(t *testing.T) {
	m, s, clock, _, _ := plannedHarness(t, "paper", "long")
	strategy := &s.Strategy.Strategy
	strategy.LLM = &core.LLMConfig{Provider: "ollama", Model: "fixture", Lookback: 2}
	strategy.Rules.EnterLong.All = append(strategy.Rules.EnterLong.All, core.Rule{Left: core.Field("llm.enter_long"), Op: "gt", Right: core.Constant(.5)})
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
	before, _ := json.Marshal(s.Setups)
	clock.at = clock.at.Add(15 * time.Minute)
	if e := m.tick(context.Background(), s, func(context.Context, []core.Candle, map[string]float64, string, string) (*core.LLMAudit, error) {
		return nil, fmt.Errorf("model unavailable")
	}); e == nil {
		t.Fatal("expected model failure")
	}
	after, _ := json.Marshal(s.Setups)
	if string(before) != string(after) || len(sessionDecisions(s)) != 1 {
		t.Fatal("half-evaluated setup committed")
	}
	if e := m.tick(context.Background(), s, func(context.Context, []core.Candle, map[string]float64, string, string) (*core.LLMAudit, error) {
		return &core.LLMAudit{Verdict: core.Verdict{EnterLong: 1}}, nil
	}); e != nil {
		t.Fatal(e)
	}
	if len(sessionFills(s)) != 1 || s.Setups["BTCUSDT"]["event"].Status != "consumed" {
		t.Fatal("retry did not execute exactly once")
	}
}

func TestPlannedSessionRiskExitUsesFrozenStopAndReportsR(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		for _, side := range []string{"long", "short"} {
			t.Run(mode+"/"+side, func(t *testing.T) {
				m, s, clock, feed, _ := plannedHarness(t, mode, side)
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				nextPlannedFrame(t, m, s, clock)
				p := s.Positions["BTCUSDT"]
				stop := p.StopPrice
				// A gap through the already frozen stop exits at the observed quote even
				// between frames. This must not resize the position or update its stop.
				quote := 85.0
				if side == "short" {
					quote = 115
				}
				feed.bars[602].Open = quote
				clock.at = clock.at.Add(30 * time.Second)
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				trades := sessionTrades(s)
				if len(trades) != 1 || trades[0].StopPrice != stop || trades[0].Exit != quote || trades[0].Reason != "stop_loss" || trades[0].PnLR >= -1 {
					t.Fatal("frozen stop/gap/R lost", trades)
				}
				if math.Abs(trades[0].PnLR-trades[0].PnL/trades[0].InitialRisk) > 1e-10 {
					t.Fatal("incorrect net R")
				}
			})
		}
	}
}

func TestPlannedSessionReconcilesRiskViolationWithoutLosingExposure(t *testing.T) {
	m, s, clock, _, broker := plannedHarness(t, "live", "long")
	broker.fail = true
	broker.fillPrice = 102
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
	clock.at = clock.at.Add(15 * time.Minute)
	if e := m.tick(context.Background(), s, nil); e == nil {
		t.Fatal("expected timeout")
	}
	clock.at = clock.at.Add(30 * time.Second)
	out, e := m.Command(context.Background(), s.ID, "reconcile")
	if e != nil {
		t.Fatal(e)
	}
	if out.Pending != nil || out.Positions["BTCUSDT"].Side != "" || len(broker.orders) != 2 || !broker.orders[1].ReduceOnly || out.Daily["BTCUSDT"].Count != 1 {
		t.Fatal("unsafe confirmed entry not recorded/closed")
	}
}

func TestPlannedSessionReceiptDateIsUsedForDailyAudit(t *testing.T) {
	m, s, clock, _, broker := plannedHarness(t, "live", "long")
	broker.fillTime = clock.at.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
	nextPlannedFrame(t, m, s, clock)
	if !s.Positions["BTCUSDT"].Time.Equal(broker.fillTime) || s.Daily["BTCUSDT"].Day != broker.fillTime.Format("2006-01-02") {
		t.Fatal("receipt timestamp was replaced by polling/reconciliation time")
	}
}

func TestPlannedSessionTerminalPartialEntryUsesConfirmedQuantity(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			m, s, clock, _, broker := plannedHarness(t, "live", side)
			broker.status, broker.fraction = "CANCELED", .4
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			nextPlannedFrame(t, m, s, clock)
			requested, _ := strconv.ParseFloat(broker.orders[0].Quantity, 64)
			p := s.Positions["BTCUSDT"]
			if s.Pending != nil || math.Abs(p.Quantity-requested*.4) > 1e-10 || math.Abs(math.Abs(broker.actual)-p.Quantity) > 1e-10 || math.Abs(p.InitialRisk-4) > 1e-10 || p.StopPrice != sessionDecisions(s)[1].Plan.StopPrice {
				t.Fatal("partial terminal entry lost actual exposure/protection", p)
			}
			out, e := m.Command(context.Background(), s.ID, "reconcile")
			if e != nil || broker.lookups != 0 || out.Daily["BTCUSDT"].Count != 1 || len(sessionFills(&out)) != 1 {
				t.Fatal("partial terminal entry was counted twice", e)
			}
		})
	}
}

func TestPlannedSessionFailedEmergencyExitRetainsStateForReconcile(t *testing.T) {
	for _, kind := range []string{"lost_receipt", "rejected"} {
		t.Run(kind, func(t *testing.T) {
			m, s, clock, _, broker := plannedHarness(t, "live", "long")
			broker.fillPrice = 102 // actual entry exceeds its planned risk budget.
			broker.failExit = kind == "lost_receipt"
			if kind == "rejected" {
				broker.exitStatus = "REJECTED"
			}
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			clock.at = clock.at.Add(15 * time.Minute)
			if e := m.tick(context.Background(), s, nil); e == nil {
				t.Fatal("unsuccessful exit reported success")
			}
			var saved Session
			if e := m.App.Store.Get("sessions", s.ID, &saved); e != nil {
				t.Fatal(e)
			}
			if saved.Positions["BTCUSDT"].Entry != 102 || saved.Positions["BTCUSDT"].StopPrice != 90 || saved.EntryViolations["BTCUSDT"] == "" || saved.Daily["BTCUSDT"].Count != 1 || len(sessionFills(&saved)) != 1 {
				t.Fatal("failed emergency exit erased exposure/audit")
			}
			if kind == "lost_receipt" && (saved.Pending == nil || !saved.Pending.Order.ReduceOnly) {
				t.Fatal("unknown exit lost durable intent")
			}
			if kind == "rejected" && saved.Pending != nil {
				t.Fatal("confirmed rejection retained uncertain intent")
			}
			clock.at = clock.at.Add(30 * time.Second)
			broker.exitStatus = ""
			out, e := m.Command(context.Background(), s.ID, "reconcile")
			if e != nil || out.Pending != nil || out.Positions["BTCUSDT"].Side != "" || out.EntryViolations["BTCUSDT"] != "" || math.Abs(broker.actual) > 1e-10 || len(sessionTrades(&out)) != 1 || out.Daily["BTCUSDT"].Count != 1 {
				t.Fatal("failed to reconcile emergency exit", e)
			}
			wantOrders := 2
			if kind == "rejected" {
				wantOrders = 3
			}
			if len(broker.orders) != wantOrders {
				t.Fatal("uncertain order retried")
			}
			for _, order := range broker.orders[1:] {
				if !order.ReduceOnly {
					t.Fatal("recovery submitted a new entry")
				}
			}
		})
	}
}

func TestPlannedSessionRejectedManualCloseKeepsPositionPaused(t *testing.T) {
	m, s, clock, _, broker := plannedHarness(t, "live", "long")
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
	nextPlannedFrame(t, m, s, clock)
	broker.exitStatus = "REJECTED"
	out, e := m.Command(context.Background(), s.ID, "close")
	if e == nil || out.Status != "paused" || out.Positions["BTCUSDT"].Side != "long" || out.Pending != nil || len(sessionTrades(&out)) != 0 {
		t.Fatal("rejected close was recorded as a stopped/flat session", e)
	}
}
