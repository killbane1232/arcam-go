package core

import (
	"math"
	"time"
)

type EntryPlan struct {
	Side           string    `json:"side"`
	SignalTime     time.Time `json:"signal_time"`
	ReferencePrice float64   `json:"reference_price"`
	StopPrice      float64   `json:"stop_price,omitempty"`
	TargetPrice    float64   `json:"target_price,omitempty"`
	RiskBudget     float64   `json:"risk_budget,omitempty"`
	EventID        string    `json:"event_id,omitempty"`
}
type ExecutionAudit struct {
	Action      string     `json:"action"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	Plan        *EntryPlan `json:"plan,omitempty"`
	EntryPrice  float64    `json:"entry_price,omitempty"`
	StopPrice   float64    `json:"stop_price,omitempty"`
	TargetPrice float64    `json:"target_price,omitempty"`
	Quantity    float64    `json:"quantity,omitempty"`
	PlannedRisk float64    `json:"planned_risk,omitempty"`
	Notional    float64    `json:"notional,omitempty"`
}

// PlanEntry freezes absolute protection and the monetary budget at the signal.
func PlanEntry(s Strategy, d Decision, balance float64) (*EntryPlan, string) {
	side := "long"
	spec := s.Risk.Long
	if d.Action == "enter_short" {
		side = "short"
		spec = s.Risk.Short
	}
	plan := &EntryPlan{Side: side, SignalTime: d.Time, ReferencePrice: d.Features["close"], RiskBudget: balance * s.Risk.RiskFraction}
	if spec != nil {
		stop, ok := operand(spec.StopPrice, d.Features)
		if !ok {
			return nil, "stop price unavailable"
		}
		plan.StopPrice = stop
		if spec.TargetPrice != nil {
			take, ok := operand(spec.TargetPrice, d.Features)
			if !ok {
				return nil, "target price unavailable"
			}
			plan.TargetPrice = take
		}
		if stop <= 0 || spec.TargetPrice != nil && plan.TargetPrice <= 0 {
			return nil, "protection price must be positive"
		}
		if side == "long" && (stop >= plan.ReferencePrice || plan.TargetPrice > 0 && plan.TargetPrice <= plan.ReferencePrice) || side == "short" && (stop <= plan.ReferencePrice || plan.TargetPrice > 0 && plan.TargetPrice >= plan.ReferencePrice) {
			return nil, "invalid protection at signal"
		}
	}
	return plan, ""
}

// SizeEntry sizes at a modeled next open (or an observed session quote).
func SizeEntry(s Strategy, r BacktestRequest, plan *EntryPlan, balance float64, bar Candle) (Position, *ExecutionAudit) {
	audit := &ExecutionAudit{Action: "enter_" + plan.Side, Status: "skipped", Plan: plan}
	price := Slipped(bar.Open, r.SlippageBPS, plan.Side == "long")
	audit.EntryPrice = price
	if balance <= 0 || !Finite(price) || price <= 0 {
		audit.Reason = "capital exhausted or invalid entry price"
		return Position{}, audit
	}
	stop, take := plan.StopPrice, plan.TargetPrice
	direction := 1.0
	if plan.Side == "short" {
		direction = -1
	}
	if stop == 0 && s.Risk.StopLoss > 0 {
		stop = price * (1 - direction*s.Risk.StopLoss)
	}
	if take == 0 && s.Risk.TakeProfit > 0 {
		take = price * (1 + direction*s.Risk.TakeProfit)
	}
	audit.StopPrice, audit.TargetPrice = stop, take
	distance := (price - stop) * direction
	if stop > 0 && ((plan.StopPrice > 0 && (bar.Open-stop)*direction <= 0) || distance <= 0) {
		audit.Reason = "gap beyond stop"
		return Position{}, audit
	}
	if take > 0 && ((plan.TargetPrice > 0 && (take-bar.Open)*direction <= 0) || (take-price)*direction <= 0) {
		audit.Reason = "gap beyond target"
		return Position{}, audit
	}
	if stop > 0 && s.Risk.MaxStopFraction > 0 && distance/price > s.Risk.MaxStopFraction {
		audit.Reason = "stop distance limit"
		return Position{}, audit
	}
	fee := r.FeeBPS / 10000
	capQty := balance * s.Risk.PositionFraction / (price * (1 + fee))
	if s.Risk.MaxNotional > 0 {
		capQty = math.Min(capQty, s.Risk.MaxNotional/price)
	}
	qty := capQty
	// R includes modeled entry/stop fees and adverse stop slippage. Gaps can
	// still exceed R; realized net PnL/R is kept in each completed trade.
	unitRisk := EntryUnitRisk(plan.Side, price, stop, r.FeeBPS, r.SlippageBPS)
	if s.Risk.RiskFraction > 0 {
		if unitRisk <= 0 || !Finite(unitRisk) {
			audit.Reason = "risk unavailable"
			return Position{}, audit
		}
		budget := math.Min(plan.RiskBudget, balance*s.Risk.RiskFraction)
		qty = budget / unitRisk
		if qty > capQty*(1+1e-12) {
			audit.Reason = "notional limit"
			return Position{}, audit
		}
	}
	if qty <= 0 || !Finite(qty) {
		audit.Reason = "invalid quantity"
		return Position{}, audit
	}
	p := Position{Side: plan.Side, Entry: price, Quantity: qty, EntryFee: price * qty * fee, Time: bar.Time, StopPrice: stop, TargetPrice: take, InitialRisk: qty * unitRisk, EventID: plan.EventID}
	audit.Status = "filled"
	audit.Quantity = qty
	audit.PlannedRisk = p.InitialRisk
	audit.Notional = price * qty
	return p, audit
}

// EntryUnitRisk includes the configured stop slippage and estimated fees;
// price is already the modeled or confirmed entry, never slipped twice.
func EntryUnitRisk(side string, price, stop, feeBPS, slippageBPS float64) float64 {
	if stop <= 0 {
		return 0
	}
	direction := 1.0
	if side == "short" {
		direction = -1
	}
	stopFill := Slipped(stop, slippageBPS, side == "short")
	return math.Max(0, (price-stopFill)*direction+feeBPS/10000*(price+stopFill))
}

// ConfirmEntry records an actual fill even if it breaches a constraint. The
// caller must retain that exposure and handle the returned violation; a fill
// must never be discarded or replaced by the planned price/quantity.
func ConfirmEntry(s Strategy, r BacktestRequest, plan *EntryPlan, balance, qty, price float64, at time.Time) (Position, *ExecutionAudit) {
	stop, take := plan.StopPrice, plan.TargetPrice
	direction := 1.0
	if plan.Side == "short" {
		direction = -1
	}
	if stop == 0 && s.Risk.StopLoss > 0 {
		stop = price * (1 - direction*s.Risk.StopLoss)
	}
	if take == 0 && s.Risk.TakeProfit > 0 {
		take = price * (1 + direction*s.Risk.TakeProfit)
	}
	p := Position{Side: plan.Side, Entry: price, Quantity: qty, EntryFee: qty * price * r.FeeBPS / 10000, Time: at, StopPrice: stop, TargetPrice: take, InitialRisk: qty * EntryUnitRisk(plan.Side, price, stop, r.FeeBPS, r.SlippageBPS), EventID: plan.EventID}
	audit := &ExecutionAudit{Action: "enter_" + plan.Side, Status: "filled", Plan: plan, EntryPrice: price, StopPrice: stop, TargetPrice: take, Quantity: qty, PlannedRisk: p.InitialRisk, Notional: qty * price}
	reason := ""
	switch {
	case !Finite(qty) || qty <= 0 || !Finite(price) || price <= 0:
		reason = "invalid fill"
	case stop > 0 && (price-stop)*direction <= 0:
		reason = "fill beyond stop"
	case take > 0 && (take-price)*direction <= 0:
		reason = "fill beyond target"
	case stop > 0 && s.Risk.MaxStopFraction > 0 && (price-stop)*direction/price > s.Risk.MaxStopFraction:
		reason = "stop distance limit"
	case qty*price*(1+r.FeeBPS/10000) > balance*s.Risk.PositionFraction*(1+1e-10):
		reason = "capital limit"
	case s.Risk.MaxNotional > 0 && qty*price > s.Risk.MaxNotional*(1+1e-10):
		reason = "notional limit"
	case s.Risk.RiskFraction > 0 && p.InitialRisk > math.Min(plan.RiskBudget, balance*s.Risk.RiskFraction)*(1+1e-10):
		reason = "risk budget exceeded"
	}
	if reason != "" {
		audit.Status = "risk_breached"
		audit.Reason = reason
	}
	return p, audit
}

// PrepareEntryDecision is shared by historical and streaming execution. An
// event gets one attempt once a valid plan is produced, including later skips.
func PrepareEntryDecision(s Strategy, d *Decision, balance float64, states map[string]SetupState, entryDay string, entries int) {
	if d.Action != "enter_long" && d.Action != "enter_short" {
		return
	}
	if balance <= 0 {
		d.Action = "hold"
		d.Reason = "capital exhausted"
		return
	}
	if s.Risk.MaxEntriesPerDay > 0 && d.Time.UTC().Format("2006-01-02") == entryDay && entries >= s.Risk.MaxEntriesPerDay {
		d.Action = "hold"
		d.Reason = "daily entry limit"
		return
	}
	plan, reason := PlanEntry(s, *d, balance)
	if reason != "" {
		d.Action = "hold"
		d.Reason = reason
		return
	}
	plan.EventID = ConsumeSetups(states, d.Rules[d.Action])
	d.Plan = plan
}
