package core

import (
	"context"
	"testing"
	"time"
)

func TestSnapshotsAreConsistentAndCancellationKeepsOpenPosition(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []Candle{}
	for i := 0; i < 5; i++ {
		bars = append(bars, Candle{Time: start.Add(time.Duration(i) * time.Hour), Open: 100, High: 130, Low: 90, Close: 100 + float64(i)*5})
	}
	r := BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: "1h", From: start, To: start.Add(5 * time.Hour), Capital: 1000, FeeBPS: 10}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapshots := []PairResult{}
	out, e := BacktestWithUpdates(ctx, plainStrategy(), r, "BTCUSDT", bars, nil, func(p PairResult) {
		snapshots = append(snapshots, p)
		if len(p.Candles) == 2 {
			cancel()
		}
	})
	if e != context.Canceled || len(out.Candles) != 2 || out.OpenPosition == nil || out.Complete || len(out.Trades) != 0 || len(out.Fills) != 1 {
		t.Fatal("cancellation must preserve an open position at the committed prefix", out, e)
	}
	if len(snapshots) != 2 || len(snapshots[0].Candles) != 1 || snapshots[0].OpenPosition != nil || len(snapshots[0].Fills) != 0 || snapshots[0].Metrics.Final != 1000 {
		t.Fatal("previous snapshot changed", snapshots)
	}
	near(t, out.Metrics.Final, out.Balance+out.UnrealizedPnL)
	near(t, out.Metrics.Fees, out.OpenPosition.EntryFee)
	if out.Metrics.Fees <= 0 || out.Metrics.Trades != 0 {
		t.Fatal("open entry fee missing or open trade counted as closed")
	}
	finished, e := Backtest(context.Background(), plainStrategy(), r, "BTCUSDT", bars, nil)
	if e != nil || !finished.Complete || finished.OpenPosition != nil {
		t.Fatal(finished, e)
	}
	expected := CalculateMetrics(r.Capital, finished.Trades, finished.Equity)
	near(t, finished.Metrics.Final, expected.Final)
	near(t, finished.Metrics.Fees, expected.Fees)
	near(t, finished.Metrics.MaxDrawdownPct, expected.MaxDrawdownPct)
	near(t, finished.Metrics.WinRatePct, expected.WinRatePct)
}
