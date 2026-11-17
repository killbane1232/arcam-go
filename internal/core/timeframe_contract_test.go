package core

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These assertions run through the backtest, not just the feature builder:
// a mutation after t must not change decisions, plans or executions before t.
func TestBacktestTimeframesNeverUseFutureCandles(t *testing.T) {
	for _, interval := range []string{"15m", "1h"} {
		t.Run(interval, func(t *testing.T) {
			step, _ := Interval(interval)
			dayBars := int(24 * time.Hour / step)
			c := make([]Candle, 21*dayBars)
			start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
			for i := range c {
				v := 100 + 5*math.Sin(float64(i)*.21)
				c[i] = Candle{Time: start.Add(time.Duration(i) * step), Open: v, High: v + 2, Low: v - 2, Close: v + 1, Volume: 10}
			}
			s := plainStrategy()
			s.Indicators = []Indicator{{ID: "daily", Type: "ohlc", Timeframe: "1d"}, {ID: "adr5", Type: "adr", Period: 5, Timeframe: "1d"}, {ID: "atr14", Type: "atr", Period: 14, Timeframe: "1d"}, {ID: "h4", Type: "ema", Period: 3, Timeframe: "4h"}, {ID: "p", Type: "pivot", Left: 2, Right: 2, Timeframe: "4h"}}
			s.Rules.EnterLong = Compare("close", "gt", "h4.value")
			s.Rules.ExitLong = Compare("close", "lt", "h4.value")
			first := 16 * dayBars
			r := BacktestRequest{Symbols: []string{"BTCUSDT"}, Interval: interval, From: c[first].Time, To: c[len(c)-1].Time.Add(step), Capital: 1000}
			baseline, e := Backtest(context.Background(), s, r, "BTCUSDT", c, nil)
			if e != nil {
				t.Fatal(e)
			}
			h4Bars := int(4 * time.Hour / step)
			for _, cut := range []int{first + h4Bars - 1, first + h4Bars, first + h4Bars + 1, first + dayBars - 1, first + dayBars, first + dayBars + 1} {
				changed := append([]Candle(nil), c...)
				for i := cut; i < len(changed); i++ {
					changed[i].Open *= 10
					changed[i].High *= 11
					changed[i].Low *= 9
					changed[i].Close *= 10
					changed[i].Volume *= 100
				}
				got, e := Backtest(context.Background(), s, r, "BTCUSDT", changed, nil)
				if e != nil {
					t.Fatal(e)
				}
				n := cut - first
				if !reflect.DeepEqual(got.Decisions[:n], baseline.Decisions[:n]) {
					t.Fatalf("future OHLC changed decisions before %s", c[cut].Time)
				}
				pastFills := func(p PairResult) []Fill {
					out := []Fill{}
					for _, f := range p.Fills {
						if f.Time.Before(c[cut].Time) {
							out = append(out, f)
						}
					}
					return out
				}
				if !reflect.DeepEqual(pastFills(got), pastFills(baseline)) {
					t.Fatal("future OHLC changed previous executions")
				}

				for i := 0; i < n; i++ {
					d := got.Decisions[i]
					for field, v := range d.Features {
						if strings.HasSuffix(field, ".available_at") && v > float64(d.Time.Unix()) {
							t.Fatalf("%s available after decision: %g > %s", field, v, d.Time)
						}
					}
				}
				// Appending a future suffix must also preserve the entire feature prefix.
				prefix, e := BuildFeatures(c[:cut], s, interval)
				if e != nil {
					t.Fatal(e)
				}
				full, e := BuildFeatures(changed, s, interval)
				if e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(prefix, full[:cut]) {
					t.Fatalf("future suffix changed %s feature prefix", interval)
				}
				for _, p := range []PairResult{baseline, got} {
					for _, f := range p.Fills {
						if f.Action == "enter_long" || f.Action == "enter_short" {
							signal := int(f.Time.Sub(r.From)/step) - 1
							if signal < 0 || signal >= len(p.Decisions) || p.Decisions[signal].Action != f.Action {
								t.Fatal("entry was not scheduled by the preceding closed candle")
							}
						}
					}
				}
			}
			// Shorter requested ranges cannot gain a completed daily/H4 context from
			// extra candles supplied past the exclusive end of that requested range.
			cut := first + dayBars - 1
			r.To = c[cut].Time
			prefix, e := Backtest(context.Background(), s, r, "BTCUSDT", c[:cut], nil)
			if e != nil {
				t.Fatal(e)
			}
			full, e := Backtest(context.Background(), s, r, "BTCUSDT", c, nil)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(prefix, full) {
				t.Fatal("extra input past to changed the backtest")
			}
		})
	}
}
