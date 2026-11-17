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

type frameClock struct{ at time.Time }

func (c *frameClock) now() time.Time { return c.at }

type frameFeed struct {
	bars     []core.Candle
	requests []candleRange
	after    func()
	corrupt  func([]core.Candle, time.Time, time.Time) []core.Candle
}

func (f *frameFeed) Symbols(context.Context) ([]string, error) { return []string{"BTCUSDT"}, nil }
func (f *frameFeed) Candles(_ context.Context, _, _ string, from, to time.Time) ([]core.Candle, error) {
	f.requests = append(f.requests, candleRange{from, to})
	out := []core.Candle{}
	for _, b := range f.bars {
		if !b.Time.Before(from) && b.Time.Before(to) {
			out = append(out, b)
		}
	}
	if f.corrupt != nil {
		out = f.corrupt(out, from, to)
	}
	if f.after != nil {
		f.after()
	}
	return out, nil
}

type frameBroker struct {
	clock        *frameClock
	feed         *frameFeed
	actual       float64
	orders       []execution.Order
	quotes       int
	onQuote      func(int)
	onPrepare    func()
	onCheck      func()
	beforeSubmit func(execution.Order)
}

func (b *frameBroker) price() float64 {
	at := b.clock.at.Truncate(15 * time.Minute)
	for _, bar := range b.feed.bars {
		if bar.Time.Equal(at) {
			return bar.Open
		}
	}
	return 100
}
func (b *frameBroker) Quote(context.Context, string) (float64, error) {
	b.quotes++
	if b.onQuote != nil {
		b.onQuote(b.quotes)
	}
	return b.price(), nil
}
func (b *frameBroker) Position(context.Context, string) (float64, error) { return b.actual, nil }
func (b *frameBroker) CheckAccount(context.Context, []string) error {
	if b.onCheck != nil {
		b.onCheck()
	}
	return nil
}
func (b *frameBroker) Prepare(_ context.Context, o execution.Order, _ float64) (execution.Order, error) {
	if b.onPrepare != nil {
		b.onPrepare()
	}
	return o, nil
}
func (b *frameBroker) Submit(_ context.Context, o execution.Order) (execution.Receipt, error) {
	if b.beforeSubmit != nil {
		b.beforeSubmit(o)
	}
	b.orders = append(b.orders, o)
	qty, e := strconv.ParseFloat(o.Quantity, 64)
	if e != nil {
		return execution.Receipt{}, e
	}
	if o.Side == "SELL" {
		b.actual -= qty
	} else {
		b.actual += qty
	}
	return execution.Receipt{ClientID: o.ClientID, Status: "FILLED", ExecutedQty: o.Quantity, AvgPrice: strconv.FormatFloat(b.price(), 'g', -1, 64)}, nil
}
func (b *frameBroker) Lookup(context.Context, execution.Order) (execution.Receipt, error) {
	return execution.Receipt{}, fmt.Errorf("unexpected lookup")
}
func frameBars(n int) []core.Candle {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]core.Candle, n)
	previous := 100.0
	for i := range out {
		close := 100 + 8*math.Sin(float64(i)*.31) + 3*math.Sin(float64(i)*.07)
		out[i] = core.Candle{Time: start.Add(time.Duration(i) * 15 * time.Minute), Open: previous, High: math.Max(previous, close) + 1, Low: math.Min(previous, close) - 1, Close: close, Volume: 10}
		previous = close
	}
	return out
}
func frameStrategy() core.Strategy {
	return core.Strategy{Name: "frame contract", Indicators: []core.Indicator{{ID: "ema", Type: "ema", Period: 3}, {ID: "h4", Type: "ema", Period: 3, Timeframe: "4h"}, {ID: "daily", Type: "ohlc", Timeframe: "1d"}, {ID: "adr", Type: "adr", Period: 2, Timeframe: "1d"}, {ID: "atr", Type: "atr", Period: 3, Timeframe: "1d"}, {ID: "pivot", Type: "pivot", Left: 1, Right: 1, Timeframe: "4h"}}, Risk: core.Risk{PositionFraction: .2}, Rules: core.Rules{
		EnterLong: core.Compare("close", "gt", "ema.value"), ExitLong: core.Compare("close", "lt", "ema.value"),
		EnterShort: core.Compare("close", "lt", "ema.value"), ExitShort: core.Compare("close", "gt", "ema.value"),
	}}
}
func newFrameSession(t *testing.T, mode string, strategy core.Strategy, bars []core.Candle, at time.Time) (*Sessions, *Session, *frameClock, *frameFeed, *frameBroker) {
	t.Helper()
	app := testApp(t)
	clock := &frameClock{at: at}
	feed := &frameFeed{bars: append([]core.Candle(nil), bars...)}
	broker := &frameBroker{clock: clock, feed: feed}
	app.Sources["frames"] = feed
	m := &Sessions{App: app, now: clock.now, LiveEnabled: true, Brokers: map[string]execution.Broker{"frames": broker}}
	s := &Session{ID: "frame-session", Status: "running", Request: SessionRequest{Mode: mode, Exchange: "frames", Interval: "15m", Symbols: []string{"BTCUSDT"}, Capital: 1000, MaxOrderNotional: 10000, FeeBPS: 4}, Strategy: core.Version{Strategy: strategy}, Positions: map[string]core.Position{}, Balances: map[string]float64{"BTCUSDT": 1000}, LastCandle: map[string]time.Time{}, Previous: map[string]map[string]float64{}}
	broker.beforeSubmit = func(o execution.Order) {
		var saved Session
		if e := app.Store.Get("sessions", s.ID, &saved); e != nil || saved.Pending == nil || saved.Pending.Order.ClientID != o.ClientID {
			t.Fatal("order lacks durable intent", e)
		}
	}
	return m, s, clock, feed, broker
}
func sessionDecisions(s *Session) []core.Decision {
	out := []core.Decision{}
	for _, e := range s.Events {
		if e.Decision != nil {
			out = append(out, *e.Decision)
		}
	}
	return out
}
func sessionFills(s *Session) []core.Fill {
	out := []core.Fill{}
	for _, e := range s.Events {
		if e.Fill != nil {
			out = append(out, *e.Fill)
		}
	}
	return out
}
func alwaysEnter() core.Strategy {
	s := frameStrategy()
	s.Rules = core.Rules{EnterLong: &core.Rule{Left: core.Field("close"), Op: "gt", Right: core.Constant(0)}, ExitLong: &core.Rule{Left: core.Field("close"), Op: "lt", Right: core.Constant(0)}}
	return s
}

