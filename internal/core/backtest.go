package core

import (
	"context"
	"fmt"
	"math"
	"time"
)

type Enricher func(context.Context, []Candle, map[string]float64, string, string) (*LLMAudit, error)
type Position struct {
	StopPrice   float64   `json:"stop_price,omitempty"`
	TargetPrice float64   `json:"target_price,omitempty"`
	InitialRisk float64   `json:"initial_risk,omitempty"`
	EventID     string    `json:"event_id,omitempty"`
	Side        string    `json:"side"`
	Entry       float64   `json:"entry"`
	Quantity    float64   `json:"quantity"`
	EntryFee    float64   `json:"entry_fee"`
	Time        time.Time `json:"time"`
}

func (p Position) PnL(price float64) float64 {
	v := (price - p.Entry) * p.Quantity
	if p.Side == "short" {
		v = -v
	}
	return v
}
func Slipped(price, bps float64, buy bool) float64 {
	if buy {
		return price * (1 + bps/10000)
	}
	return price * (1 - bps/10000)
}
func RiskExit(p Position, c Candle, r Risk) (float64, string) {
	if p.Side == "" {
		return 0, ""
	}
	stop, take := 0.0, 0.0
	if p.Side == "long" {
		stop = p.Entry * (1 - r.StopLoss)
		take = p.Entry * (1 + r.TakeProfit)
		if p.StopPrice > 0 {
			stop = p.StopPrice
		}
		if p.TargetPrice > 0 {
			take = p.TargetPrice
		}
		if (r.StopLoss > 0 || p.StopPrice > 0) && c.Low <= stop {
			return math.Min(c.Open, stop), "stop_loss"
		}
		if (r.TakeProfit > 0 || p.TargetPrice > 0) && c.High >= take {
			return math.Max(c.Open, take), "take_profit"
		}
	} else {
		stop = p.Entry * (1 + r.StopLoss)
		take = p.Entry * (1 - r.TakeProfit)
		if p.StopPrice > 0 {
			stop = p.StopPrice
		}
		if p.TargetPrice > 0 {
			take = p.TargetPrice
		}
		if (r.StopLoss > 0 || p.StopPrice > 0) && c.High >= stop {
			return math.Max(c.Open, stop), "stop_loss"
		}
		if (r.TakeProfit > 0 || p.TargetPrice > 0) && c.Low <= take {
			return math.Min(c.Open, take), "take_profit"
		}
	}
	return 0, ""
}
func Backtest(ctx context.Context, s Strategy, r BacktestRequest, symbol string, c []Candle, enrich Enricher) (PairResult, error) {
	return BacktestWithProgress(ctx, s, r, symbol, c, enrich, nil)
}

// report runs synchronously after each fully evaluated test candle.
func BacktestWithProgress(ctx context.Context, s Strategy, r BacktestRequest, symbol string, c []Candle, enrich Enricher, report func(Decision)) (PairResult, error) {
	return BacktestWithUpdates(ctx, s, r, symbol, c, enrich, func(p PairResult) {
		if report != nil {
			report(p.Decisions[len(p.Decisions)-1])
		}
	})
}

// report receives an immutable prefix after each complete candle. Consumers
// may retain the slice views but must not modify them or their nested values.
// On error, only the last complete prefix is returned (no half-evaluated bar).
func BacktestWithUpdates(ctx context.Context, s Strategy, r BacktestRequest, symbol string, c []Candle, enrich Enricher, report func(PairResult)) (PairResult, error) {
	return ResumeBacktest(ctx, s, r, symbol, c, PairResult{}, enrich, report)
}

