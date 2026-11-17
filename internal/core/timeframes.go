package core

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func indicatorWarmup(i Indicator) int {
	switch i.Type {
	case "ichimoku":
		return i.Period + i.Displacement + 1
	case "macd":
		return i.Slow + i.Signal + 1
	case "pivot":
		return i.Left + i.Right + 1
	case "ohlc":
		return 1
	default:
		return i.Period + 1
	}
}

// WarmupBars includes alignment slack for complete context candles and offsets.
func (s Strategy) WarmupBars(interval string) (int, error) {
	step, e := Interval(interval)
	if e != nil {
		return 0, e
	}
	n := s.Warmup()
	for _, i := range s.Indicators {
		if i.Timeframe == "" {
			continue
		}
		tf, e := Interval(i.Timeframe)
		if e != nil {
			return 0, e
		}
		if tf < step || tf%step != 0 {
			return 0, fmt.Errorf("%s: timeframe must be an integer multiple of execution interval", i.ID)
		}
		n = max(n, (indicatorWarmup(i)+1)*int(tf/step))
	}
	offset := 0
	s.operands(func(o *Operand) {
		offset = max(offset, o.Offset)
		if strings.HasPrefix(o.Field, "day.") || strings.HasPrefix(o.Field, "previous_day.") {
			n = max(n, 2*int(24*time.Hour/step)+1)
		}
	})
	n += offset
	if n > 200000 {
		return 0, fmt.Errorf("context warmup exceeds 200000 execution bars")
	}
	return n, nil
}

// AggregateClosed emits only complete, contiguous UTC buckets. All source
// candles are frozen in Run.InputData and included in the existing data hash.
func AggregateClosed(c []Candle, base, target time.Duration) []Candle {
	out := []Candle{}
	var bucket Candle
	var start, expected time.Time
	count := 0
	valid := false
	for _, b := range c {
		t := b.Time.UTC().Truncate(target)
		if !t.Equal(start) {
			start = t
			bucket = b
			bucket.Time = t
			count = 0
			expected = t
			valid = true
		}
		if !b.Time.Equal(expected) {
			valid = false
		}
		if count > 0 {
			bucket.High = math.Max(bucket.High, b.High)
			bucket.Low = math.Min(bucket.Low, b.Low)
			bucket.Close = b.Close
			bucket.Volume += b.Volume
		}
		count++
		expected = b.Time.Add(base)
		if expected.Equal(t.Add(target)) && valid && count == int(target/base) {
			out = append(out, bucket)
		}
	}
	return out
}

// BuildFeatures joins a context bar only at/after its explicit close time.
// A gap resets indicators; an incomplete bucket cannot be carried as current.
func BuildFeatures(c []Candle, s Strategy, interval string) ([]map[string]float64, error) {
	if _, e := s.WarmupBars(interval); e != nil {
		return nil, e
	}
	step, _ := Interval(interval)
	base := []Indicator{}
	groups := map[time.Duration][]Indicator{}
	for _, i := range s.Indicators {
		tf := step
		if i.Timeframe != "" {
			tf, _ = Interval(i.Timeframe)
		}
		if tf == step {
			base = append(base, i)
		} else {
			groups[tf] = append(groups[tf], i)
		}
	}
	out := continuousFeatures(c, base, step)
	for tf, ind := range groups {
		bars := AggregateClosed(c, step, tf)
		rows := continuousFeatures(bars, ind, tf)
		k := -1
		for j, b := range c {
			at := b.Time.Add(step)
			for k+1 < len(bars) && !bars[k+1].Time.Add(tf).After(at) {
				k++
			}
			if k < 0 || !bars[k].Time.Equal(at.Truncate(tf).Add(-tf)) {
				continue
			}
			for _, i := range ind {
				for _, key := range append(Outputs(i), "available_at", "bar_time") {
					if v, ok := rows[k][i.ID+"."+key]; ok {
						out[j][i.ID+"."+key] = v
					}
				}
			}
		}
	}
	// Daily fields use the UTC date of the execution candle. The first partial
	// day is unavailable, since extrema would otherwise silently omit history.
	days := AggregateClosed(c, step, 24*time.Hour)
	daily := map[int64]Candle{}
	for _, b := range days {
		daily[b.Time.Unix()] = b
	}
	var day time.Time
	var hi, lo, op float64
	var expected time.Time
	valid := false
	for j, b := range c {
		date := b.Time.UTC().Truncate(24 * time.Hour)
		if !date.Equal(day) {
			day = date
			hi = b.High
			lo = b.Low
			op = b.Open
			expected = date
			valid = true
		}
		if !b.Time.Equal(expected) {
			valid = false
		}
		expected = b.Time.Add(step)
		hi = math.Max(hi, b.High)
		lo = math.Min(lo, b.Low)
		if valid {
			out[j]["day.high"] = hi
			out[j]["day.low"] = lo
			out[j]["day.open"] = op
			out[j]["day.range"] = hi - lo
			out[j]["day.time"] = float64(date.Unix())
			out[j]["day.available_at"] = float64(expected.Unix())
		}
		if p, ok := daily[date.Add(-24*time.Hour).Unix()]; ok {
			for key, v := range map[string]float64{"open": p.Open, "high": p.High, "low": p.Low, "close": p.Close, "range": p.High - p.Low, "time": float64(p.Time.Unix()), "available_at": float64(date.Unix())} {
				out[j]["previous_day."+key] = v
			}
		}
	}
	h := s.historyOperands()
	for j := range out {
		addHistory(h, out, j)
	}
	return out, nil
}

func continuousFeatures(c []Candle, ind []Indicator, step time.Duration) []map[string]float64 {
	out := make([]map[string]float64, 0, len(c))
	for start := 0; start < len(c); {
		end := start + 1
		for end < len(c) && c[end].Time.Equal(c[end-1].Time.Add(step)) {
			end++
		}
		rows := Features(c[start:end], ind)
		for j, row := range rows {
			for _, i := range ind {
				row[i.ID+".bar_time"] = float64(c[start+j].Time.Unix())
				row[i.ID+".available_at"] = float64(c[start+j].Time.Add(step).Unix())
			}
		}
		out = append(out, rows...)
		start = end
	}
	return out
}

// WarmupByInterval makes validation usable before an execution interval is selected.
func (s Strategy) WarmupByInterval() map[string]int {
	out := map[string]int{}
	for _, interval := range []string{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d"} {
		if n, e := s.WarmupBars(interval); e == nil {
			out[interval] = n
		}
	}
	return out
}