func TestSessionDoesNotEnterAtThirtySeconds(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		t.Run(mode, func(t *testing.T) {
			bars := frameBars(800)
			frame := bars[600].Time
			m, s, clock, _, broker := newFrameSession(t, mode, alwaysEnter(), bars, frame.Add(30*time.Second))
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			if len(sessionFills(s)) != 0 || len(broker.orders) != 0 {
				t.Fatal("late signal entered on the 30th second")
			}
			if ds := sessionDecisions(s); len(ds) != 1 || ds[0].Action != "hold" {
				t.Fatal("late signal lacks hold audit")
			}
			clock.at = frame.Add(15 * time.Minute)
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			if len(sessionFills(s)) != 1 {
				t.Fatal("valid boundary did not execute")
			}
			clock.at = clock.at.Add(30 * time.Second)
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			if len(sessionFills(s)) != 1 || len(sessionDecisions(s)) != 2 {
				t.Fatal("intrabar tick reevaluated or duplicated the signal")
			}
		})
	}
}

func TestSessionAndBacktestTimeframeParity(t *testing.T) {
	bars := frameBars(900)
	strategy := frameStrategy()
	first, count := 600, 112
	warmup, e := strategy.WarmupBars("15m")
	if e != nil {
		t.Fatal(e)
	}
	input := bars[first-warmup:]
	request := core.BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "15m", From: bars[first].Time, To: bars[first+count+2].Time, Capital: 1000, FeeBPS: 4}
	baseline, e := core.Backtest(context.Background(), strategy, request, "BTCUSDT", input, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"paper", "live"} {
		t.Run(mode, func(t *testing.T) {
			m, s, clock, feed, broker := newFrameSession(t, mode, strategy, bars, bars[first+1].Time)
			for j := 0; j < count; j++ {
				clock.at = bars[first+j+1].Time
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatalf("frame %d: %v", j, e)
				}
				got := sessionDecisions(s)[j]
				want := baseline.Decisions[j]
				if !reflect.DeepEqual(got.Features, want.Features) {
					for key, v := range want.Features {
						if got.Features[key] != v {
							t.Fatalf("frame %d %s differs: session=%g backtest=%g", j, key, got.Features[key], v)
						}
					}
					t.Fatalf("frame %d feature availability differs", j)
				}
				if got.Action != want.Action || got.Position != want.Position || !reflect.DeepEqual(got.Rules, want.Rules) || !got.Time.Equal(want.Time) {
					t.Fatalf("frame %d decision differs: session=%+v backtest=%+v", j, got, want)
				}
				if j == 16 {
					// Persistence/resume must keep the original indicator seed and raw input.
					encoded, _ := json.Marshal(s)
					var recovered Session
					if e := json.Unmarshal(encoded, &recovered); e != nil {
						t.Fatal(e)
					}
					*s = recovered
					// Changes to already accepted source history cannot reseed indicators.
					feed.bars[first-1].High = 5000
					feed.bars[first-1].Close = 4999
				}
				beforeEvents, beforeOrders := len(s.Events), len(broker.orders)
				clock.at = clock.at.Add(30 * time.Second)
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				if len(s.Events) != beforeEvents || len(broker.orders) != beforeOrders {
					t.Fatal("intrabar quote produced a decision/order")
				}
			}
			fills := sessionFills(s)
			wantFills := []core.Fill{}
			end := bars[first+count].Time
			for _, f := range baseline.Fills {
				if !f.Time.After(end) {
					wantFills = append(wantFills, f)
				}
			}
			if len(fills) != len(wantFills) || len(fills) < 4 {
				t.Fatalf("fills differ: got %d want %d", len(fills), len(wantFills))
			}
			sides := map[string]bool{}
			for i, f := range fills {
				w := wantFills[i]
				sides[f.Action] = true
				if f.Action != w.Action || !f.Time.Equal(w.Time) || math.Abs(f.Price-w.Price) > 1e-9 || math.Abs(f.Quantity-w.Quantity) > 1e-9 {
					t.Fatalf("fill %d differs: %+v vs %+v", i, f, w)
				}
			}
			if !sides["enter_long"] || !sides["enter_short"] {
				t.Fatal("both entry directions must be exercised")
			}
			if len(feed.requests) != count {
				t.Fatal("unexpected history request count")
			}
			for i := 1; i < len(feed.requests); i++ {
				if feed.requests[i].from != feed.requests[i-1].to {
					t.Fatal("session reloaded an overlapping indicator window")
				}
			}
		})
	}
}

