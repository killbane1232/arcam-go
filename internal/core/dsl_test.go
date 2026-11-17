package core

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func hourlyBars(n int) []Candle {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]Candle, n)
	for i := range bars {
		v := 100 + float64(i/24)
		bars[i] = Candle{Time: start.Add(time.Duration(i) * time.Hour), Open: v, High: v + 2, Low: v - 2, Close: v + 1, Volume: 10}
	}
	return bars
}
func expr(op string, args ...*Operand) *Operand {
	o := &Operand{Op: op}
	for _, a := range args {
		o.Args = append(o.Args, *a)
	}
	return o
}
func cmp(a *Operand, op string, b *Operand) *Rule { return &Rule{Left: a, Op: op, Right: b} }
func requestFor(c []Candle) BacktestRequest {
	return BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "1h", From: c[0].Time, To: c[len(c)-1].Time.Add(time.Hour), Capital: 1000}
}

func TestContextCausalityAvailabilityAndDailyReset(t *testing.T) {
	c := hourlyBars(24 * 18)
	s := plainStrategy()
	s.Indicators = []Indicator{{ID: "d", Type: "ohlc", Timeframe: "1d"}, {ID: "adr", Type: "adr", Period: 5, Timeframe: "1d"}, {ID: "atr", Type: "atr", Period: 14, Timeframe: "1d"}, {ID: "h", Type: "sma", Period: 2, Timeframe: "4h"}}
	all, e := BuildFeatures(c, s, "1h")
	if e != nil {
		t.Fatal(e)
	}
	for _, cut := range []int{1, 23, 24, 25, 119, 120, 359, 360, 401} {
		prefix, e := BuildFeatures(c[:cut], s, "1h")
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(prefix, all[:cut]) {
			t.Fatalf("future leak at prefix %d", cut)
		}
	}
	for _, key := range []string{"d.close", "previous_day.high", "h.value"} {
		if _, ok := all[2][key]; ok {
			t.Fatalf("premature %s", key)
		}
	}
	near(t, all[23]["d.close"], 101)
	near(t, all[23]["d.available_at"], float64(c[24].Time.Unix()))
	near(t, all[24]["previous_day.high"], 102)
	near(t, all[24]["day.high"], 103)
	near(t, all[24]["day.low"], 99)
	if _, ok := all[118]["adr.value"]; ok {
		t.Fatal("ADR included unfinished fifth day")
	}
	near(t, all[119]["adr.value"], 4)
	if _, ok := all[358]["atr.value"]; ok {
		t.Fatal("ATR before 14 daily changes")
	}
	near(t, all[359]["atr.value"], 4)
	changed := append([]Candle(nil), c...)
	for i := 120; i < len(changed); i++ {
		changed[i].High += 200
	}
	revised, _ := BuildFeatures(changed, s, "1h")
	if !reflect.DeepEqual(revised[:120], all[:120]) {
		t.Fatal("future changes affected historical features")
	}
	partial, _ := BuildFeatures(c[1:25], s, "1h")
	for _, row := range partial {
		if _, ok := row["d.close"]; ok {
			t.Fatal("partial day accepted")
		}
	}
	if _, ok := partial[0]["day.high"]; ok {
		t.Fatal("partial intraday range accepted")
	}
	gapped := append(append([]Candle{}, c[:6]...), c[7:12]...)
	gf, _ := BuildFeatures(gapped, s, "1h")
	if _, ok := gf[len(gf)-1]["h.value"]; ok {
		t.Fatal("indicator bridged missing context bucket")
	}
}

