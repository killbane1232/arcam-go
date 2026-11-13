package core

import (
	"context"
	"fmt"
)

// ResolveDecision first checks cheap conditions. At most one enrichment is
// requested per candle, then every active rule is evaluated again with its
// result. All four LLM scores share that one response/cache lookup.
func ResolveDecision(ctx context.Context, s Strategy, bars []Candle, now, prev map[string]float64, symbol, side string, enrich Enricher) (Decision, error) {
	d := Decide(s, now, prev, side)
	if s.LLM == nil {
		return d, nil
	}
	if !d.RequiresLLM() {
		d.LLMStatus = "skipped"
		return d, nil
	}
	if len(bars) < s.Warmup() {
		d.LLMStatus = "not_ready"
		d.Reason = "indicators warming up"
		return d, nil
	}
	if enrich == nil {
		return d, fmt.Errorf("LLM provider required")
	}
	audit, err := enrich(ctx, bars, now, symbol, side)
	if err != nil {
		return d, err
	}
	if audit == nil {
		return d, fmt.Errorf("LLM provider returned no verdict")
	}
	ApplyVerdict(now, audit.Verdict)
	d = Decide(s, now, prev, side)
	d.LLM = audit
	d.LLMStatus = "evaluated"
	if audit.Cached {
		d.LLMStatus = "cached"
	}
	return d, nil
}