func TestSessionHigherTimeframesAreUnavailableUntilTheirClose(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		for _, tf := range []string{"4h", "1d"} {
			t.Run(mode+"/"+tf, func(t *testing.T) {
				bars := frameBars(900)
				duration, _ := core.Interval(tf)
				boundary := bars[672].Time.Truncate(duration).Add(duration)
				spikeIndex := int(boundary.Sub(bars[0].Time)/(15*time.Minute)) - 1
				bars[spikeIndex].High = 900
				strategy := alwaysEnter()
				strategy.Indicators = []core.Indicator{{ID: "context", Type: "ohlc", Timeframe: tf}}
				strategy.Rules.EnterLong = &core.Rule{Left: core.Field("context.high"), Op: "gt", Right: core.Constant(500)}
				m, s, clock, feed, broker := newFrameSession(t, mode, strategy, bars, boundary.Add(-15*time.Minute))
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				ds := sessionDecisions(s)
				before := ds[0]
				if before.Action != "hold" || before.Features["context.high"] >= 500 || before.Features["context.available_at"] != float64(boundary.Add(-duration).Unix()) {
					t.Fatalf("unclosed %s leaked: %+v", tf, before)
				}
				if len(broker.orders) > 0 || len(sessionFills(s)) > 0 {
					t.Fatal("entered using a future context candle")
				}
				// Both current quotes and the forming source bar may move; neither can
				// re-evaluate an already consumed frame or affect its recorded features.
				encoded, _ := json.Marshal(before)
				feed.bars[spikeIndex].High = 1000
				clock.at = clock.at.Add(30 * time.Second)
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				unchanged, _ := json.Marshal(sessionDecisions(s)[0])
				if string(encoded) != string(unchanged) || len(sessionDecisions(s)) != 1 || len(sessionFills(s)) > 0 {
					t.Fatal("intrabar information changed the decision")
				}
				clock.at = boundary
				if e := m.tick(context.Background(), s, nil); e != nil {
					t.Fatal(e)
				}
				ds = sessionDecisions(s)
				after := ds[1]
				if after.Action != "enter_long" || after.Features["context.high"] != 1000 || after.Features["context.available_at"] != float64(boundary.Unix()) || len(sessionFills(s)) != 1 {
					t.Fatalf("closed context did not become available exactly at boundary: %+v", after)
				}
				for _, q := range feed.requests {
					if q.to.After(boundary) {
						t.Fatal("requested future candles")
					}
				}
			})
		}
	}
}