func TestPivotConfirmationAndOffsetsArithmetic(t *testing.T) {
	c := hourlyBars(7)
	for i, h := range []float64{101, 103, 108, 104, 102, 101, 100} {
		c[i].High = h
	}
	s := plainStrategy()
	s.Indicators = []Indicator{{ID: "p", Type: "pivot", Left: 2, Right: 2}}
	s.Rules.EnterLong = cmp(expr("div", expr("sub", Field("high"), &Operand{Field: "low", Offset: 2}), Constant(2)), "gt", Constant(0))
	crossing := cmp(&Operand{Field: "high", Offset: 1}, "crosses_below", Constant(105))
	s.Rules.ExitLong = crossing
	if e := ValidateStrategy(s); e != nil {
		t.Fatal(e)
	}
	f, e := BuildFeatures(c, s, "1h")
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := f[3]["p.high"]; ok {
		t.Fatal("unconfirmed pivot")
	}
	near(t, f[4]["p.high"], 108)
	near(t, f[4]["p.high_time"], float64(c[2].Time.Unix()))
	if Evaluate(s.Rules.EnterLong, f[1], f[0]).Ready {
		t.Fatal("missing history must be unavailable")
	}
	if !Evaluate(s.Rules.EnterLong, f[4], f[3]).Result {
		t.Fatal("arithmetic/offset comparison failed")
	}
	divZero := cmp(expr("div", Constant(1), Constant(0)), "gt", Constant(0))
	if tr := Evaluate(&Rule{Not: divZero}, f[4], f[3]); tr.Ready || tr.Result {
		t.Fatal("undefined arithmetic passed NOT")
	}
	if !Evaluate(crossing, f[4], f[3]).Result {
		t.Fatal("crossing did not shift both rows")
	}
}

func setupStrategy(side string) Strategy {
	s := plainStrategy()
	s.Setups = []Setup{{ID: "retest", When: cmp(Field("close"), "gt", Constant(0)), Capture: map[string]Operand{"level": *Constant(100), "stop": *Constant(90)}, ExpiresAfter: 3}}
	entry := &Rule{All: []Rule{{Setup: "retest"}, *cmp(Field("setup.retest.age"), "gte", Constant(1))}}
	s.Rules = Rules{}
	protection := &Protection{StopPrice: Field("setup.retest.stop"), TargetPrice: Constant(110)}
	if side == "long" {
		s.Rules.EnterLong = entry
		s.Rules.ExitLong = cmp(Field("close"), "lt", Constant(0))
		s.Risk.Long = protection
	} else {
		s.Setups[0].Capture["stop"] = *Constant(110)
		protection.TargetPrice = Constant(90)
		s.Rules.EnterShort = entry
		s.Rules.ExitShort = cmp(Field("close"), "lt", Constant(0))
		s.Risk.Short = protection
	}
	s.Risk.RiskFraction = .01
	s.Risk.MaxEntriesPerDay = 2
	return s
}
func flatBars(n int) []Candle {
	c := hourlyBars(n)
	for i := range c {
		c[i].Open = 100
		c[i].Close = 100
		c[i].High = 101
		c[i].Low = 99
	}
	return c
}

func TestSetupFrozenProtectionMirrorAndResumeEveryBoundary(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			c := flatBars(8)
			c[3].High = 115
			c[3].Low = 85
			s := setupStrategy(side)
			if e := ValidateStrategy(s); e != nil {
				t.Fatal(e)
			}
			r := requestFor(c)
			want, e := Backtest(context.Background(), s, r, "BTCUSDT", c, nil)
			if e != nil {
				t.Fatal(e)
			}
			if len(want.Trades) != 1 || want.Trades[0].Reason != "stop_loss" {
				t.Fatalf("one-shot or stop priority failed: %+v", want.Trades)
			}
			near(t, want.Trades[0].Quantity, 1)
			near(t, want.Trades[0].PnLR, -1)
			if !want.Trades[0].EntryTime.Equal(c[2].Time) {
				t.Fatal("retest or next-open timing wrong")
			}
			if want.Decisions[0].Setups["retest"].Status != "waiting" || want.Decisions[1].Setups["retest"].Status != "consumed" {
				t.Fatal("missing lifecycle audit")
			}
			for cut := 1; cut < len(c); cut++ {
				ctx, cancel := context.WithCancel(context.Background())
				prefix, e := BacktestWithUpdates(ctx, s, r, "BTCUSDT", c, nil, func(p PairResult) {
					if len(p.Candles) == cut {
						cancel()
					}
				})
				cancel()
				if e == nil {
					t.Fatal("expected interruption")
				}
				data, _ := json.Marshal(prefix)
				var saved PairResult
				if e = json.Unmarshal(data, &saved); e != nil {
					t.Fatal(e)
				}
				got, e := ResumeBacktest(context.Background(), s, r, "BTCUSDT", c, saved, nil, nil)
				if e != nil {
					t.Fatalf("cut %d: %v", cut, e)
				}
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				if string(gotJSON) != string(wantJSON) {
					t.Fatalf("cut %d resumed state differs", cut)
				}
				after, _ := json.Marshal(saved)
				if string(after) != string(data) {
					t.Fatal("saved prefix mutated")
				}
			}
		})
	}
}

