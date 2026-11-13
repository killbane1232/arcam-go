package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"arcam/internal/core"
	"arcam/internal/store"
)

type candleRange struct{ from, to time.Time }

const historyBatch = 5000

func missingRanges(bars []core.Candle, from, to time.Time, step time.Duration) []candleRange {
	gaps := []candleRange{}
	cursor := from
	for _, c := range bars {
		if c.Time.Before(cursor) {
			continue
		}
		if !c.Time.Before(to) {
			break
		}
		if c.Time.After(cursor) {
			gaps = append(gaps, candleRange{cursor, c.Time})
		}
		cursor = c.Time.Add(step)
	}
	if cursor.Before(to) {
		gaps = append(gaps, candleRange{cursor, to})
	}
	return gaps
}
func (s *Service) cachedRange(ctx context.Context, source, symbol, interval string, from, to time.Time) ([]core.Candle, error) {
	d, e := s.Store.DatasetRange(ctx, source, symbol, interval, from, to)
	if os.IsNotExist(e) {
		return []core.Candle{}, nil
	}
	return d.Candles, e
}
func (s *Service) coverageError(r core.BacktestRequest, symbol string, from time.Time, bars []core.Candle, gaps []candleRange) error {
	step, _ := core.Interval(r.Interval)
	count := 0
	for _, g := range gaps {
		count += int(g.to.Sub(g.from) / step)
	}
	first := gaps[0]
	kind := "test period"
	if first.from.Before(r.From) {
		kind = "warmup"
	}
	available := "no candles in required range"
	if len(bars) > 0 {
		available = fmt.Sprintf("available in required range: [%s, %s)", bars[0].Time.Format(time.RFC3339), bars[len(bars)-1].Time.Add(step).Format(time.RFC3339))
	}
	hint := "Import the missing range for this exact source, symbol and interval."
	if r.Source == "demo" {
		hint = "DEMO is synthetic and cannot be downloaded from an exchange. Select binance_futures or binance_spot for market history."
	}
	if sets, e := s.Store.Datasets(); e == nil {
		for _, d := range sets {
			if d.Symbol == symbol && d.Interval == r.Interval && d.Source != r.Source && !d.From.After(from) && !d.To.Before(r.To) {
				hint += fmt.Sprintf(" Other source covering these dates: %s.", d.Source)
			}
		}
	}
	return fmt.Errorf("%s / %s / %s: missing %d candles across %d ranges; first gap in %s: [%s, %s) UTC; required [%s, %s); %s. %s", r.Source, symbol, r.Interval, count, len(gaps), kind, first.from.Format(time.RFC3339), first.to.Format(time.RFC3339), from.Format(time.RFC3339), r.To.Format(time.RFC3339), available, hint)
}

// Fill only holes in the frozen snapshot. Already-cached values are never
// replaced by imports racing with a queued run. A per-dataset gate lets another
// preparation reuse completed downloads, and can be cancelled while waiting.
func (s *Service) prepareHistory(ctx context.Context, r core.BacktestRequest, symbol string, from time.Time, frozen []core.Candle, progress func(core.DataLoad, bool) error) ([]core.Candle, error) {
	step, _ := core.Interval(r.Interval)
	gaps := missingRanges(frozen, from, r.To, step)
	if len(gaps) == 0 {
		return frozen, nil
	}
	source := s.Sources[r.Source]
	if source == nil {
		return nil, s.coverageError(r, symbol, from, frozen, gaps)
	}
	key := store.DatasetKey(r.Source, symbol, r.Interval)
	value, _ := s.downloadLocks.LoadOrStore(key, make(chan struct{}, 1))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	bars := append([]core.Candle(nil), frozen...)
	// Another run or an explicit import may already have filled some holes.
	for _, gap := range gaps {
		cached, e := s.cachedRange(ctx, r.Source, symbol, r.Interval, gap.from, gap.to)
		if e != nil {
			return nil, e
		}
		bars = append(bars, cached...)
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Time.Before(bars[j].Time) })
	gaps = missingRanges(bars, from, r.To, step)
	for _, gap := range gaps {
		for start := gap.from; start.Before(gap.to); {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			end := start.Add(historyBatch * step)
			if end.After(gap.to) {
				end = gap.to
			}
			load := core.DataLoad{Symbol: symbol, From: start, To: end}
			if e := progress(load, false); e != nil {
				return nil, e
			}
			fetched, e := source.Candles(ctx, symbol, r.Interval, start, end)
			if e != nil {
				return nil, fmt.Errorf("%s / %s / %s: auto-download [%s,%s) failed: %w", r.Source, symbol, r.Interval, start.Format(time.RFC3339), end.Format(time.RFC3339), e)
			}
			if len(fetched) > 0 {
				if e = core.ValidateCandles(fetched, r.Interval); e != nil {
					return nil, fmt.Errorf("invalid downloaded candles: %w", e)
				}
				for _, c := range fetched {
					if c.Time.Before(start) || !c.Time.Before(end) {
						return nil, fmt.Errorf("source returned candles outside requested gap")
					}
				}
				if e = ctx.Err(); e != nil {
					return nil, e
				}
				if _, e = s.Store.Merge(store.Dataset{Source: r.Source, Symbol: symbol, Interval: r.Interval, Candles: fetched}); e != nil {
					return nil, e
				}
				bars = append(bars, fetched...)
				load.Candles = len(fetched)
				if e = progress(load, true); e != nil {
					return nil, e
				}
			}
			if remaining := missingRanges(fetched, start, end, step); len(remaining) > 0 {
				return nil, fmt.Errorf("%s / %s / %s: source did not supply all requested candles; first unavailable range [%s,%s). No synthetic candles were inserted", r.Source, symbol, r.Interval, remaining[0].from.Format(time.RFC3339), remaining[0].to.Format(time.RFC3339))
			}
			start = end
		}
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Time.Before(bars[j].Time) })
	if e := core.ValidateCandles(bars, r.Interval); e != nil {
		return nil, e
	}
	if gaps = missingRanges(bars, from, r.To, step); len(gaps) > 0 {
		return nil, s.coverageError(r, symbol, from, bars, gaps)
	}
	return bars, nil
}
