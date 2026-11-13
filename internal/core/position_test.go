package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func positionRules() Strategy {
	s := plainStrategy()
	s.Rules.EnterLong = &Rule{All: []Rule{{Position: "none"}, *Compare("close", "gt", "open")}}
	s.Rules.EnterShort = &Rule{All: []Rule{{Position: "none"}, *Compare("close", "lt", "open")}}
	s.Rules.ExitLong = &Rule{All: []Rule{{Position: "long"}, {Signal: "enter_short"}}}
	s.Rules.ExitShort = &Rule{All: []Rule{{Position: "short"}, {Signal: "enter_long"}}}
	return s
}
func TestPositionGuardsAndOppositeSignal(t *testing.T) {
	s := positionRules()
	if e := ValidateStrategy(s); e != nil {
		t.Fatal(e)
	}
	for _, tt := range []struct {
		side   string
		close  float64
		action string
	}{{"", 110, "enter_long"}, {"none", 90, "enter_short"}, {"long", 110, "hold"}, {"short", 90, "hold"}, {"long", 90, "exit_long"}, {"short", 110, "exit_short"}} {
		d := Decide(s, map[string]float64{"open": 100, "close": tt.close}, nil, tt.side)
		if d.Action != tt.action {
			t.Fatalf("%+v: %+v", tt, d)
		}
		if strings.HasPrefix(tt.action, "exit_") {
			opposite := "enter_short"
			if tt.side == "short" {
				opposite = "enter_long"
			}
			if d.Rules[opposite].Result {
				t.Fatal("actual entry must remain false while in a position")
			}
			if !d.Rules[tt.action].Result {
				t.Fatal("opposite entry signal failed with flat position guard")
			}
		}
	}
	// Engine guard still prevents adding a position even without an explicit condition.
	s.Rules.EnterLong = &Rule{Left: Field("close"), Op: "gt", Right: Constant(0)}
	s.Rules.ExitLong = &Rule{Position: "none"}
	if d := Decide(s, map[string]float64{"close": 10}, nil, "long"); d.Action != "hold" {
		t.Fatal("pyramiding allowed", d)
	}
	if e := ValidateStrategy(PositionStrategy()); e != nil {
		t.Fatal(e)
	}
}
func TestPositionAndSignalValidation(t *testing.T) {
	for _, r := range []Rule{{Position: "flat"}, {Position: "none", Op: "eq"}, {Signal: "exit_long"}, {Signal: "enter_short", Position: "long"}} {
		s := positionRules()
		s.Rules.ExitLong = &r
		if ValidateStrategy(s) == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	s := positionRules()
	s.Rules.EnterLong = &Rule{Any: []Rule{{Signal: "enter_short"}, {Position: "none"}}}
	if ValidateStrategy(s) == nil {
		t.Fatal("cyclic entry reference accepted")
	}
	s = positionRules()
	s.Rules.EnterShort = nil
	if ValidateStrategy(s) == nil {
		t.Fatal("reference to disabled entry accepted")
	}
	s = positionRules()
	s.Rules.ExitLong = &Rule{Not: &Rule{Position: "none"}}
	if d := Decide(s, map[string]float64{"open": 100, "close": 110}, nil, "long"); d.Action != "exit_long" {
		t.Fatal("nested position condition failed")
	}
	s = positionRules()
	d := Decide(s, map[string]float64{"open": 100}, nil, "long")
	if d.Action != "hold" || d.Rules["exit_long"].Ready {
		t.Fatal("missing indicator incorrectly produced an exit")
	}
}
func TestOppositeSignalClosesBeforeReentry(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	closes := []float64{110, 90, 90, 90, 90}
	bars := []Candle{}
	for i, close := range closes {
		bars = append(bars, Candle{Time: start.Add(time.Duration(i) * time.Hour), Open: 100, High: 111, Low: 89, Close: close})
	}
	req := BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "1h", From: start, To: start.Add(5 * time.Hour), Capital: 1000}
	result, e := Backtest(context.Background(), positionRules(), req, "BTCUSDT", bars, nil)
	if e != nil {
		t.Fatal(e)
	}
	expected := []string{"enter_long", "exit_long", "enter_short"}
	if len(result.Fills) < 3 {
		t.Fatal(result.Fills)
	}
	for i, want := range expected {
		if result.Fills[i].Action != want || !result.Fills[i].Time.Equal(start.Add(time.Duration(i+1)*time.Hour)) {
			t.Fatal(result.Fills)
		}
	}
	if result.Decisions[1].Position != "long" || result.Decisions[2].Position != "none" {
		t.Fatal("wrong position at decision time")
	}
}