func TestSessionRejectsUnclosedOrOutOfRangeSourceBars(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		for _, kind := range []string{"forming", "future", "older", "gap", "shifted"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				bars := frameBars(800)
				m, s, _, feed, broker := newFrameSession(t, mode, alwaysEnter(), bars, bars[600].Time)
				feed.corrupt = func(c []core.Candle, from, to time.Time) []core.Candle {
					switch kind {
					case "forming", "future":
						b := c[len(c)-1]
						b.Time = to
						if kind == "future" {
							b.Time = to.Add(15 * time.Minute)
						}
						return append(c, b)
					case "older":
						b := c[0]
						b.Time = from.Add(-15 * time.Minute)
						return append([]core.Candle{b}, c...)
					case "gap":
						return c[1:]
					default:
						c[len(c)-1].Time = c[len(c)-1].Time.Add(time.Second)
						return c
					}
				}
				if e := m.tick(context.Background(), s, nil); e == nil {
					t.Fatal("invalid source response accepted")
				}
				if len(s.Events) != 0 || len(broker.orders) != 0 || len(s.InputData) != 0 || !s.LastCandle["BTCUSDT"].IsZero() {
					t.Fatal("rejected data affected state or trading")
				}
			})
		}
	}
}

func TestSessionLateProcessingCannotSubmitAnOrder(t *testing.T) {
	for _, mode := range []string{"paper", "live"} {
		for _, stage := range []string{"data", "llm", "quote", "prepare", "account", "durable_intent"} {
			if mode == "paper" && (stage == "prepare" || stage == "account" || stage == "durable_intent") {
				continue
			}
			t.Run(mode+"/"+stage, func(t *testing.T) {
				bars := frameBars(800)
				frame := bars[600].Time
				strategy := alwaysEnter()
				if stage == "llm" {
					strategy.LLM = &core.LLMConfig{Provider: "ollama", Model: "test", Lookback: 2}
					strategy.Rules.EnterLong = &core.Rule{Left: core.Field("llm.enter_long"), Op: "gt", Right: core.Constant(.5)}
				}
				m, s, clock, feed, broker := newFrameSession(t, mode, strategy, bars, frame)
				late := func() { clock.at = frame.Add(30 * time.Second) }
				var enrich core.Enricher
				switch stage {
				case "data":
					feed.after = late
				case "quote":
					broker.onQuote = func(n int) {
						if n == 2 {
							late()
						}
					}
				case "prepare":
					broker.onPrepare = late
				case "account":
					broker.onCheck = late
				case "durable_intent":
					m.now = func() time.Time {
						if s.Pending != nil {
							late()
						}
						return clock.at
					}
				case "llm":
					enrich = func(_ context.Context, c []core.Candle, f map[string]float64, _, _ string) (*core.LLMAudit, error) {
						if !c[len(c)-1].Time.Add(15 * time.Minute).Equal(frame) {
							t.Fatal("LLM received unclosed bars")
						}
						for key, v := range f {
							if len(key) > 13 && key[len(key)-13:] == ".available_at" && v > float64(frame.Unix()) {
								t.Fatal("LLM received future context")
							}
						}
						late()
						return &core.LLMAudit{Verdict: core.Verdict{EnterLong: 1}}, nil
					}
				}
				if e := m.tick(context.Background(), s, enrich); e != nil {
					t.Fatal(e)
				}
				if len(broker.orders) != 0 || len(sessionFills(s)) != 0 || s.Pending != nil {
					t.Fatal("late processing dispatched an order")
				}
				ds := sessionDecisions(s)
				if len(ds) != 1 || ds[0].Action != "hold" || ds[0].Reason != missedSessionFrameReason || !ds[0].Time.Equal(frame) {
					t.Fatal("missing late-decision audit", ds)
				}
				var saved Session
				if e := m.App.Store.Get("sessions", s.ID, &saved); e != nil || saved.Pending != nil || sessionDecisions(&saved)[0].Action != "hold" {
					t.Fatal("late order intent/decision persisted incorrectly", e)
				}
			})
		}
	}
}

