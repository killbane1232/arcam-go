package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func score(field string, threshold float64) Rule {
	return Rule{Left: Field("llm." + field), Op: "gte", Right: Constant(threshold)}
}
func lazyStrategy(r Rule) Strategy {
	s := plainStrategy()
	s.LLM = &LLMConfig{Provider: "ollama", Model: "test", Lookback: 2}
	s.Rules.EnterLong = &r
	return s
}
func TestLazyLLMBooleanBranches(t *testing.T) {
	yes := Rule{Left: Field("close"), Op: "gt", Right: Constant(0)}
	no := Rule{Left: Field("close"), Op: "lt", Right: Constant(0)}
	model := score("enter_long", .7)
	for _, tt := range []struct {
		name   string
		rule   Rule
		calls  int
		action string
	}{
		{"model first but indicator false", Rule{All: []Rule{model, no}}, 0, "hold"},
		{"all passes", Rule{All: []Rule{model, yes}}, 1, "enter_long"},
		{"nested rejected all returns to any", Rule{Any: []Rule{{All: []Rule{model, no}}, yes}}, 0, "enter_long"},
		{"all rejects nested pending branch", Rule{All: []Rule{{Any: []Rule{model, no}}, no}}, 0, "hold"},
		{"pending branch followed by rejected model branch", Rule{All: []Rule{model, {All: []Rule{model, no}}}}, 0, "hold"},
		{"any succeeds without model", Rule{Any: []Rule{model, yes}}, 0, "enter_long"},
		{"any needs model", Rule{Any: []Rule{model, no}}, 1, "enter_long"},
		{"not of rejected all", Rule{Not: &Rule{All: []Rule{model, no}}}, 0, "enter_long"},
		{"not pending", Rule{Not: &model}, 1, "hold"},
		{"not negative model", Rule{Not: &Rule{Left: Field("llm.enter_long"), Op: "gte", Right: Constant(.99)}}, 1, "enter_long"},
		{"missing indicator does not invoke model", Rule{All: []Rule{model, *Compare("missing", "gt", "close")}}, 0, "hold"},
		{"not does not invert unavailable indicator", Rule{Not: Compare("missing", "gt", "llm.enter_long")}, 0, "hold"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := lazyStrategy(tt.rule)
			before, _ := json.Marshal(s)
			calls := 0
			fn := func(_ context.Context, _ []Candle, f map[string]float64, _, _ string) (*LLMAudit, error) {
				calls++
				if _, ok := f["llm.enter_long"]; ok {
					t.Fatal("stale LLM feature sent to provider")
				}
				return &LLMAudit{Verdict: Verdict{EnterLong: .8}}, nil
			}
			now := map[string]float64{"close": 100}
			d, err := ResolveDecision(context.Background(), s, make([]Candle, 2), now, nil, "BTCUSDT", "", fn)
			if err != nil || calls != tt.calls || d.Action != tt.action || d.RequiresLLM() {
				t.Fatalf("calls=%d decision=%+v err=%v", calls, d, err)
			}
			if (d.LLMStatus == "skipped") != (tt.calls == 0) {
				t.Fatal("wrong LLM status", d.LLMStatus)
			}
			after, _ := json.Marshal(s)
			if string(before) != string(after) {
				t.Fatal("immutable strategy reordered")
			}
			// Compare against a full evaluation with a known model answer.
			full := map[string]float64{"close": 100}
			ApplyVerdict(full, Verdict{EnterLong: .8})
			if expected := Decide(s, full, nil, ""); expected.Action != d.Action {
				t.Fatal("lazy evaluation changed action", expected.Action, d.Action)
			}
		})
	}
}
func TestLazyTraceAndPositionSignals(t *testing.T) {
	s := lazyStrategy(Rule{All: []Rule{score("enter_long", .7), {Position: "long"}}})
	d, err := ResolveDecision(context.Background(), s, make([]Candle, 2), map[string]float64{}, nil, "BTCUSDT", "", nil)
	if err != nil || !d.Rules["enter_long"].Children[0].Skipped || d.Rules["enter_long"].Children[1].Result {
		t.Fatal("model branch evaluated before position guard", d, err)
	}
	s = positionRules()
	s.LLM = &LLMConfig{Provider: "ollama", Model: "test", Lookback: 2}
	s.Rules.EnterShort.All = append([]Rule{score("enter_short", .7)}, s.Rules.EnterShort.All...)
	for _, tt := range []struct {
		side   string
		close  float64
		calls  int
		action string
	}{
		{"long", 110, 0, "hold"}, {"long", 90, 1, "exit_long"}, {"", 110, 0, "enter_long"}, {"short", 110, 0, "exit_short"},
	} {
		calls := 0
		fn := func(_ context.Context, _ []Candle, _ map[string]float64, _, side string) (*LLMAudit, error) {
			calls++
			if side != tt.side {
				t.Fatal("model received virtual instead of actual position")
			}
			return &LLMAudit{Verdict: Verdict{EnterShort: .9}}, nil
		}
		d, e := ResolveDecision(context.Background(), s, make([]Candle, s.Warmup()), map[string]float64{"open": 100, "close": tt.close}, nil, "BTCUSDT", tt.side, fn)
		if e != nil || calls != tt.calls || d.Action != tt.action {
			t.Fatalf("%+v: calls=%d decision=%+v err=%v", tt, calls, d, e)
		}
	}
}
func TestLazyConflictingSignalsUseOneVerdict(t *testing.T) {
	s := lazyStrategy(Rule{All: []Rule{score("enter_long", .7), score("exit_short", .7)}})
	short := score("enter_short", .7)
	s.Rules.EnterShort = &short
	s.Rules.ExitShort = &Rule{Position: "short"}
	calls := 0
	d, e := ResolveDecision(context.Background(), s, make([]Candle, 2), map[string]float64{}, nil, "BTCUSDT", "", func(context.Context, []Candle, map[string]float64, string, string) (*LLMAudit, error) {
		calls++
		return &LLMAudit{Verdict: Verdict{EnterLong: .9, EnterShort: .9, ExitShort: .9}}, nil
	})
	if e != nil || calls != 1 || d.Action != "hold" || d.Reason != "conflicting long and short signals" {
		t.Fatal(calls, d, e)
	}
	// A resolved long signal still needs the pending short signal to rule out conflict.
	s.Rules.EnterLong = &Rule{Position: "none"}
	d = Decide(s, map[string]float64{}, nil, "")
	if !d.RequiresLLM() || d.Action != "hold" {
		t.Fatal("entry allowed before opposing signal resolved", d)
	}
}
func TestLazyErrorsOnlyWhenReached(t *testing.T) {
	s := lazyStrategy(score("enter_long", .7))
	fn := func(context.Context, []Candle, map[string]float64, string, string) (*LLMAudit, error) {
		return nil, fmt.Errorf("provider unavailable")
	}
	if _, e := ResolveDecision(context.Background(), s, make([]Candle, 2), map[string]float64{}, nil, "BTCUSDT", "", fn); e == nil {
		t.Fatal("model failure hidden")
	}
	s.Rules.EnterLong = &Rule{All: []Rule{score("enter_long", .7), {Position: "short"}}}
	if d, e := ResolveDecision(context.Background(), s, make([]Candle, 2), map[string]float64{}, nil, "BTCUSDT", "", fn); e != nil || d.LLMStatus != "skipped" {
		t.Fatal("unused model contacted", d, e)
	}
}
func TestLazyLLMCrossRequiresAdjacentValues(t *testing.T) {
	s := lazyStrategy(Rule{All: []Rule{{Left: Field("llm.enter_long"), Op: "crosses_above", Right: Constant(.5)}, {Left: Field("close"), Op: "gt", Right: Constant(0)}}})
	calls := 0
	fn := func(context.Context, []Candle, map[string]float64, string, string) (*LLMAudit, error) {
		calls++
		return &LLMAudit{Verdict: Verdict{EnterLong: .9}}, nil
	}
	skipped := map[string]float64{"close": -1}
	d, _ := ResolveDecision(context.Background(), s, make([]Candle, 2), skipped, map[string]float64{"llm.enter_long": .1}, "BTCUSDT", "", fn)
	if calls != 0 || d.LLMStatus != "skipped" {
		t.Fatal(d)
	}
	d, _ = ResolveDecision(context.Background(), s, make([]Candle, 2), map[string]float64{"close": 1}, skipped, "BTCUSDT", "", fn)
	if calls != 1 || d.Action != "hold" || d.Rules["enter_long"].Ready {
		t.Fatal("skipped predecessor fabricated", d)
	}
	d, _ = ResolveDecision(context.Background(), s, make([]Candle, 2), map[string]float64{"close": 1}, map[string]float64{"llm.enter_long": .1}, "BTCUSDT", "", fn)
	if calls != 2 || d.Action != "enter_long" {
		t.Fatal("valid adjacent cross missing", d)
	}
}
func TestBacktestSkipsLLMUntilEligibleEntry(t *testing.T) {
	s := lazyStrategy(Rule{All: []Rule{score("enter_long", .7), {Left: Field("close"), Op: "gt", Right: Constant(150)}}})
	s.Rules.ExitLong = &Rule{Left: Field("close"), Op: "lt", Right: Constant(150)}
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []Candle{}
	for i, v := range []float64{100, 100, 100, 200, 200, 100, 100, 100} {
		bars = append(bars, Candle{Time: start.Add(time.Duration(i) * time.Hour), Open: 100, High: 200, Low: 100, Close: v})
	}
	req := BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "1h", From: bars[2].Time, To: start.Add(8 * time.Hour), Capital: 1000}
	calls := 0
	out, e := Backtest(context.Background(), s, req, "BTCUSDT", bars, func(_ context.Context, c []Candle, _ map[string]float64, _, _ string) (*LLMAudit, error) {
		calls++
		if len(c) != 4 {
			t.Fatal("wrong candle sent to model", len(c))
		}
		return &LLMAudit{Verdict: Verdict{EnterLong: .9}}, nil
	})
	if e != nil || calls != 1 || len(out.Decisions) != 6 || len(out.Trades) != 1 {
		t.Fatal(calls, out, e)
	}
	for i, want := range []string{"hold", "enter_long", "hold", "exit_long", "hold", "hold"} {
		d := out.Decisions[i]
		if d.Action != want {
			t.Fatal(i, d)
		}
		if i != 1 && (d.LLM != nil || d.LLMStatus != "skipped") {
			t.Fatal("unnecessary LLM call", i, d)
		}
	}
}
