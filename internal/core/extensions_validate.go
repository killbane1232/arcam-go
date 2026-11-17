package core

import (
	"fmt"
	"strings"
)

func validateExtensions(s Strategy) error {
	r := s.Risk
	if !Finite(r.RiskFraction) || r.RiskFraction < 0 || r.RiskFraction > 1 {
		return fmt.Errorf("risk_fraction must be [0,1]")
	}
	if !Finite(r.MaxNotional) || r.MaxNotional < 0 {
		return fmt.Errorf("max_notional must be nonnegative")
	}
	if !Finite(r.MaxStopFraction) || r.MaxStopFraction < 0 || r.MaxStopFraction >= 1 {
		return fmt.Errorf("max_stop_fraction must be [0,1)")
	}
	if r.MaxEntriesPerDay < 0 || r.MaxEntriesPerDay > 10000 {
		return fmt.Errorf("max_entries_per_day must be 0..10000")
	}
	count := 0
	for _, side := range []string{"long", "short"} {
		p := r.Long
		entry := s.Rules.EnterLong
		if side == "short" {
			p = r.Short
			entry = s.Rules.EnterShort
		}
		if entry != nil && r.RiskFraction > 0 && p == nil && r.StopLoss == 0 {
			return fmt.Errorf("%s: risk_fraction requires a stop", side)
		}
		if p == nil {
			continue
		}
		if entry == nil {
			return fmt.Errorf("%s protection requires an entry rule", side)
		}
		if r.StopLoss != 0 || p.TargetPrice != nil && r.TakeProfit != 0 {
			return fmt.Errorf("absolute protection conflicts with fractional stop/target")
		}
		if err := validateOperand(p.StopPrice, s.Fields(), 0, &count); err != nil {
			return err
		}
		if p.TargetPrice != nil {
			if err := validateOperand(p.TargetPrice, s.Fields(), 0, &count); err != nil {
				return err
			}
		}
		if llmOperand(p.StopPrice) || llmOperand(p.TargetPrice) {
			return fmt.Errorf("protection cannot depend on LLM scores")
		}
	}
	if len(s.Setups) > 16 {
		return fmt.Errorf("maximum 16 setups")
	}
	ordinary := s.Fields()
	for key := range ordinary {
		if strings.HasPrefix(key, "setup.") || strings.HasPrefix(key, "llm.") {
			delete(ordinary, key)
		}
	}
	used := map[string]bool{}
	for _, setup := range s.Setups {
		if !fieldID.MatchString(setup.ID) || used[setup.ID] {
			return fmt.Errorf("invalid or duplicate setup id %q", setup.ID)
		}
		used[setup.ID] = true
		if setup.ExpiresAfter < 1 || setup.ExpiresAfter > 10000 {
			return fmt.Errorf("setup expires_after must be 1..10000 bars")
		}
		if setup.When == nil {
			return fmt.Errorf("setup when is required")
		}
		if len(setup.Capture) == 0 || len(setup.Capture) > 16 {
			return fmt.Errorf("setup requires 1..16 captures")
		}
		if err := validateRule(*setup.When, ordinary, 0, &count, false, Rules{}); err != nil {
			return fmt.Errorf("setup %s: %w", setup.ID, err)
		}
		if setup.Key != nil {
			if err := validateOperand(setup.Key, ordinary, 0, &count); err != nil {
				return err
			}
		}
		cancelFields := map[string]bool{}
		for k, v := range ordinary {
			cancelFields[k] = v
		}
		for key, o := range setup.Capture {
			if !fieldID.MatchString(key) || key == "active" || key == "age" || key == "reference_time" || key == "expires_at" {
				return fmt.Errorf("invalid capture name %q", key)
			}
			if err := validateOperand(&o, ordinary, 0, &count); err != nil {
				return err
			}
			cancelFields["setup."+setup.ID+"."+key] = true
		}
		for _, k := range []string{"active", "age", "reference_time", "expires_at"} {
			cancelFields["setup."+setup.ID+"."+k] = true
		}
		if setup.Cancel != nil {
			if err := validateRule(*setup.Cancel, cancelFields, 0, &count, false, Rules{}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Sessions execute at observed quotes, not historical next opens. Do not
// silently apply historical execution plans to the real-order adapter.
func (s Strategy) RequiresPlannedExecution() bool {
	return len(s.Setups) > 0 || s.Risk.Long != nil || s.Risk.Short != nil || s.Risk.RiskFraction > 0 || s.Risk.MaxNotional > 0 || s.Risk.MaxStopFraction > 0 || s.Risk.MaxEntriesPerDay > 0
}
