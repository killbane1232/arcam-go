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

func planEntry(s Strategy, d Decision, balance float64) (*EntryPlan, string) {
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

func executeEntry(s Strategy, r BacktestRequest, plan *EntryPlan, balance float64, bar Candle) (Position, *ExecutionAudit) {
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
	unitRisk := 0.0
	if stop > 0 {
		stopFill := Slipped(stop, r.SlippageBPS, plan.Side == "short")
		unitRisk = (price-stopFill)*direction + fee*(price+stopFill)
	}
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
