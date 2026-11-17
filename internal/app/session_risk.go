package app

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"arcam/internal/core"
	"arcam/internal/execution"
	"arcam/internal/store"
)

type DailyEntries struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

func sessionDay(s *Session, symbol string, at time.Time) DailyEntries {
	if s.Daily == nil {
		s.Daily = map[string]DailyEntries{}
	}
	day := at.UTC().Format("2006-01-02")
	v := s.Daily[symbol]
	if v.Day != day {
		v = DailyEntries{Day: day}
		s.Daily[symbol] = v
	}
	return v
}

func sessionSetupFeatures(s *Session, symbol string, rows []map[string]float64, bars []core.Candle, step time.Duration, side string) (map[string]float64, map[string]float64, map[string]core.SetupState) {
	strategy := s.Strategy.Strategy
	last := len(rows) - 1
	start := last
	previous := s.Previous[symbol]
	states := s.Setups[symbol]
	if !s.LastCandle[symbol].IsZero() {
		for start > 0 && bars[start-1].Time.After(s.LastCandle[symbol]) {
			start--
		}
	}
	if previous == nil || !s.LastCandle[symbol].Equal(bars[start].Time.Add(-step)) {
		previous = nil
		if start > 0 {
			previous = rows[start-1]
		}
	}
	var decisionPrevious map[string]float64
	for i := start; i <= last; i++ {
		strategy.ApplyPreviousRuntimeHistory(rows[i], previous)
		// Missed frames may cancel/expire a pending event, but cannot create an
		// actionable retrospective signal or make retrospective LLM requests.
		states = core.AdvanceSetups(strategy, states, rows[i], previous, bars[i].Time.Add(step), step, side, i == last)
		decisionPrevious = previous
		previous = rows[i]
	}
	return rows[last], decisionPrevious, states
}

func (m *Sessions) sessionSizing(s *Session) (core.Strategy, core.BacktestRequest) {
	strategy := s.Strategy.Strategy
	limit := s.Request.MaxOrderNotional
	if s.Request.Mode == "live" && m.MaxLiveNotional > 0 {
		limit = math.Min(limit, m.MaxLiveNotional)
	}
	if strategy.Risk.MaxNotional > 0 {
		limit = math.Min(limit, strategy.Risk.MaxNotional)
	}
	strategy.Risk.MaxNotional = limit
	return strategy, core.BacktestRequest{FeeBPS: s.Request.FeeBPS, SlippageBPS: s.Request.SlippageBPS}
}
func decisionExecution(s *Session, symbol string, at time.Time, audit *core.ExecutionAudit) {
	for i := len(s.Events) - 1; i >= 0; i-- {
		e := &s.Events[i]
		if e.Symbol == symbol && e.Decision != nil && e.Decision.Time.Equal(at) {
			e.Decision.Execution = audit
			if audit.Status == "skipped" {
				e.Decision.Action = "hold"
				e.Decision.Reason = audit.Reason
			}
			return
		}
	}
}
func (m *Sessions) skipEntry(s *Session, symbol string, at time.Time, audit *core.ExecutionAudit) error {
	audit.Status = "skipped"
	decisionExecution(s, symbol, at, audit)
	s.Events = append(s.Events, SessionEvent{Symbol: symbol, Time: m.nowUTC(), Execution: audit})
	trimEvents(s)
	return m.App.Store.Put("sessions", s.ID, s)
}

