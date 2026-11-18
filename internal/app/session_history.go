package app

import (
	"sort"
	"strings"
	"time"

	"arcam/internal/core"
)

// SessionHistory is a read-only projection of one persisted snapshot. Viewing it
// never fetches market data, evaluates a strategy, or contacts an execution broker.
type SessionHistory struct {
	Session Session              `json:"session"`
	Pairs   []SessionHistoryPair `json:"pairs"`
}

type SessionHistoryPair struct {
	Symbol    string             `json:"symbol"`
	Candles   []core.Candle      `json:"candles"`
	Decisions []core.Decision    `json:"decisions"`
	Events    []SessionEvent     `json:"events"`
	Trades    []core.Trade       `json:"trades"`
	Balance   []core.EquityPoint `json:"balance"`
}

func (m *Sessions) History(id string) (SessionHistory, error) {
	var s Session
	if e := m.App.Store.Get("sessions", id, &s); e != nil {
		return SessionHistory{}, e
	}
	step, e := core.Interval(s.Request.Interval)
	if e != nil {
		return SessionHistory{}, e
	}
	out := SessionHistory{Session: s, Pairs: []SessionHistoryPair{}}
	out.Session.Events, out.Session.InputData, out.Session.Previous = nil, nil, nil
	for _, symbol := range s.Request.Symbols {
		p := SessionHistoryPair{Symbol: symbol, Candles: []core.Candle{}, Decisions: []core.Decision{}, Events: []SessionEvent{}, Trades: []core.Trade{}, Balance: []core.EquityPoint{}}
		var from time.Time
		for _, event := range s.Events {
			if event.Symbol != symbol {
				continue
			}
			at := event.Time.Truncate(step)
			if event.Decision != nil {
				p.Decisions = append(p.Decisions, *event.Decision)
				at = event.Decision.Time.Add(-step)
			}
			if from.IsZero() || at.Before(from) {
				from = at
			}
			if event.Fill != nil || event.Execution != nil || event.Receipt != nil {
				p.Events = append(p.Events, event)
			}
			if event.Trade != nil {
				p.Trades = append(p.Trades, *event.Trade)
			}
		}
		if !from.IsZero() {
			for _, bar := range s.InputData[symbol] {
				if !bar.Time.Before(from) {
					p.Candles = append(p.Candles, bar)
				}
			}
		}
		sort.SliceStable(p.Decisions, func(i, j int) bool { return p.Decisions[i].Time.Before(p.Decisions[j].Time) })
		sort.SliceStable(p.Events, func(i, j int) bool { return p.Events[i].Time.Before(p.Events[j].Time) })
		sort.SliceStable(p.Trades, func(i, j int) bool { return p.Trades[i].ExitTime.Before(p.Trades[j].ExitTime) })
		// Anchor to the actual recorded balance so a trimmed journal cannot reset
		// it to the original capital. This is realized balance, not marked equity.
		balance := s.Balances[symbol]
		complete := true
		for i := len(p.Events) - 1; i >= 0; i-- {
			event := p.Events[i]
			if event.Fill == nil {
				continue
			}
			p.Balance = append(p.Balance, core.EquityPoint{Time: event.Time, Value: balance})
			fee := event.Fill.Quantity * event.Fill.Price * s.Request.FeeBPS / 10000
			if strings.HasPrefix(event.Fill.Action, "enter_") {
				balance += fee
			} else if event.Trade != nil {
				balance -= event.Trade.PnL + event.Trade.Fees - fee
			} else {
				// Older sessions may have exits without a saved trade. Their earlier
				// balance is unknown; keep the known suffix instead of inventing it.
				complete = false
				break
			}
		}
		if complete && !from.IsZero() {
			p.Balance = append(p.Balance, core.EquityPoint{Time: from, Value: balance})
		}
		for i, j := 0, len(p.Balance)-1; i < j; i, j = i+1, j-1 {
			p.Balance[i], p.Balance[j] = p.Balance[j], p.Balance[i]
		}
		out.Pairs = append(out.Pairs, p)
	}
	return out, nil
}