func TestSessionFreezesBoundaryBeforeIO(t *testing.T) {
	bars := frameBars(800)
	boundary := bars[600].Time
	m, s, clock, feed, broker := newFrameSession(t, "live", alwaysEnter(), bars, boundary.Add(-time.Millisecond))
	broker.onQuote = func(n int) {
		if n == 1 {
			clock.at = boundary
		}
	}
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
	previous := boundary.Add(-15 * time.Minute)
	if len(feed.requests) != 1 || !feed.requests[0].to.Equal(previous) || !sessionDecisions(s)[0].Time.Equal(previous) || len(broker.orders) != 0 {
		t.Fatal("I/O advanced the information boundary")
	}
	if e := m.tick(context.Background(), s, nil); e != nil {
		t.Fatal(e)
	}
	if len(broker.orders) != 1 || !sessionDecisions(s)[1].Time.Equal(boundary) {
		t.Fatal("next boundary was not evaluated")
	}
}

func TestSessionFrameWindowAndSchedulerBoundaries(t *testing.T) {
	frame := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		offset time.Duration
		want   bool
	}{{-time.Nanosecond, false}, {0, true}, {5*time.Second - time.Nanosecond, true}, {5 * time.Second, false}, {30 * time.Second, false}, {15 * time.Minute, false}} {
		m := &Sessions{now: func() time.Time { return frame.Add(tc.offset) }}
		if got := m.withinSessionFrame(frame); got != tc.want {
			t.Fatalf("offset %s: %v", tc.offset, got)
		}
	}
	for _, offset := range []time.Duration{-time.Nanosecond, 0, 17 * time.Second, 30 * time.Second, 15*time.Minute - time.Nanosecond} {
		at := frame.Add(offset)
		next := nextSessionPoll(at)
		if !next.After(at) || next.Sub(at) > 5*time.Second || next.UnixNano()%int64(5*time.Second) != 0 {
			t.Fatal("poll is not UTC aligned", at, next)
		}
	}
}

func TestSessionRiskExitRemainsActiveBetweenFrames(t *testing.T) {
	bars := frameBars(800)
	frame := bars[600].Time
	bars[600].Open = 80
	for _, mode := range []string{"paper", "live"} {
		t.Run(mode, func(t *testing.T) {
			strategy := alwaysEnter()
			strategy.Risk.StopLoss = .1
			m, s, _, _, broker := newFrameSession(t, mode, strategy, bars, frame.Add(30*time.Second))
			s.Positions["BTCUSDT"] = core.Position{Side: "long", Entry: 100, Quantity: 1, Time: frame.Add(-time.Hour)}
			broker.actual = 1
			if e := m.tick(context.Background(), s, nil); e != nil {
				t.Fatal(e)
			}
			fills := sessionFills(s)
			if len(fills) != 1 || fills[0].Action != "exit_long" || fills[0].Reason != "stop_loss" {
				t.Fatal("frame gate disabled protection", fills)
			}
		})
	}
}