func TestSetupExpiryCancelKeysAndFrozenReference(t *testing.T) {
	for _, mode := range []string{"expiry", "cancel", "same_key", "capture"} {
		t.Run(mode, func(t *testing.T) {
			s := setupStrategy("long")
			s.Setups[0].ExpiresAfter = 1
			s.Setups[0].Key = Constant(7)
			s.Rules.EnterLong = cmp(Field("close"), "lt", Constant(0))
			c := flatBars(6)
			if mode == "cancel" {
				s.Setups[0].Cancel = cmp(Field("setup.retest.age"), "gte", Constant(1))
			}
			if mode == "capture" {
				s.Setups[0].Capture["level"] = *Field("high")
				c[1].High = 200
			}
			out, e := Backtest(context.Background(), s, requestFor(c), "BTCUSDT", c, nil)
			if e != nil {
				t.Fatal(e)
			}
			state := out.Setups["retest"]
			expected := "expired"
			if mode == "cancel" {
				expected = "cancelled"
			}
			if state.Status != expected || !state.ReferenceTime.Equal(c[0].Time.Add(time.Hour)) {
				t.Fatalf("rearmed old event: %+v", state)
			}
			if mode == "capture" {
				near(t, state.Captured["level"], 101)
			}
		})
	}
}

func TestExecutionGapsLimitsCostsAndDailyReset(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		for _, kind := range []string{"stop_gap", "target_gap", "notional", "distance", "costs", "missing"} {
			t.Run(side+"/"+kind, func(t *testing.T) {
				c := flatBars(5)
				s := setupStrategy(side)
				r := requestFor(c)
				switch kind {
				case "stop_gap":
					if side == "long" {
						c[2] = Candle{Time: c[2].Time, Open: 80, High: 81, Low: 79, Close: 80}
					} else {
						c[2] = Candle{Time: c[2].Time, Open: 120, High: 121, Low: 119, Close: 120}
					}
				case "target_gap":
					if side == "long" {
						c[2] = Candle{Time: c[2].Time, Open: 120, High: 121, Low: 119, Close: 120}
					} else {
						c[2] = Candle{Time: c[2].Time, Open: 80, High: 81, Low: 79, Close: 80}
					}
				case "notional":
					s.Risk.MaxNotional = 50
				case "distance":
					s.Risk.MaxStopFraction = .05
				case "costs":
					r.FeeBPS = 10
					r.SlippageBPS = 10
				case "missing":
					s.Indicators = []Indicator{{ID: "atr", Type: "atr", Period: 14}}
					if side == "long" {
						s.Risk.Long.StopPrice = Field("atr.value")
					} else {
						s.Risk.Short.StopPrice = Field("atr.value")
					}
				}
				out, e := Backtest(context.Background(), s, r, "BTCUSDT", c, nil)
				if e != nil {
					t.Fatal(e)
				}
				if kind == "costs" {
					if len(out.Trades) != 1 {
						t.Fatal("missing trade")
					}
					near(t, out.Trades[0].InitialRisk, 10)
					near(t, out.Trades[0].PnLR, out.Trades[0].PnL/10)
					if out.Trades[0].Fees <= 0 {
						t.Fatal("missing costs")
					}
					return
				}
				if len(out.Trades) != 0 {
					t.Fatal("invalid entry executed")
				}
				if kind == "missing" {
					if out.Decisions[1].Reason != "stop price unavailable" {
						t.Fatal(out.Decisions[1].Reason)
					}
				} else {
					if out.Decisions[2].Execution == nil || out.Decisions[2].Execution.Status != "skipped" {
						t.Fatal("missing skip audit")
					}
				}
			})
		}
	}
	c := flatBars(30)
	s := plainStrategy()
	s.Risk.StopLoss = .001
	s.Risk.MaxEntriesPerDay = 1
	out, e := Backtest(context.Background(), s, requestFor(c), "BTCUSDT", c, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Trades) != 2 {
		t.Fatalf("daily cap/reset: %d", len(out.Trades))
	}
	if out.Decisions[2].Reason != "daily entry limit" {
		t.Fatal("missing daily block audit")
	}
	if !out.Trades[1].EntryTime.Equal(c[24].Time) {
		t.Fatal("midnight limit used signal day instead of fill day")
	}
}