func (m *Sessions) fillWithPlan(ctx context.Context, s *Session, symbol, action string, quote float64, reason string, signalTime time.Time, plan *core.EntryPlan) error {
	if reason == "strategy" && !m.withinSessionFrame(signalTime) {
		return errMissedSessionFrame
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if !core.Finite(quote) || quote <= 0 {
		return fmt.Errorf("invalid execution quote")
	}
	p := s.Positions[symbol]
	entry := strings.HasPrefix(action, "enter_")
	buy := action == "enter_long" || action == "exit_short"
	strategy, costs := m.sessionSizing(s)
	qty := p.Quantity
	var audit *core.ExecutionAudit
	if entry {
		if p.Side != "" {
			return fmt.Errorf("position already open")
		}
		if plan == nil {
			if strategy.Risk.Long != nil || strategy.Risk.Short != nil || len(strategy.Setups) > 0 {
				return fmt.Errorf("entry requires a frozen decision plan")
			}
			plan = &core.EntryPlan{Side: strings.TrimPrefix(action, "enter_"), SignalTime: signalTime, ReferencePrice: quote, RiskBudget: s.Balances[symbol] * strategy.Risk.RiskFraction}
		}
		if action != "enter_"+plan.Side || !plan.SignalTime.Equal(signalTime) {
			return fmt.Errorf("entry plan does not match signal")
		}
		daily := sessionDay(s, symbol, m.nowUTC())
		if strategy.Risk.MaxEntriesPerDay > 0 && daily.Count >= strategy.Risk.MaxEntriesPerDay {
			return m.skipEntry(s, symbol, signalTime, &core.ExecutionAudit{Action: action, Plan: plan, Reason: "daily entry limit"})
		}
		sized, a := core.SizeEntry(strategy, costs, plan, s.Balances[symbol], core.Candle{Time: m.nowUTC(), Open: quote})
		audit = a
		if sized.Side == "" {
			return m.skipEntry(s, symbol, signalTime, audit)
		}
		qty = sized.Quantity
	} else if p.Side == "" {
		return nil
	}
	side := "SELL"
	if buy {
		side = "BUY"
	}
	intent := Intent{Order: execution.Order{Symbol: symbol, Side: side, Quantity: strconv.FormatFloat(qty, 'f', -1, 64), ReduceOnly: !entry, ClientID: "arc-" + store.Hash([]string{s.ID, symbol, action, signalTime.Format(time.RFC3339Nano)})[:28]}, Action: action, Reason: reason, At: m.nowUTC(), SignalTime: signalTime, Plan: plan, BalanceBefore: s.Balances[symbol]}
	if s.Request.Mode == "paper" {
		price := core.Slipped(quote, s.Request.SlippageBPS, buy)
		m.applyFill(s, intent, qty, price, nil)
		return m.App.Store.Put("sessions", s.ID, s)
	}
	if !m.LiveEnabled {
		return fmt.Errorf("live execution is disabled")
	}
	broker := m.Brokers[s.Request.Exchange]
	if e := m.matchPosition(ctx, s, symbol); e != nil {
		return e
	}
	if e := broker.CheckAccount(ctx, []string{symbol}); e != nil {
		return e
	}
	order, e := broker.Prepare(ctx, intent.Order, quote)
	if e != nil {
		return e
	}
	prepared, e := strconv.ParseFloat(order.Quantity, 64)
	if e != nil || !core.Finite(prepared) || prepared <= 0 || prepared > qty*(1+1e-10) || order.Symbol != intent.Order.Symbol || order.Side != intent.Order.Side || order.ReduceOnly != intent.Order.ReduceOnly || order.ClientID != intent.Order.ClientID {
		return fmt.Errorf("broker returned an invalid prepared order")
	}
	if !entry && math.Abs(prepared-qty) > math.Max(1e-10, qty*1e-8) {
		return fmt.Errorf("broker changed reduce-only quantity")
	}
	if entry {
		quote, e = broker.Quote(ctx, symbol)
		if e != nil {
			return e
		}
		if !core.Finite(quote) || quote <= 0 {
			return fmt.Errorf("invalid execution quote")
		}
		if sized, a := core.SizeEntry(strategy, costs, plan, s.Balances[symbol], core.Candle{Time: m.nowUTC(), Open: quote}); sized.Side == "" {
			return m.skipEntry(s, symbol, signalTime, a)
		}
		// Recheck the rounded quantity at the latest quote, with the configured
		// slippage reserve. Never silently increase quantity after broker rounding.
		_, audit = core.ConfirmEntry(strategy, costs, plan, s.Balances[symbol], prepared, core.Slipped(quote, costs.SlippageBPS, buy), m.nowUTC())
		if audit.Status != "filled" {
			return m.skipEntry(s, symbol, signalTime, audit)
		}
	}
	if reason == "strategy" && !m.withinSessionFrame(signalTime) {
		return errMissedSessionFrame
	}
	intent.Order = order
	intent.At = m.nowUTC()
	s.Pending = &intent
	if audit != nil {
		audit.Status = "pending"
		decisionExecution(s, symbol, signalTime, audit)
	}
	if e = m.App.Store.Put("sessions", s.ID, s); e != nil {
		return e
	}
	submitCtx := ctx
	if reason == "strategy" {
		if !m.withinSessionFrame(signalTime) {
			s.Pending = nil
			if e = m.App.Store.Put("sessions", s.ID, s); e != nil {
				return e
			}
			return errMissedSessionFrame
		}
		var cancel context.CancelFunc
		submitCtx, cancel = context.WithTimeout(ctx, signalTime.Add(sessionFrameWindow).Sub(m.nowUTC()))
		defer cancel()
	}
	receipt, e := broker.Submit(submitCtx, order)
	if e != nil {
		return e
	}
	if e = m.complete(s, receipt); e != nil {
		return e
	}
	if entry && s.EntryViolations[symbol] != "" {
		return m.closeEntryViolation(ctx, s, symbol)
	}
	return nil
}

func (m *Sessions) applyFill(s *Session, in Intent, qty, price float64, receipt *execution.Receipt) {
	symbol := in.Order.Symbol
	fee := qty * price * s.Request.FeeBPS / 10000
	event := SessionEvent{Symbol: symbol, Time: in.At, Fill: &core.Fill{Time: in.At, Action: in.Action, Price: price, Quantity: qty, Reason: in.Reason}, Receipt: receipt}
	if strings.HasPrefix(in.Action, "enter_") {
		plan := in.Plan
		if plan == nil {
			plan = &core.EntryPlan{Side: strings.TrimPrefix(in.Action, "enter_"), SignalTime: in.SignalTime}
		}
		balance := in.BalanceBefore
		if balance == 0 {
			balance = s.Balances[symbol]
		}
		strategy, costs := m.sessionSizing(s)
		p, audit := core.ConfirmEntry(strategy, costs, plan, balance, qty, price, in.At)
		s.Positions[symbol] = p
		s.Balances[symbol] -= fee
		daily := sessionDay(s, symbol, in.At)
		daily.Count++
		s.Daily[symbol] = daily
		event.Fill.StopPrice = p.StopPrice
		event.Fill.TargetPrice = p.TargetPrice
		event.Fill.InitialRisk = p.InitialRisk
		event.Fill.EventID = p.EventID
		event.Execution = audit
		decisionExecution(s, symbol, in.SignalTime, audit)
		if audit.Status == "risk_breached" {
			if s.EntryViolations == nil {
				s.EntryViolations = map[string]string{}
			}
			s.EntryViolations[symbol] = audit.Reason
		}
	} else {
		p := s.Positions[symbol]
		pnl := p.PnL(price) - p.EntryFee - fee
		pnlR := 0.0
		if p.InitialRisk > 0 {
			pnlR = pnl / p.InitialRisk
		}
		event.Trade = &core.Trade{Side: p.Side, EntryTime: p.Time, ExitTime: in.At, Entry: p.Entry, Exit: price, Quantity: qty, Fees: p.EntryFee + fee, PnL: pnl, PnLR: pnlR, Reason: in.Reason, StopPrice: p.StopPrice, TargetPrice: p.TargetPrice, InitialRisk: p.InitialRisk, EventID: p.EventID}
		s.Balances[symbol] += p.PnL(price) - fee
		s.Positions[symbol] = core.Position{}
		delete(s.EntryViolations, symbol)
	}
	s.Events = append(s.Events, event)
	trimEvents(s)
}

func (m *Sessions) closeEntryViolation(ctx context.Context, s *Session, symbol string) error {
	p := s.Positions[symbol]
	if p.Side == "" {
		return nil
	}
	quote, e := m.Brokers[s.Request.Exchange].Quote(ctx, symbol)
	if e != nil {
		return e
	}
	return m.fill(ctx, s, symbol, "exit_"+p.Side, quote, "entry_risk_violation", m.nowUTC())
}
