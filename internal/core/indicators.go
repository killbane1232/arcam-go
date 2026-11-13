package core

import "math"

// Features are causal: each row depends only on candles through that row.
// Ichimoku spans are shifted from the past to their displayed timestamp.
func Features(c []Candle, ind []Indicator) []map[string]float64 {
	out := make([]map[string]float64, len(c))
	close := make([]float64, len(c))
	for j, v := range c {
		close[j] = v.Close
		out[j] = map[string]float64{"open": v.Open, "high": v.High, "low": v.Low, "close": v.Close, "volume": v.Volume}
	}
	add := func(id, key string, v []float64) {
		for j, x := range v {
			if Finite(x) {
				out[j][id+"."+key] = x
			}
		}
	}
	for _, i := range ind {
		switch i.Type {
		case "ema":
			add(i.ID, "value", ema(close, i.Period))
		case "sma":
			add(i.ID, "value", sma(close, i.Period))
		case "rsi":
			gains, losses := blank(len(c)), blank(len(c))
			for j := 1; j < len(c); j++ {
				delta := close[j] - close[j-1]
				gains[j] = math.Max(delta, 0)
				losses[j] = math.Max(-delta, 0)
			}
			g, l := smooth(gains, i.Period, 1/float64(i.Period)), smooth(losses, i.Period, 1/float64(i.Period))
			v := blank(len(c))
			for j := range v {
				if Finite(g[j]) {
					switch {
					case g[j] == 0 && l[j] == 0:
						v[j] = 50
					case l[j] == 0:
						v[j] = 100
					default:
						v[j] = 100 - 100/(1+g[j]/l[j])
					}
				}
			}
			add(i.ID, "value", v)
		case "atr":
			v := blank(len(c))
			for j := 1; j < len(c); j++ {
				v[j] = math.Max(c[j].High-c[j].Low, math.Max(math.Abs(c[j].High-c[j-1].Close), math.Abs(c[j].Low-c[j-1].Close)))
			}
			add(i.ID, "value", smooth(v, i.Period, 1/float64(i.Period)))
		case "aroon":
			up, down, osc := blank(len(c)), blank(len(c)), blank(len(c))
			for j := i.Period; j < len(c); j++ {
				hi, lo := j-i.Period, j-i.Period
				for k := j - i.Period; k <= j; k++ {
					if c[k].High >= c[hi].High {
						hi = k
					}
					if c[k].Low <= c[lo].Low {
						lo = k
					}
				}
				up[j] = 100 * float64(i.Period-(j-hi)) / float64(i.Period)
				down[j] = 100 * float64(i.Period-(j-lo)) / float64(i.Period)
				osc[j] = up[j] - down[j]
			}
			add(i.ID, "up", up)
			add(i.ID, "down", down)
			add(i.ID, "oscillator", osc)
		case "ichimoku":
			t, k, b := midpoint(c, i.Fast), midpoint(c, i.Slow), midpoint(c, i.Period)
			aShift, bShift := blank(len(c)), blank(len(c))
			for j := i.Displacement; j < len(c); j++ {
				past := j - i.Displacement
				aShift[j] = (t[past] + k[past]) / 2
				bShift[j] = b[past]
			}
			add(i.ID, "tenkan", t)
			add(i.ID, "kijun", k)
			add(i.ID, "span_a", aShift)
			add(i.ID, "span_b", bShift)
		case "macd":
			fast, slow := ema(close, i.Fast), ema(close, i.Slow)
			line := blank(len(c))
			for j := range line {
				line[j] = fast[j] - slow[j]
			}
			signal := ema(line, i.Signal)
			hist := blank(len(c))
			for j := range hist {
				hist[j] = line[j] - signal[j]
			}
			add(i.ID, "line", line)
			add(i.ID, "signal", signal)
			add(i.ID, "histogram", hist)
		case "bollinger":
			m := sma(close, i.Period)
			u, l := blank(len(c)), blank(len(c))
			for j := i.Period - 1; j < len(c); j++ {
				variance := 0.0
				for k := j - i.Period + 1; k <= j; k++ {
					variance += math.Pow(close[k]-m[j], 2)
				}
				sd := math.Sqrt(variance/float64(i.Period)) * i.Multiplier
				u[j] = m[j] + sd
				l[j] = m[j] - sd
			}
			add(i.ID, "middle", m)
			add(i.ID, "upper", u)
			add(i.ID, "lower", l)
		}
	}
	return out
}
func blank(n int) []float64 {
	v := make([]float64, n)
	for i := range v {
		v[i] = math.NaN()
	}
	return v
}
func ema(x []float64, n int) []float64 { return smooth(x, n, 2/float64(n+1)) }
func smooth(x []float64, n int, alpha float64) []float64 {
	out := blank(len(x))
	count := 0
	sum, prev := 0.0, math.NaN()
	for i, v := range x {
		if !Finite(v) {
			count = 0
			sum = 0
			prev = math.NaN()
			continue
		}
		if count < n {
			count++
			sum += v
			if count == n {
				prev = sum / float64(n)
				out[i] = prev
			}
			continue
		}
		prev = alpha*v + (1-alpha)*prev
		out[i] = prev
	}
	return out
}
func sma(x []float64, n int) []float64 {
	v := blank(len(x))
	sum := 0.0
	for i, xv := range x {
		sum += xv
		if i >= n {
			sum -= x[i-n]
		}
		if i >= n-1 {
			v[i] = sum / float64(n)
		}
	}
	return v
}
func midpoint(c []Candle, n int) []float64 {
	v := blank(len(c))
	for j := n - 1; j < len(c); j++ {
		lo, hi := c[j].Low, c[j].High
		for k := j - n + 1; k < j; k++ {
			lo = math.Min(lo, c[k].Low)
			hi = math.Max(hi, c[k].High)
		}
		v[j] = (lo + hi) / 2
	}
	return v
}
