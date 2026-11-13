package core

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestResumeMatchesUninterruptedBacktest(t *testing.T) {
	bars := DemoCandles(100)[:18]
	for _, side := range []string{"long", "short"} {
		s := plainStrategy()
		s.Indicators = []Indicator{{ID: "ema", Type: "ema", Period: 3}}
		s.LLM = &LLMConfig{Provider: "ollama", Model: "resume", Lookback: 2}
		entry := &Rule{All: []Rule{score("enter_"+side, .5), {Left: Field("ema.value"), Op: "gt", Right: Constant(0)}}}
		exit := &Rule{Left: Field("llm.exit_" + side), Op: "crosses_above", Right: Constant(.5)}
		s.Rules = Rules{}
		if side == "long" {
			s.Rules.EnterLong = entry
			s.Rules.ExitLong = exit
		} else {
			s.Rules.EnterShort = entry
			s.Rules.ExitShort = exit
		}
		s.Risk = Risk{PositionFraction: .8, StopLoss: .03, TakeProfit: .04}
		r := BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "1h", From: bars[4].Time, To: bars[12].Time, Capital: 1000, FeeBPS: 4, SlippageBPS: 2}
		verdict := func(_ context.Context, c []Candle, _ map[string]float64, _, _ string) (*LLMAudit, error) {
			exit := .1
			if len(c)%3 == 0 {
				exit = .9
			}
			return &LLMAudit{Verdict: Verdict{EnterLong: .9, EnterShort: .9, ExitLong: exit, ExitShort: exit}}, nil
		}
		want, e := Backtest(context.Background(), s, r, "BTCUSDT", bars, verdict)
		if e != nil {
			t.Fatal(e)
		}
		for cut := 0; cut < 8; cut++ {
			t.Run(fmt.Sprintf("%s/after_%d", side, cut), func(t *testing.T) {
				failedAt := r.From.Add(time.Duration(cut) * time.Hour)
				partial, e := Backtest(context.Background(), s, r, "BTCUSDT", bars, func(ctx context.Context, c []Candle, f map[string]float64, symbol, position string) (*LLMAudit, error) {
					if c[len(c)-1].Time.Equal(failedAt) {
						return nil, fmt.Errorf("model down")
					}
					return verdict(ctx, c, f, symbol, position)
				})
				if e == nil || len(partial.Decisions) != cut {
					t.Fatal("wrong checkpoint", e, len(partial.Decisions))
				}
				before, _ := json.Marshal(partial)
				got, e := ResumeBacktest(context.Background(), s, r, "BTCUSDT", bars, partial, func(ctx context.Context, c []Candle, f map[string]float64, symbol, position string) (*LLMAudit, error) {
					if c[len(c)-1].Time.Before(failedAt) {
						t.Fatal("re-evaluated a completed candle")
					}
					return verdict(ctx, c, f, symbol, position)
				}, nil)
				if e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("resumed result differs from baseline: got metrics=%+v want=%+v", got.Metrics, want.Metrics)
				}
				after, _ := json.Marshal(partial)
				if string(before) != string(after) {
					t.Fatal("saved prefix mutated")
				}
			})
		}
	}
}