func TestExtendedValidation(t *testing.T) {
	for _, mutate := range []func(*Strategy){
		func(s *Strategy) { s.Rules.EnterLong.Left = &Operand{Field: "close", Offset: -1} },
		func(s *Strategy) { s.Rules.EnterLong.Left = expr("div", Constant(1)) },
		func(s *Strategy) { s.Rules.EnterLong.Left = &Operand{Field: "close", Value: Constant(1).Value} },
		func(s *Strategy) { s.Rules.EnterLong.Left = Constant(math.Inf(1)) },
		func(s *Strategy) { s.Risk.RiskFraction = .01 },
		func(s *Strategy) { s.Rules.EnterLong = &Rule{Setup: "unknown"} },
		func(s *Strategy) { s.Indicators = []Indicator{{ID: "p", Type: "pivot", Left: 1}} },
		func(s *Strategy) { s.Indicators = []Indicator{{ID: "day", Type: "ohlc"}} },
	} {
		s := plainStrategy()
		mutate(&s)
		if ValidateStrategy(s) == nil {
			t.Fatalf("accepted invalid strategy: %+v", s)
		}
	}
	s := plainStrategy()
	s.Indicators = []Indicator{{ID: "x", Type: "sma", Period: 3, Timeframe: "1h"}}
	if _, e := s.WarmupBars("4h"); e == nil {
		t.Fatal("lower context allowed")
	}
	s.Indicators[0].Timeframe = "1d"
	n, e := s.WarmupBars("15m")
	if e != nil || n < 4*96 {
		t.Fatal(n, e)
	}
}

func TestFrozenProtectionAndFutureDecisions(t *testing.T) {
	c := flatBars(10)
	c[0].Low = 90
	c[2].Low = 95
	c[3].Low = 89
	s := plainStrategy()
	s.Risk.Long = &Protection{StopPrice: Field("low"), TargetPrice: Constant(120)}
	s.Risk.RiskFraction = .01
	r := requestFor(c)
	r.To = c[5].Time
	out, e := Backtest(context.Background(), s, r, "BTCUSDT", c, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Trades) == 0 {
		t.Fatal("no trade")
	}
	near(t, out.Trades[0].StopPrice, 90)
	if !out.Trades[0].ExitTime.Equal(c[4].Time) {
		t.Fatal("stop moved with future lows")
	}
	prefix, e := Backtest(context.Background(), s, r, "BTCUSDT", c[:5], nil)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(prefix, out) {
		t.Fatal("future input changed decisions/execution")
	}
}

func TestSetupCancellationUnavailableAndImmediate(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		c := flatBars(4)
		s := setupStrategy("long")
		s.Rules.EnterLong = &Rule{Setup: "retest"}
		right := Constant(95)
		if unavailable {
			s.Indicators = []Indicator{{ID: "atr", Type: "atr", Period: 14}}
			right = Field("atr.value")
		}
		s.Setups[0].Cancel = cmp(Field("close"), "gt", right)
		out, e := Backtest(context.Background(), s, requestFor(c), "BTCUSDT", c, nil)
		if e != nil {
			t.Fatal(e)
		}
		if len(out.Trades) > 0 {
			t.Fatal("entry bypassed cancellation")
		}
		if unavailable && out.Decisions[0].Setups["retest"].Reason == "" {
			t.Fatal("missing unavailability audit")
		}
	}
}

func TestLegacyFractionalStopWithLargeSlippage(t *testing.T) {
	c := flatBars(4)
	s := plainStrategy()
	s.Risk.StopLoss = .001
	r := requestFor(c)
	r.SlippageBPS = 100
	out, e := Backtest(context.Background(), s, r, "BTCUSDT", c, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Trades) == 0 {
		t.Fatal("legacy percentage entry incorrectly skipped as structural gap")
	}
}

