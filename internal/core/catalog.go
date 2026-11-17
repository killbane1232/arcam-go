package core

// DSLCatalog describes semantics that cannot be inferred from JSON types.
func DSLCatalog() map[string]any {
	return map[string]any{
		"indicator_timeframe":     "optional; >= execution interval and divisible by it; UTC complete buckets aggregated from frozen execution candles",
		"availability_fields":     []string{"<indicator>.bar_time", "<indicator>.available_at"},
		"daily_fields":            []string{"day.open", "day.high", "day.low", "day.range", "day.time", "day.available_at", "previous_day.open", "previous_day.high", "previous_day.low", "previous_day.close", "previous_day.range", "previous_day.time", "previous_day.available_at"},
		"arithmetic":              []string{"add", "sub", "mul", "div", "min", "max", "abs"},
		"expression_example":      Operand{Op: "div", Args: []Operand{{Field: "day.range"}, {Field: "adr5.value"}}},
		"offset_example":          Operand{Field: "high", Offset: 1},
		"offset_semantics":        "0..10000 execution bars in the past; missing data, division by zero and overflow are unavailable",
		"pivot":                   "left/right 1..1000; strict extrema; available only after right confirmation bars close; *_time identifies the reference bar, *_confirmation_bar identifies the confirming bar open",
		"setups":                  "id, when, capture, expires_after, optional cancel/key; rule {setup:id}; captured fields setup.<id>.<name>; age 0 is the trigger bar, expiry is inclusive; explicit keys must increase; each event permits one next-open attempt",
		"risk_fields":             []string{"long.stop_price", "long.target_price", "short.stop_price", "short.target_price", "risk_fraction", "max_notional", "max_stop_fraction", "max_entries_per_day"},
		"execution":               "next open; protection frozen at signal; risk sizing includes modeled fees/slippage; reject gaps beyond protection, excessive stop distance or risk-sized notional; stop wins simultaneous touches",
		"daily_limit":             "per pair, successful fills, UTC execution date",
		"planned_execution_modes": []string{"backtest"},
		"example":                 LevelsStrategy(),
	}
}

func LevelsStrategy() Strategy {
	return Strategy{
		Name: "Daily level breakout and retest", Description: "DSL example, not a validated trading hypothesis. Closed daily context; one retest attempt per previous-day level.",
		Indicators: []Indicator{{ID: "adr5", Type: "adr", Period: 5, Timeframe: "1d"}, {ID: "atr14", Type: "atr", Period: 14, Timeframe: "1d"}},
		Setups:     []Setup{{ID: "breakout", When: Compare("close", "crosses_above", "previous_day.high"), Key: Field("previous_day.time"), Capture: map[string]Operand{"level": *Field("previous_day.high"), "stop": {Op: "sub", Args: []Operand{{Field: "previous_day.high"}, {Op: "mul", Args: []Operand{{Field: "atr14.value"}, *Constant(.2)}}}}}, ExpiresAfter: 4, Cancel: Compare("close", "lt", "setup.breakout.stop")}},
		Rules:      Rules{EnterLong: &Rule{All: []Rule{{Setup: "breakout"}, {Left: Field("setup.breakout.age"), Op: "gte", Right: Constant(1)}, *Compare("low", "lte", "setup.breakout.level"), *Compare("close", "gt", "setup.breakout.level"), {Left: &Operand{Op: "div", Args: []Operand{{Field: "day.range"}, {Field: "adr5.value"}}}, Op: "lt", Right: Constant(1.5)}}}, ExitLong: Compare("close", "lt", "setup.breakout.level")},
		Risk:       Risk{PositionFraction: 1, RiskFraction: .005, MaxStopFraction: .1, MaxEntriesPerDay: 2, Long: &Protection{StopPrice: Field("setup.breakout.stop"), TargetPrice: &Operand{Op: "add", Args: []Operand{{Field: "setup.breakout.level"}, {Field: "atr14.value"}}}}},
	}
}
