package core

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var IDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var SymbolPattern = regexp.MustCompile(`^[A-Z0-9]{2,24}$`)
var fieldID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

func Finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func Interval(s string) (time.Duration, error) {
	values := map[string]time.Duration{"1m": time.Minute, "3m": 3 * time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute, "30m": 30 * time.Minute, "1h": time.Hour, "2h": 2 * time.Hour, "4h": 4 * time.Hour, "6h": 6 * time.Hour, "8h": 8 * time.Hour, "12h": 12 * time.Hour, "1d": 24 * time.Hour}
	d, ok := values[s]
	if !ok {
		return 0, fmt.Errorf("unsupported interval %q", s)
	}
	return d, nil
}
func ValidateCandles(c []Candle, interval string) error {
	d, err := Interval(interval)
	if err != nil {
		return err
	}
	if len(c) == 0 || len(c) > 250000 {
		return fmt.Errorf("expected 1..250000 candles")
	}
	for i, v := range c {
		if v.Time.IsZero() || v.Time.UnixMilli()%d.Milliseconds() != 0 {
			return fmt.Errorf("candle %d: time must align to interval in UTC", i)
		}
		for _, n := range []float64{v.Open, v.High, v.Low, v.Close, v.Volume} {
			if !Finite(n) {
				return fmt.Errorf("candle %d: non-finite value", i)
			}
		}
		if v.Low <= 0 || v.Volume < 0 || v.High < v.Low || v.Open < v.Low || v.Open > v.High || v.Close < v.Low || v.Close > v.High {
			return fmt.Errorf("candle %d: invalid OHLCV", i)
		}
		if i > 0 && !v.Time.After(c[i-1].Time) {
			return fmt.Errorf("candles must be strictly sorted without duplicates")
		}
	}
	return nil
}
func Outputs(i Indicator) []string {
	switch i.Type {
	case "ema", "sma", "rsi", "atr", "adr":
		return []string{"value"}
	case "ohlc":
		return []string{"open", "high", "low", "close", "range", "volume"}
	case "pivot":
		return []string{"high", "low", "high_time", "low_time", "high_confirmation_bar", "low_confirmation_bar"}
	case "aroon":
		return []string{"up", "down", "oscillator"}
	case "ichimoku":
		return []string{"tenkan", "kijun", "span_a", "span_b"}
	case "macd":
		return []string{"line", "signal", "histogram"}
	case "bollinger":
		return []string{"middle", "upper", "lower"}
	}
	return nil
}
func (s Strategy) Fields() map[string]bool {
	f := map[string]bool{"open": true, "high": true, "low": true, "close": true, "volume": true}
	for _, i := range s.Indicators {
		for _, o := range append(Outputs(i), "available_at", "bar_time") {
			f[i.ID+"."+o] = true
		}
	}
	for _, key := range []string{"open", "high", "low", "range", "time", "available_at"} {
		f["day."+key] = true
		f["previous_day."+key] = true
	}
	f["previous_day.close"] = true
	for _, setup := range s.Setups {
		for _, key := range []string{"active", "age", "reference_time", "expires_at"} {
			f["setup."+setup.ID+"."+key] = true
		}
		for key := range setup.Capture {
			f["setup."+setup.ID+"."+key] = true
		}
	}
	if s.LLM != nil {
		for _, k := range []string{"enter_long", "exit_long", "enter_short", "exit_short"} {
			f["llm."+k] = true
		}
	}
	return f
}
func (s Strategy) Warmup() int {
	n := 2
	for _, i := range s.Indicators {
		v := indicatorWarmup(i)
		if v > n {
			n = v
		}
	}
	if s.LLM != nil && s.LLM.Lookback > n {
		n = s.LLM.Lookback
	}
	return n
}
func ValidateStrategy(s Strategy) error {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 160 {
		return fmt.Errorf("name is required (max 160 characters)")
	}
	if len(s.Indicators) > 32 {
		return fmt.Errorf("maximum 32 indicators")
	}
	used := map[string]bool{"llm": true, "day": true, "previous_day": true, "setup": true}
	validPeriod := func(v int) bool { return v >= 2 && v <= 1000 }
	for _, i := range s.Indicators {
		if !fieldID.MatchString(i.ID) || used[i.ID] {
			return fmt.Errorf("invalid or duplicate indicator id %q", i.ID)
		}
		used[i.ID] = true
		if i.Timeframe != "" {
			if _, e := Interval(i.Timeframe); e != nil {
				return e
			}
		}
		if i.Type == "pivot" && (i.Left < 1 || i.Left > 1000 || i.Right < 1 || i.Right > 1000) {
			return fmt.Errorf("pivot left/right must be 1..1000")
		}
		if len(Outputs(i)) == 0 {
			return fmt.Errorf("unknown indicator %q", i.Type)
		}
		if i.Type != "macd" && i.Type != "ohlc" && i.Type != "pivot" && !validPeriod(i.Period) {
			return fmt.Errorf("%s: period must be 2..1000", i.ID)
		}
		if i.Type == "ichimoku" && (!validPeriod(i.Fast) || !validPeriod(i.Slow) || i.Fast >= i.Slow || i.Slow >= i.Period || i.Displacement < 1 || i.Displacement > 1000) {
			return fmt.Errorf("%s: ichimoku requires 2 <= fast < slow < period <= 1000 and displacement 1..1000", i.ID)
		}
		if i.Type == "macd" && (!validPeriod(i.Fast) || !validPeriod(i.Slow) || !validPeriod(i.Signal) || i.Fast >= i.Slow) {
			return fmt.Errorf("%s: invalid MACD periods", i.ID)
		}
		if i.Type == "bollinger" && (!Finite(i.Multiplier) || i.Multiplier <= 0 || i.Multiplier > 10) {
			return fmt.Errorf("%s: multiplier must be (0,10]", i.ID)
		}
	}
	if !Finite(s.Risk.PositionFraction) || s.Risk.PositionFraction <= 0 || s.Risk.PositionFraction > 1 {
		return fmt.Errorf("position_fraction must be (0,1]")
	}
	for _, v := range []float64{s.Risk.StopLoss, s.Risk.TakeProfit} {
		if !Finite(v) || v < 0 || v >= 1 {
			return fmt.Errorf("stop_loss/take_profit must be fractions in [0,1)")
		}
	}
	if s.LLM != nil {
		l := s.LLM
		if l.Protocol != "" && l.Protocol != "chat" && l.Protocol != "systemone" {
			return fmt.Errorf("LLM protocol must be chat or systemone")
		}
		if (l.Provider != "openai" && l.Provider != "ollama") || strings.TrimSpace(l.Model) == "" || l.Lookback < 1 || l.Lookback > 200 || len(l.Instructions) > 16000 {
			return fmt.Errorf("LLM requires provider openai/ollama, model, lookback 1..200, instructions <=16000 characters")
		}
	}
	if s.Rules.EnterLong == nil && s.Rules.EnterShort == nil {
		return fmt.Errorf("at least one entry rule is required")
	}
	if s.Rules.EnterLong != nil && s.Rules.ExitLong == nil {
		return fmt.Errorf("long entry requires an exit rule")
	}
	if s.Rules.EnterShort != nil && s.Rules.ExitShort == nil {
		return fmt.Errorf("short entry requires an exit rule")
	}
	if err := validateExtensions(s); err != nil {
		return err
	}
	count := 0
	for name, r := range s.Rules.Map() {
		if r != nil {
			if err := validateRule(*r, s.Fields(), 0, &count, strings.HasPrefix(name, "exit_"), s.Rules); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}
func (r Rules) Map() map[string]*Rule {
	return map[string]*Rule{"enter_long": r.EnterLong, "exit_long": r.ExitLong, "enter_short": r.EnterShort, "exit_short": r.ExitShort}
}
func validateRule(r Rule, fields map[string]bool, depth int, count *int, allowSignal bool, rules Rules) error {
	*count++
	if depth > 16 || *count > 256 {
		return fmt.Errorf("rule tree exceeds depth/node limit")
	}
	modes := 0
	if r.Setup != "" {
		modes++
	}
	if r.Position != "" {
		modes++
	}
	if r.Signal != "" {
		modes++
	}
	if r.All != nil {
		modes++
	}
	if r.Any != nil {
		modes++
	}
	if r.Not != nil {
		modes++
	}
	if r.Op != "" || r.Left != nil || r.Right != nil {
		modes++
	}
	if modes != 1 {
		return fmt.Errorf("rule must contain exactly one of all/any/not/comparison/position/signal/setup")
	}
	if r.Setup != "" {
		if !fields["setup."+r.Setup+".active"] {
			return fmt.Errorf("unknown setup %q", r.Setup)
		}
		return nil
	}
	if r.Position != "" {
		if r.Position != "none" && r.Position != "long" && r.Position != "short" {
			return fmt.Errorf("position must be none, long or short")
		}
		return nil
	}
	if r.Signal != "" {
		if !allowSignal {
			return fmt.Errorf("signal references are only allowed in exit rules")
		}
		if r.Signal != "enter_long" && r.Signal != "enter_short" {
			return fmt.Errorf("signal must be enter_long or enter_short")
		}
		if rules.Map()[r.Signal] == nil {
			return fmt.Errorf("referenced %s rule is disabled", r.Signal)
		}
		return nil
	}
	if r.All != nil || r.Any != nil {
		children := r.All
		if r.Any != nil {
			children = r.Any
		}
		if len(children) == 0 {
			return fmt.Errorf("empty group")
		}
		for _, c := range children {
			if err := validateRule(c, fields, depth+1, count, allowSignal, rules); err != nil {
				return err
			}
		}
		return nil
	}
	if r.Not != nil {
		return validateRule(*r.Not, fields, depth+1, count, allowSignal, rules)
	}
	switch r.Op {
	case "gt", "gte", "lt", "lte", "eq", "crosses_above", "crosses_below":
	default:
		return fmt.Errorf("unknown operator %q", r.Op)
	}
	operands := 0
	for _, o := range []*Operand{r.Left, r.Right} {
		if err := validateOperand(o, fields, 0, &operands); err != nil {
			return err
		}
	}
	return nil
}
func ValidateBacktest(r BacktestRequest) error {
	d, err := Interval(r.Interval)
	if err != nil {
		return err
	}
	if !IDPattern.MatchString(r.StrategyID) || r.Version < 1 {
		return fmt.Errorf("exact strategy_id and version >=1 required")
	}
	if !IDPattern.MatchString(r.Source) {
		return fmt.Errorf("invalid source")
	}
	if r.From.IsZero() || !r.To.After(r.From) || r.From.UnixMilli()%d.Milliseconds() != 0 || r.To.UnixMilli()%d.Milliseconds() != 0 {
		return fmt.Errorf("from/to must be increasing UTC interval boundaries; to is exclusive")
	}
	if r.To.After(time.Now().UTC()) {
		return fmt.Errorf("backtest end must be in the past")
	}
	if r.To.Sub(r.From)/d > 50000 {
		return fmt.Errorf("maximum 50000 bars per pair")
	}
	if len(r.Symbols) == 0 || len(r.Symbols) > 10 {
		return fmt.Errorf("1..10 symbols required")
	}
	seen := map[string]bool{}
	for _, s := range r.Symbols {
		if !SymbolPattern.MatchString(s) || seen[s] {
			return fmt.Errorf("invalid or duplicate symbol %q", s)
		}
		seen[s] = true
	}
	if !Finite(r.Capital) || r.Capital <= 0 || r.Capital > 1e12 {
		return fmt.Errorf("invalid capital")
	}
	for _, v := range []float64{r.FeeBPS, r.SlippageBPS} {
		if !Finite(v) || v < 0 || v > 1000 {
			return fmt.Errorf("costs must be 0..1000 bps")
		}
	}
	if r.MaxLLMCalls < 0 || r.MaxLLMCalls > 50000 {
		return fmt.Errorf("max_llm_calls must be 0..50000")
	}
	return nil
}