func TestNestedLLMExpressionAndHistoricalOffsetResume(t *testing.T) {
	c := flatBars(8)
	s := plainStrategy()
	s.LLM = &LLMConfig{Provider: "ollama", Model: "test", Lookback: 2}
	s.Rules.EnterLong = cmp(expr("add", Field("llm.enter_long"), Constant(.1)), "gt", Constant(.5))
	s.Rules.ExitLong = cmp(&Operand{Field: "llm.enter_long", Offset: 2}, "gt", Constant(.5))
	enrich := func(context.Context, []Candle, map[string]float64, string, string) (*LLMAudit, error) {
		return &LLMAudit{Verdict: Verdict{EnterLong: .9}}, nil
	}
	r := requestFor(c)
	r.From = c[2].Time
	want, e := Backtest(context.Background(), s, r, "BTCUSDT", c, enrich)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	partial, _ := BacktestWithUpdates(ctx, s, r, "BTCUSDT", c, enrich, func(p PairResult) {
		if len(p.Candles) == 3 {
			cancel()
		}
	})
	got, e := ResumeBacktest(context.Background(), s, r, "BTCUSDT", c, partial, enrich, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("LLM history changed on resume")
	}
	if len(want.Trades) < 1 || want.Decisions[2].Action != "exit_long" {
		t.Fatal("historical LLM score not evaluated")
	}
}

func TestSessionLLMHistoryLeavesSkippedHoles(t *testing.T) {
	s := plainStrategy()
	s.Rules.EnterLong = cmp(&Operand{Field: "llm.enter_long", Offset: 3}, "gt", Constant(.5))
	a := map[string]float64{"llm.enter_long": .9}
	b := map[string]float64{}
	s.ApplyPreviousLLMHistory(b, a)
	c := map[string]float64{"llm.enter_long": .1}
	s.ApplyPreviousLLMHistory(c, b)
	d := map[string]float64{}
	s.ApplyPreviousLLMHistory(d, c)
	if v, ok := d["llm.enter_long[3]"]; !ok || v != .9 {
		t.Fatal("history lost")
	}
	if _, ok := d["llm.enter_long[2]"]; ok {
		t.Fatal("skipped score carried forward")
	}
	s.ApplyPreviousLLMHistory(d, nil)
	if len(d) != 0 {
		t.Fatal("stale history survived candle gap")
	}
}

func TestResumeRejectsCorruptPlanAndDailyCount(t *testing.T) {
	s := setupStrategy("long")
	c := flatBars(6)
	r := requestFor(c)
	for _, cut := range []int{2, 3} {
		ctx, cancel := context.WithCancel(context.Background())
		p, _ := BacktestWithUpdates(ctx, s, r, "BTCUSDT", c, nil, func(p PairResult) {
			if len(p.Candles) == cut {
				cancel()
			}
		})
		cancel()
		if cut == 2 {
			p.PendingPlan.Side = "short"
		} else {
			p.EntriesToday++
			p.Decisions[cut-1].EntriesToday++
		}
		if _, e := ResumeBacktest(context.Background(), s, r, "BTCUSDT", c, p, nil, nil); e == nil {
			t.Fatal("corrupt runtime state accepted")
		}
	}
}

func TestUndefinedLLMArithmeticDoesNotRequestInference(t *testing.T) {
	s := plainStrategy()
	s.LLM = &LLMConfig{Provider: "ollama", Model: "test", Lookback: 2}
	s.Rules.EnterLong = &Rule{Not: cmp(expr("div", Field("llm.enter_long"), Constant(0)), "gt", Constant(.5))}
	calls := 0
	d, e := ResolveDecision(context.Background(), s, flatBars(2), map[string]float64{}, nil, "BTCUSDT", "", func(context.Context, []Candle, map[string]float64, string, string) (*LLMAudit, error) {
		calls++
		return &LLMAudit{Verdict: Verdict{EnterLong: .9}}, nil
	})
	if e != nil || calls != 0 || d.Action != "hold" || d.RequiresLLM() {
		t.Fatal("undefined expression invoked LLM or passed NOT", e, d)
	}
}
