package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arcam/internal/core"
	"arcam/internal/store"
)

// This is transport/scheduler tolerance after a UTC candle boundary, not an
// intrabar signal window. Data and the decision timestamp stay at the boundary.
const sessionFrameWindow = 5 * time.Second
const missedSessionFrameReason = "candle execution window missed; waiting for the next boundary"

var errMissedSessionFrame = errors.New(missedSessionFrameReason)

func nextSessionPoll(now time.Time) time.Time {
	return now.UTC().Truncate(sessionFrameWindow).Add(sessionFrameWindow)
}
func (m *Sessions) withinSessionFrame(boundary time.Time) bool {
	now := m.nowUTC()
	return !now.Before(boundary) && now.Before(boundary.Add(sessionFrameWindow))
}

// Preserve the same warmup origin and accepted source prefix as a backtest.
// Re-seeding recursive indicators on a sliding window changes their values.
// Only new, fully closed candles are requested; historical corrections in the
// shared dataset or provider cannot rewrite a running session's input.
func (m *Sessions) closedSessionHistory(ctx context.Context, s *Session, symbol string, end time.Time, warmup int) ([]core.Candle, error) {
	step, e := core.Interval(s.Request.Interval)
	if e != nil {
		return nil, e
	}
	frozen := s.InputData[symbol]
	from := end.Add(-time.Duration(warmup+1) * step)
	if len(frozen) > 0 {
		if e = core.ValidateCandles(frozen, s.Request.Interval); e != nil {
			return nil, fmt.Errorf("%s: invalid saved session history: %w", symbol, e)
		}
		for i := 1; i < len(frozen); i++ {
			if !frozen[i].Time.Equal(frozen[i-1].Time.Add(step)) {
				return nil, fmt.Errorf("%s: saved session history gap", symbol)
			}
		}
		from = frozen[len(frozen)-1].Time.Add(step)
		if from.After(end) {
			return nil, fmt.Errorf("%s: saved history extends beyond decision boundary", symbol)
		}
	}
	bars := append([]core.Candle(nil), frozen...)
	for cursor := from; cursor.Before(end); {
		to := cursor.Add(historyBatch * step)
		if to.After(end) {
			to = end
		}
		fetched, e := m.App.Sources[s.Request.Exchange].Candles(ctx, symbol, s.Request.Interval, cursor, to)
		if e != nil {
			return nil, e
		}
		if len(fetched) != int(to.Sub(cursor)/step) {
			return nil, fmt.Errorf("%s: incomplete or out-of-range session candles", symbol)
		}
		if e = core.ValidateCandles(fetched, s.Request.Interval); e != nil {
			return nil, e
		}
		for i, b := range fetched {
			if !b.Time.Equal(cursor.Add(time.Duration(i)*step)) || b.Time.Add(step).After(end) {
				return nil, fmt.Errorf("%s: session candle outside closed decision range", symbol)
			}
		}
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		if _, e = m.App.Store.Merge(store.Dataset{Source: s.Request.Exchange, Symbol: symbol, Interval: s.Request.Interval, Candles: fetched}); e != nil {
			return nil, e
		}
		bars = append(bars, fetched...)
		cursor = to
	}
	if len(bars) < warmup+1 || !bars[len(bars)-1].Time.Add(step).Equal(end) {
		return nil, fmt.Errorf("%s: incomplete warmup or stale market data", symbol)
	}
	if e = core.ValidateCandles(bars, s.Request.Interval); e != nil {
		return nil, e
	}
	if s.InputData == nil {
		s.InputData = map[string][]core.Candle{}
	}
	s.InputData[symbol] = bars
	return bars, nil
}
