package core

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"
)

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("got %.12f want %.12f", got, want)
	}
}
func TestIndicatorsCausalAndGolden(t *testing.T) {
	c := DemoCandles(100)
	ind := []Indicator{{ID: "ema", Type: "ema", Period: 3}, {ID: "sma", Type: "sma", Period: 3}, {ID: "rsi", Type: "rsi", Period: 14}, {ID: "atr", Type: "atr", Period: 14}, {ID: "aroon", Type: "aroon", Period: 25}, {ID: "cloud", Type: "ichimoku", Fast: 9, Slow: 26, Period: 52, Displacement: 26}, {ID: "macd", Type: "macd", Fast: 12, Slow: 26, Signal: 9}, {ID: "bb", Type: "bollinger", Period: 20, Multiplier: 2}}
	all := Features(c, ind)
	prefix := Features(c[:150], ind)
	for i := range prefix {
		if !reflect.DeepEqual(prefix[i], all[i]) {
			t.Fatalf("future data changed features at %d", i)
		}
	}
	if _, ok := all[76]["cloud.span_b"]; ok {
		t.Fatal("cloud was available before its displaced warmup")
	}
	near(t, all[77]["cloud.span_b"], midpoint(c, 52)[51])
	near(t, all[2]["ema.value"], (c[0].Close+c[1].Close+c[2].Close)/3)
	near(t, all[3]["ema.value"], (all[2]["ema.value"]+c[3].Close)/2)
	flat := make([]Candle, 50)
	for i := range flat {
		flat[i] = Candle{Open: 100, Close: 100, High: 101, Low: 99}
	}
	f := Features(flat, ind)
	near(t, f[14]["rsi.value"], 50)
	near(t, f[25]["aroon.up"], 100)
	near(t, f[25]["aroon.down"], 100)
	near(t, f[14]["atr.value"], 2)
}
func TestRulesMissingNegationAndCross(t *testing.T) {
	rule := &Rule{Not: Compare("fast.value", "gt", "slow.value")}
	v := Evaluate(rule, map[string]float64{"fast.value": 5}, nil)
	if v.Ready || v.Result {
		t.Fatal("missing data became true through NOT")
	}
	cross := Compare("fast.value", "crosses_above", "slow.value")
	v = Evaluate(cross, map[string]float64{"fast.value": 3, "slow.value": 2}, map[string]float64{"fast.value": 2, "slow.value": 2})
	if !v.Result {
		t.Fatal("cross expected")
	}
	v = Evaluate(cross, map[string]float64{"fast.value": 3, "slow.value": 2}, nil)
	if v.Ready || v.Result {
		t.Fatal("cross requires previous values")
	}
	s := DemoStrategy()
	s.Rules.EnterShort = s.Rules.EnterLong
	f := map[string]float64{"fast.value": 3, "slow.value": 2, "trend.up": 100}
	d := Decide(s, f, nil, "")
	if d.Action != "hold" || d.Reason != "conflicting long and short signals" {
		t.Fatalf("conflict: %+v", d)
	}
}
func TestInvalidStrategies(t *testing.T) {
	for name, mutate := range map[string]func(*Strategy){"unknown field": func(s *Strategy) { s.Rules.EnterLong = Compare("future", "gt", "close") }, "empty group": func(s *Strategy) { s.Rules.EnterLong = &Rule{All: []Rule{}} }, "mixed nodes": func(s *Strategy) { s.Rules.EnterLong = &Rule{Not: Compare("close", "gt", "open"), Op: "gt"} }, "bad period": func(s *Strategy) { s.Indicators[0].Period = 0 }, "duplicate": func(s *Strategy) { s.Indicators[1].ID = s.Indicators[0].ID }, "no exit": func(s *Strategy) { s.Rules.ExitLong = nil }} {
		t.Run(name, func(t *testing.T) {
			s := DemoStrategy()
			mutate(&s)
			if ValidateStrategy(s) == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if e := ValidateStrategy(DemoStrategy()); e != nil {
		t.Fatal(e)
	}
}
func plainStrategy() Strategy {
	return Strategy{Name: "test", Rules: Rules{EnterLong: &Rule{Left: Field("close"), Op: "gt", Right: Constant(0)}, ExitLong: &Rule{Left: Field("close"), Op: "lt", Right: Constant(0)}}, Risk: Risk{PositionFraction: 1}}
}
func TestNextOpenFeesAndRange(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []Candle{{Time: start, Open: 100, High: 106, Low: 99, Close: 105}, {Time: start.Add(time.Hour), Open: 110, High: 116, Low: 109, Close: 115}, {Time: start.Add(2 * time.Hour), Open: 120, High: 126, Low: 119, Close: 125}}
	r := BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "1h", From: start, To: start.Add(3 * time.Hour), Capital: 1000, FeeBPS: 10, SlippageBPS: 10}
	out, e := Backtest(context.Background(), plainStrategy(), r, "BTCUSDT", bars, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Trades) != 1 {
		t.Fatalf("trades %d", len(out.Trades))
	}
	tr := out.Trades[0]
	near(t, tr.Entry, 110*1.001)
	near(t, tr.Exit, 125*.999)
	if !tr.EntryTime.Equal(bars[1].Time) {
		t.Fatal("executed on signal candle")
	}
	near(t, tr.PnL, (tr.Exit-tr.Entry)*tr.Quantity-tr.Fees)
	near(t, out.Metrics.Final, 1000+tr.PnL)
	if out.Decisions[0].Action != "enter_long" {
		t.Fatal("entry signal missing")
	}
	_, e = Backtest(context.Background(), plainStrategy(), r, "BTCUSDT", []Candle{bars[0], bars[2]}, nil)
	if e == nil {
		t.Fatal("gap silently accepted")
	}
}
func TestRiskStopsGapAndAmbiguity(t *testing.T) {
	cases := []struct {
		name, side string
		bar        Candle
		price      float64
		reason     string
	}{
		{"both long", "long", Candle{Open: 100, High: 120, Low: 80}, 90, "stop_loss"}, {"gap long", "long", Candle{Open: 70, High: 80, Low: 60}, 70, "stop_loss"}, {"both short", "short", Candle{Open: 100, High: 120, Low: 80}, 110, "stop_loss"}, {"gap short", "short", Candle{Open: 130, High: 140, Low: 120}, 130, "stop_loss"}, {"take long", "long", Candle{Open: 120, High: 125, Low: 115}, 120, "take_profit"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, r := RiskExit(Position{Side: c.side, Entry: 100}, c.bar, Risk{StopLoss: .1, TakeProfit: .1})
			near(t, v, c.price)
			if r != c.reason {
				t.Fatal(r)
			}
		})
	}
}
func TestLLMReceivesOnlyPast(t *testing.T) {
	c := DemoCandles(100)[:20]
	s := plainStrategy()
	s.LLM = &LLMConfig{Provider: "ollama", Model: "fake", Lookback: 2}
	s.Rules.EnterLong = &Rule{Left: Field("llm.enter_long"), Op: "gte", Right: Constant(.5)}
	s.Rules.ExitLong = &Rule{Left: Field("llm.exit_long"), Op: "gte", Right: Constant(.5)}
	r := BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "1h", From: c[4].Time, To: c[10].Time, Capital: 1000}
	calls := 0
	enrich := func(ctx context.Context, bars []Candle, f map[string]float64, symbol, side string) (*LLMAudit, error) {
		if !bars[len(bars)-1].Time.Equal(c[4+calls].Time) {
			t.Fatal("future candles leaked")
		}
		calls++
		return &LLMAudit{Verdict: Verdict{EnterLong: .9, Reason: "test"}}, nil
	}
	if _, e := Backtest(context.Background(), s, r, "BTCUSDT", c, enrich); e != nil {
		t.Fatal(e)
	}
	if calls != 6 {
		t.Fatalf("calls %d", calls)
	}
}
