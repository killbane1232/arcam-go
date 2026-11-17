package core

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
)

// ValidateResume rejects incomplete/corrupt checkpoints rather than restarting
// silently or fabricating an open position. Old completed pairs can be reused.
func ValidateResume(r BacktestRequest, symbol string, input []Candle, p PairResult) error {
	d, e := Interval(r.Interval)
	if e != nil {
		return e
	}
	n := len(p.Candles)
	if p.Symbol != symbol || n != len(p.Decisions) || n != len(p.Equity) || n > int(r.To.Sub(r.From)/d) {
		return fmt.Errorf("%s: inconsistent saved result", symbol)
	}
	index := 0
	for index < len(input) && input[index].Time.Before(r.From) {
		index++
	}
	for i, bar := range p.Candles {
		at := r.From.Add(time.Duration(i) * d)
		if index+i >= len(input) || !bar.Time.Equal(at) || bar != input[index+i] || !p.Decisions[i].Time.Equal(at.Add(d)) || !p.Equity[i].Time.Equal(at.Add(d)) || !Finite(p.Equity[i].Value) {
			return fmt.Errorf("%s: saved candles do not match frozen input at %s", symbol, at.Format(time.RFC3339))
		}
	}
	if n == 0 {
		if len(p.Fills)+len(p.Trades) != 0 || p.OpenPosition != nil {
			return fmt.Errorf("%s: trading state without saved candles", symbol)
		}
		return nil
	}
	if n == int(r.To.Sub(r.From)/d) {
		return nil
	}
	closeEnough := func(a, b float64) bool {
		return Finite(a) && Finite(b) && math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b))
	}
	if !closeEnough(p.Metrics.Initial, r.Capital/float64(len(r.Symbols))) || !closeEnough(p.Metrics.Final, p.Equity[n-1].Value) || p.Decisions[n-1].Features == nil {
		return fmt.Errorf("%s: missing saved metrics or previous indicator values", symbol)
	}
	if p.PendingAction != p.Decisions[n-1].Action {
		return fmt.Errorf("%s: missing saved pending action", symbol)
	}
	if !reflect.DeepEqual(p.Setups, p.Decisions[n-1].Setups) || !reflect.DeepEqual(p.PendingPlan, p.Decisions[n-1].Plan) || p.EntryDay != p.Decisions[n-1].EntryDay || p.EntriesToday != p.Decisions[n-1].EntriesToday {
		return fmt.Errorf("%s: inconsistent saved setup/plan/daily state", symbol)
	}
	if p.EntryDay != "" {
		day := p.Candles[n-1].Time.UTC().Format("2006-01-02")
		entries := 0
		for _, f := range p.Fills {
			if strings.HasPrefix(f.Action, "enter_") && f.Time.UTC().Format("2006-01-02") == day {
				entries++
			}
		}
		if p.EntryDay != day || p.EntriesToday != entries {
			return fmt.Errorf("%s: inconsistent saved daily entry count", symbol)
		}
	}
	if p.PendingPlan != nil {
		plan := p.PendingPlan
		if p.PendingAction != "enter_"+plan.Side || !plan.SignalTime.Equal(p.Decisions[n-1].Time) || !Finite(plan.ReferencePrice) || plan.ReferencePrice <= 0 || !Finite(plan.RiskBudget) || plan.RiskBudget < 0 {
			return fmt.Errorf("%s: invalid saved entry plan", symbol)
		}
	}
	var pos Position
	balance := p.Metrics.Initial
	fee := r.FeeBPS / 10000
	closes := 0
	for _, f := range p.Fills {
		switch f.Action {
		case "enter_long", "enter_short":
			if pos.Side != "" || f.Quantity <= 0 || f.Price <= 0 {
				return fmt.Errorf("%s: inconsistent saved entry", symbol)
			}
			side := "long"
			if f.Action == "enter_short" {
				side = "short"
			}
			entryFee := f.Price * f.Quantity * fee
			balance -= entryFee
			pos = Position{StopPrice: f.StopPrice, TargetPrice: f.TargetPrice, InitialRisk: f.InitialRisk, EventID: f.EventID, Side: side, Entry: f.Price, Quantity: f.Quantity, EntryFee: entryFee, Time: f.Time}
		case "exit_long", "exit_short":
			if pos.Side == "" || f.Action != "exit_"+pos.Side || !closeEnough(f.Quantity, pos.Quantity) {
				return fmt.Errorf("%s: inconsistent saved exit", symbol)
			}
			balance += pos.PnL(f.Price) - f.Price*f.Quantity*fee
			pos = Position{}
			closes++
		default:
			return fmt.Errorf("%s: invalid saved fill", symbol)
		}
	}
	if closes != len(p.Trades) || p.Metrics.Trades != closes || !closeEnough(balance, p.Balance) || !closeEnough(balance+pos.PnL(p.Candles[n-1].Close), p.Metrics.Final) {
		return fmt.Errorf("%s: inconsistent saved balance/trades", symbol)
	}
	if (p.OpenPosition == nil) != (pos.Side == "") || (p.OpenPosition != nil && *p.OpenPosition != pos) {
		return fmt.Errorf("%s: inconsistent saved open position", symbol)
	}
	return nil
}
