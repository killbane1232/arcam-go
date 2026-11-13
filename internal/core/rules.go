package core

import (
	"fmt"
	"strings"
)

func operand(o *Operand, f map[string]float64) (float64, bool) {
	if o == nil {
		return 0, false
	}
	if o.Value != nil {
		return *o.Value, true
	}
	v, ok := f[o.Field]
	return v, ok && Finite(v)
}
func Evaluate(r *Rule, now, prev map[string]float64) Trace {
	return evaluate(r, now, prev, ruleContext{position: "none"})
}

type ruleContext struct {
	position string
	rules    Rules
	allowLLM bool
}

func llmOperand(o *Operand) bool { return o != nil && strings.HasPrefix(o.Field, "llm.") }
func usesLLM(r *Rule, rules Rules) bool {
	if r == nil {
		return false
	}
	if r.Signal != "" {
		return usesLLM(rules.Map()[r.Signal], Rules{})
	}
	if llmOperand(r.Left) || llmOperand(r.Right) {
		return true
	}
	if usesLLM(r.Not, rules) {
		return true
	}
	for i := range r.All {
		if usesLLM(&r.All[i], rules) {
			return true
		}
	}
	for i := range r.Any {
		if usesLLM(&r.Any[i], rules) {
			return true
		}
	}
	return false
}
func ruleLabel(r *Rule) string {
	switch {
	case r == nil:
		return "disabled"
	case r.Position != "":
		return "position == " + r.Position
	case r.Signal != "":
		return "signal " + r.Signal
	case r.All != nil:
		return "all"
	case r.Any != nil:
		return "any"
	case r.Not != nil:
		return "not"
	default:
		return fmt.Sprintf("%s %s %s", label(r.Left), r.Op, label(r.Right))
	}
}

