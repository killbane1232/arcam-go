package core

import (
	"math"
	"time"
)

func Constant(v float64) *Operand   { return &Operand{Value: &v} }
func Field(s string) *Operand       { return &Operand{Field: s} }
func Compare(a, op, b string) *Rule { return &Rule{Left: Field(a), Op: op, Right: Field(b)} }
func DemoStrategy() Strategy {
	return Strategy{Name: "Trend confirmation", Description: "EMA 12/26 + Aroon 25. Independent long/short rules.", Indicators: []Indicator{{ID: "fast", Type: "ema", Period: 12}, {ID: "slow", Type: "ema", Period: 26}, {ID: "trend", Type: "aroon", Period: 25}}, Rules: Rules{
		EnterLong:  &Rule{All: []Rule{*Compare("fast.value", "gt", "slow.value"), {Left: Field("trend.up"), Op: "gt", Right: Constant(70)}}},
		ExitLong:   Compare("fast.value", "lt", "slow.value"),
		EnterShort: &Rule{All: []Rule{*Compare("fast.value", "lt", "slow.value"), {Left: Field("trend.down"), Op: "gt", Right: Constant(70)}}},
		ExitShort:  Compare("fast.value", "gt", "slow.value")}, Risk: Risk{PositionFraction: 0.5, StopLoss: 0.03, TakeProfit: 0.06}}
}

// D1Strategy is an opt-in research preset; existing versions remain unchanged.
func D1Strategy() Strategy {
	s := DemoStrategy()
	s.Name = "EMA + LiquidAI/d1-2B"
	s.Description = "Temporary d1-2B decision preset. EMA/Aroon entries require model support >= 0.75; exits use EMA or d1 support >= 0.75."
	s.LLM = &LLMConfig{Provider: "ollama", Protocol: "systemone", Model: "d1:2b", Lookback: 30, Instructions: "Evaluate trend continuation and reversal risk from the supplied closed candles, EMA and Aroon indicators. Require consistent trend evidence for entries; favor exiting when the trend weakens or reverses."}
	threshold := func(field string) Rule { return Rule{Left: Field("llm." + field), Op: "gte", Right: Constant(0.75)} }
	s.Rules.EnterLong.All = append(s.Rules.EnterLong.All, threshold("enter_long"))
	s.Rules.EnterShort.All = append(s.Rules.EnterShort.All, threshold("enter_short"))
	s.Rules.ExitLong = &Rule{Any: []Rule{*s.Rules.ExitLong, threshold("exit_long")}}
	s.Rules.ExitShort = &Rule{Any: []Rule{*s.Rules.ExitShort, threshold("exit_short")}}
	return s
}

func PositionStrategy() Strategy {
	s := DemoStrategy()
	s.Name = "EMA + Aroon / position and opposite signal"
	s.Description = "Enter only when flat. Close an existing position on its exit rule or on the opposite entry signal. Re-entry requires a later decision."
	s.Rules.EnterLong = &Rule{All: []Rule{{Position: "none"}, *s.Rules.EnterLong}}
	s.Rules.EnterShort = &Rule{All: []Rule{{Position: "none"}, *s.Rules.EnterShort}}
	s.Rules.ExitLong = &Rule{All: []Rule{{Position: "long"}, {Any: []Rule{*s.Rules.ExitLong, {Signal: "enter_short"}}}}}
	s.Rules.ExitShort = &Rule{All: []Rule{{Position: "short"}, {Any: []Rule{*s.Rules.ExitShort, {Signal: "enter_long"}}}}}
	return s
}

// Explicitly synthetic, never labeled as exchange history.
func DemoCandles(base float64) []Candle {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]Candle, 1000)
	prev := base
	for i := range out {
		price := base * (1 + 0.07*math.Sin(float64(i)/32) + 0.03*math.Sin(float64(i)/7) + float64(i)*0.00005)
		out[i] = Candle{Time: start.Add(time.Duration(i) * time.Hour), Open: prev, High: math.Max(prev, price) * 1.003, Low: math.Min(prev, price) * 0.997, Close: price, Volume: 100 + 30*math.Sin(float64(i)/5)}
		prev = price
	}
	return out
}