// ResumeBacktest appends only new decisions to a previously committed prefix.
// Indicator series are rebuilt from the frozen input; previous LLM values and
// trading state come from the saved prefix, never from a new model evaluation.
func ResumeBacktest(ctx context.Context, s Strategy, r BacktestRequest, symbol string, c []Candle, saved PairResult, enrich Enricher, report func(PairResult)) (PairResult, error) {
	result := saved
	if len(saved.Candles) == 0 {
		result = PairResult{Symbol: symbol, Candles: []Candle{}, Decisions: []Decision{}, Fills: []Fill{}, Trades: []Trade{}, Equity: []EquityPoint{}}
	}
	d, _ := Interval(r.Interval)
	expected := r.From
	for _, bar := range c {
		if bar.Time.Before(r.From) || !bar.Time.Before(r.To) {
			continue
		}
		if !bar.Time.Equal(expected) {
			return result, fmt.Errorf("%s: data gap at %s", symbol, expected.Format(time.RFC3339))
		}
		expected = expected.Add(d)
	}
	if !expected.Equal(r.To) {
		return result, fmt.Errorf("%s: incomplete data, expected candles through %s", symbol, r.To)
	}
	if len(c) < 2 {
		return result, fmt.Errorf("not enough candles")
	}
	f, err := BuildFeatures(c, s, r.Interval)
	if err != nil {
		return result, err
	}
	history := s.historyOperands()
	balance := r.Capital / float64(len(r.Symbols))
	initial := balance
	peak, wins := initial, 0
	fee := r.FeeBPS / 10000
	var pos Position
	pending := ""
	var pendingPlan *EntryPlan
	states := saved.Setups
	entryDay, entriesToday := saved.EntryDay, saved.EntriesToday
	var prev map[string]float64
	resumeAt := r.From
	if len(saved.Candles) > 0 {
		if e := ValidateResume(r, symbol, c, saved); e != nil {
			return saved, e
		}
		balance = saved.Balance
		if saved.OpenPosition != nil {
			pos = *saved.OpenPosition
		}
		pending = saved.PendingAction
		pendingPlan = saved.PendingPlan
		if len(s.Setups) > 0 && len(states) != len(s.Setups) {
			return saved, fmt.Errorf("missing saved setup state")
		}
		if s.RequiresPlannedExecution() && (pending == "enter_long" || pending == "enter_short") && pendingPlan == nil {
			return saved, fmt.Errorf("missing saved entry plan")
		}
		j := 0
		for i, bar := range c {
			if !bar.Time.Before(r.From) && j < len(saved.Decisions) {
				f[i] = saved.Decisions[j].Features
				j++
			}
		}
		prev = saved.Decisions[len(saved.Decisions)-1].Features
		resumeAt = r.From.Add(time.Duration(len(saved.Candles)) * d)
		for _, e := range saved.Equity {
			peak = math.Max(peak, e.Value)
		}
		for _, trade := range saved.Trades {
			if trade.PnL > 0 {
				wins++
			}
		}
		if resumeAt.Equal(r.To) {
			result.Complete = true
			return result, nil
		}
	} else {
		result.Balance = balance
		result.Metrics = Metrics{Initial: initial, Final: initial}
	}
	committed := result
	closePosition := func(price float64, at time.Time, reason string) {
		price = Slipped(price, r.SlippageBPS, pos.Side == "short")
		exitFee := price * pos.Quantity * fee
		pnl := pos.PnL(price) - pos.EntryFee - exitFee
		balance += pos.PnL(price) - exitFee
		result.Metrics.Fees += exitFee
		result.Metrics.Trades++
		if pnl > 0 {
			wins++
		}
		pnlR := 0.0
		if pos.InitialRisk > 0 {
			pnlR = pnl / pos.InitialRisk
		}
		result.Trades = append(result.Trades, Trade{StopPrice: pos.StopPrice, TargetPrice: pos.TargetPrice, InitialRisk: pos.InitialRisk, PnLR: pnlR, EventID: pos.EventID, Side: pos.Side, EntryTime: pos.Time, ExitTime: at, Entry: pos.Entry, Exit: price, Quantity: pos.Quantity, Fees: pos.EntryFee + exitFee, PnL: pnl, Reason: reason})
		result.Fills = append(result.Fills, Fill{Time: at, Action: "exit_" + pos.Side, Price: price, Quantity: pos.Quantity, Reason: reason})
		pos = Position{}
	}
	for i, bar := range c {
		if e := ctx.Err(); e != nil {
			return committed, e
		}
		if bar.Time.Before(r.From) {
			if len(saved.Candles) == 0 {
				prev = f[i]
			}
			continue
		}
		if bar.Time.Before(resumeAt) {
			continue
		}
		if !bar.Time.Before(r.To) {
			break
		}
		if pending == "exit_long" || pending == "exit_short" {
			if pos.Side != "" {
				closePosition(bar.Open, bar.Time, "rule")
			}
		}
		day := bar.Time.UTC().Format("2006-01-02")
		if day != entryDay {
			entryDay = day
			entriesToday = 0
		}
		var execution *ExecutionAudit
		if (pending == "enter_long" || pending == "enter_short") && pos.Side == "" {
			if pendingPlan == nil {
				// Legacy checkpoints did not store plans.
				side := "long"
				if pending == "enter_short" {
					side = "short"
				}
				pendingPlan = &EntryPlan{Side: side}
			}
			if s.Risk.MaxEntriesPerDay > 0 && entriesToday >= s.Risk.MaxEntriesPerDay {
				execution = &ExecutionAudit{Action: pending, Status: "skipped", Reason: "daily entry limit", Plan: pendingPlan}
			} else {
				pos, execution = SizeEntry(s, r, pendingPlan, balance, bar)
				if pos.Side != "" {
					balance -= pos.EntryFee
					result.Metrics.Fees += pos.EntryFee
					entriesToday++
					result.Fills = append(result.Fills, Fill{Time: bar.Time, Action: pending, Price: pos.Entry, Quantity: pos.Quantity, Reason: "previous closed candle signal", StopPrice: pos.StopPrice, TargetPrice: pos.TargetPrice, InitialRisk: pos.InitialRisk, EventID: pos.EventID})
				}
			}
		}
		pending = ""
		pendingPlan = nil
		if price, reason := RiskExit(pos, bar, s.Risk); reason != "" {
			closePosition(price, bar.Time.Add(d), reason)
		}
		addHistory(history, f, i)
		states = AdvanceSetups(s, states, f[i], prev, bar.Time.Add(d), d, pos.Side, true)
		decision, e := ResolveDecision(ctx, s, c[:i+1], f[i], prev, symbol, pos.Side, enrich)
		if e != nil {
			return committed, e
		}
		decision.Time = bar.Time.Add(d)
		decision.Execution = execution
		decision.EntryDay, decision.EntriesToday = entryDay, entriesToday
		if balance <= 0 && pos.Side == "" {
			decision.Action = "hold"
			decision.Reason = "capital exhausted"
		}
		last := bar.Time.Add(d).Equal(r.To)
		if !last {
			PrepareEntryDecision(s, &decision, balance, states, entryDay, entriesToday)
			pendingPlan = decision.Plan
			pending = decision.Action
		} else {
			if decision.Action != "hold" {
				decision.Reason += "; no next candle, signal not executed"
			}
			if pos.Side != "" {
				closePosition(bar.Close, bar.Time.Add(d), "end_of_range")
			}
		}
		decision.Setups = states
		result.Setups = states
		result.PendingPlan = pendingPlan
		result.EntryDay = entryDay
		result.EntriesToday = entriesToday
		result.Candles = append(result.Candles, bar)
		result.Decisions = append(result.Decisions, decision)
		equity := balance + pos.PnL(bar.Close)
		result.Equity = append(result.Equity, EquityPoint{Time: bar.Time.Add(d), Value: equity})
		result.Balance, result.UnrealizedPnL = balance, pos.PnL(bar.Close)
		result.PendingAction, result.Complete = pending, last
		result.OpenPosition = nil
		if pos.Side != "" {
			open := pos
			result.OpenPosition = &open
		}
		// Incremental metrics keep publishing O(1) per candle, even on long runs.
		result.Metrics.Final = equity
		result.Metrics.ReturnPct = (equity/initial - 1) * 100
		peak = math.Max(peak, equity)
		if peak > 0 {
			result.Metrics.MaxDrawdownPct = math.Max(result.Metrics.MaxDrawdownPct, (peak-equity)/peak*100)
		}
		if result.Metrics.Trades > 0 {
			result.Metrics.WinRatePct = float64(wins) / float64(result.Metrics.Trades) * 100
		}
		committed = result
		prev = f[i]
		if report != nil {
			report(committed)
		}
	}
	return committed, nil
}
func ApplyVerdict(f map[string]float64, v Verdict) {
	f["llm.enter_long"] = v.EnterLong
	f["llm.exit_long"] = v.ExitLong
	f["llm.enter_short"] = v.EnterShort
	f["llm.exit_short"] = v.ExitShort
}
func CalculateMetrics(initial float64, trades []Trade, equity []EquityPoint) Metrics {
	m := Metrics{Initial: initial, Final: initial, Trades: len(trades)}
	peak := initial
	wins := 0
	for _, p := range equity {
		m.Final = p.Value
		peak = math.Max(peak, p.Value)
		if peak > 0 {
			m.MaxDrawdownPct = math.Max(m.MaxDrawdownPct, (peak-p.Value)/peak*100)
		}
	}
	for _, t := range trades {
		if t.PnL > 0 {
			wins++
		}
		m.Fees += t.Fees
	}
	if len(trades) > 0 {
		m.WinRatePct = 100 * float64(wins) / float64(len(trades))
	}
	m.ReturnPct = (m.Final/initial - 1) * 100
	return m
}
func Aggregate(pairs []PairResult, capital float64) Metrics {
	if len(pairs) == 0 {
		return Metrics{Initial: capital, Final: capital}
	}
	all := []Trade{}
	curve := make([]EquityPoint, len(pairs[0].Equity))
	for i, p := range pairs[0].Equity {
		curve[i].Time = p.Time
	}
	for _, p := range pairs {
		all = append(all, p.Trades...)
		for i, e := range p.Equity {
			if i < len(curve) {
				curve[i].Value += e.Value
			}
		}
	}
	return CalculateMetrics(capital, all, curve)
}