func evaluate(r *Rule, now, prev map[string]float64, ctx ruleContext) Trace {
	if r == nil {
		return Trace{Rule: "disabled", Ready: true}
	}
	if r.Position != "" {
		return Trace{Rule: fmt.Sprintf("position == %s (actual: %s)", r.Position, ctx.position), Ready: true, Result: r.Position == ctx.position}
	}
	if r.Signal != "" {
		// Resolve references lazily: unused entry signals must not request a model.
		flat := ctx
		flat.position = "none"
		child := evaluate(ctx.rules.Map()[r.Signal], now, prev, flat)
		return Trace{Rule: "signal " + r.Signal + " (evaluated with position=none)", Ready: child.Ready, Result: child.Result, NeedsLLM: child.NeedsLLM, Children: []Trace{child}}
	}
	if r.All != nil || r.Any != nil {
		all := r.All != nil
		children := r.All
		if !all {
			children = r.Any
		}
		t := Trace{Rule: ruleLabel(r), Children: make([]Trace, len(children))}
		// Keep trace children in the saved strategy order; evaluate cheap branches
		// first without mutating the immutable strategy. No model calls happen here.
		order := make([]int, 0, len(children))
		for i := range children {
			t.Children[i] = Trace{Rule: ruleLabel(&children[i]), Skipped: true, Reason: "short circuit: result already determined"}
			if !usesLLM(&children[i], ctx.rules) {
				order = append(order, i)
			}
		}
		for i := range children {
			if usesLLM(&children[i], ctx.rules) {
				order = append(order, i)
			}
		}
		pending, unavailable := false, false
		for _, i := range order {
			c := evaluate(&children[i], now, prev, ctx)
			t.Children[i] = c
			if c.NeedsLLM {
				pending = true
				continue
			}
			if all && (!c.Ready || !c.Result) {
				t.Ready = c.Ready
				t.Reason = fmt.Sprintf("all stopped at condition %d", i+1)
				return t
			}
			if !all && c.Ready && c.Result {
				t.Ready, t.Result = true, true
				t.Reason = fmt.Sprintf("any matched at condition %d", i+1)
				return t
			}
			unavailable = unavailable || !c.Ready
		}
		t.NeedsLLM = pending
		t.Ready = !pending && !unavailable
		t.Result = all && t.Ready
		return t
	}
	if r.Not != nil {
		c := evaluate(r.Not, now, prev, ctx)
		return Trace{Rule: "not", Ready: c.Ready, Result: c.Ready && !c.Result, NeedsLLM: c.NeedsLLM, Children: []Trace{c}}
	}
	a, ok := operand(r.Left, now)
	b, ok2 := operand(r.Right, now)
	t := Trace{Rule: ruleLabel(r), Ready: ok && ok2}
	if ok {
		t.Left = &a
	}
	if ok2 {
		t.Right = &b
	}
	// Missing ordinary indicators cannot be repaired by an LLM response.
	if (!ok && !llmOperand(r.Left)) || (!ok2 && !llmOperand(r.Right)) {
		t.Reason = "indicator value unavailable"
		return t
	}
	crossing := r.Op == "crosses_above" || r.Op == "crosses_below"
	p, pok := operand(r.Left, prev)
	q, qok := operand(r.Right, prev)
	if crossing && ((!pok && !llmOperand(r.Left)) || (!qok && !llmOperand(r.Right))) {
		t.Ready = false
		t.Reason = "previous indicator value unavailable"
		return t
	}
	if !t.Ready {
		t.NeedsLLM = ctx.allowLLM
		t.Reason = "LLM value not evaluated"
		return t
	}
	switch r.Op {
	case "gt":
		t.Result = a > b
	case "gte":
		t.Result = a >= b
	case "lt":
		t.Result = a < b
	case "lte":
		t.Result = a <= b
	case "eq":
		t.Result = a == b
	case "crosses_above", "crosses_below":
		t.Ready = pok && qok
		if !t.Ready {
			t.Reason = "previous candle value unavailable; skipped LLM values are not carried forward"
		}
		if r.Op == "crosses_above" {
			t.Result = t.Ready && p <= q && a > b
		} else {
			t.Result = t.Ready && p >= q && a < b
		}
	}
	return t
}
func label(o *Operand) string {
	if o == nil {
		return "?"
	}
	if o.Field != "" {
		return o.Field
	}
	if o.Value == nil {
		return "?"
	}
	return fmt.Sprint(*o.Value)
}
func (d Decision) RequiresLLM() bool {
	for _, t := range d.Rules {
		if t.NeedsLLM {
			return true
		}
	}
	return false
}
func Decide(s Strategy, now, prev map[string]float64, side string) Decision {
	if side == "none" {
		side = ""
	}
	position := side
	if position == "" {
		position = "none"
	}
	d := Decision{Position: position, Action: "hold", Reason: "no matching rule", Features: now, Rules: map[string]Trace{}}
	if position != "none" && position != "long" && position != "short" {
		d.Reason = "invalid position state"
		return d
	}
	ctx := ruleContext{position: position, rules: s.Rules, allowLLM: s.LLM != nil}
	for _, key := range []string{"enter_long", "enter_short", "exit_long", "exit_short"} {
		r := s.Rules.Map()[key]
		active := (side == "" && strings.HasPrefix(key, "enter_")) || (side != "" && key == "exit_"+side)
		if !active && r != nil {
			d.Rules[key] = Trace{Rule: ruleLabel(r), Skipped: true, Reason: "rule inactive for position " + position}
		} else {
			d.Rules[key] = evaluate(r, now, prev, ctx)
		}
	}
	if d.RequiresLLM() {
		d.Reason = "LLM evaluation required"
		return d
	}
	if side != "" {
		key := "exit_" + side
		if d.Rules[key].Result {
			d.Action = key
			d.Reason = "exit rule matched"
		}
	} else {
		l, sh := d.Rules["enter_long"].Result, d.Rules["enter_short"].Result
		if l && sh {
			d.Reason = "conflicting long and short signals"
		} else if l {
			d.Action = "enter_long"
			d.Reason = "entry rule matched"
		} else if sh {
			d.Action = "enter_short"
			d.Reason = "entry rule matched"
		}
	}
	for _, t := range d.Rules {
		if !t.Skipped && !t.Ready && d.Action == "hold" {
			d.Reason = "indicator values unavailable"
		}
	}
	return d
}
