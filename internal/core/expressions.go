package core

import (
	"fmt"
	"math"
	"strings"
)

func historyKey(field string, offset int) string {
	if offset == 0 {
		return field
	}
	return fmt.Sprintf("%s[%d]", field, offset)
}

func operand(o *Operand, f map[string]float64) (float64, bool) {
	if o == nil {
		return 0, false
	}
	if o.Value != nil {
		return *o.Value, Finite(*o.Value)
	}
	if o.Field != "" {
		v, ok := f[historyKey(o.Field, o.Offset)]
		return v, ok && Finite(v)
	}
	if len(o.Args) == 0 {
		return 0, false
	}
	a, ok := operand(&o.Args[0], f)
	if !ok {
		return 0, false
	}
	if o.Op == "abs" {
		return math.Abs(a), len(o.Args) == 1
	}
	if len(o.Args) != 2 {
		return 0, false
	}
	b, ok := operand(&o.Args[1], f)
	if !ok {
		return 0, false
	}
	switch o.Op {
	case "add":
		a += b
	case "sub":
		a -= b
	case "mul":
		a *= b
	case "div":
		if b == 0 {
			return 0, false
		}
		a /= b
	case "min":
		a = math.Min(a, b)
	case "max":
		a = math.Max(a, b)
	default:
		return 0, false
	}
	return a, Finite(a)
}

func validateOperand(o *Operand, fields map[string]bool, depth int, count *int) error {
	*count++
	if o == nil || depth > 16 || *count > 1024 {
		return fmt.Errorf("invalid operand or expression depth/node limit exceeded")
	}
	modes := 0
	if o.Field != "" {
		modes++
	}
	if o.Value != nil {
		modes++
	}
	if o.Op != "" || o.Args != nil {
		modes++
	}
	if modes != 1 {
		return fmt.Errorf("operand requires exactly one field, value or op/args")
	}
	if o.Offset < 0 || o.Offset > 10000 || (o.Offset != 0 && o.Field == "") {
		return fmt.Errorf("offset requires a field and must be 0..10000")
	}
	if o.Field != "" && !fields[o.Field] {
		return fmt.Errorf("unknown feature %q", o.Field)
	}
	if o.Value != nil && !Finite(*o.Value) {
		return fmt.Errorf("non-finite constant")
	}
	if o.Op != "" || o.Args != nil {
		n := 2
		switch o.Op {
		case "abs":
			n = 1
		case "add", "sub", "mul", "div", "min", "max":
		default:
			return fmt.Errorf("unknown arithmetic operator %q", o.Op)
		}
		if len(o.Args) != n {
			return fmt.Errorf("%s requires %d arguments", o.Op, n)
		}
		for i := range o.Args {
			if err := validateOperand(&o.Args[i], fields, depth+1, count); err != nil {
				return err
			}
		}
	}
	return nil
}

func llmOperand(o *Operand) bool {
	if o == nil {
		return false
	}
	if strings.HasPrefix(o.Field, "llm.") {
		return true
	}
	for i := range o.Args {
		if llmOperand(&o.Args[i]) {
			return true
		}
	}
	return false
}

// A missing non-LLM leaf (or undefined arithmetic) must not trigger inference.
func missingOrdinary(o *Operand, f map[string]float64) bool {
	if o == nil {
		return true
	}
	if !llmOperand(o) {
		_, ok := operand(o, f)
		return !ok
	}
	if o.Field != "" {
		if o.Offset == 0 {
			return false
		}
		_, ok := operand(o, f)
		return !ok
	}
	for i := range o.Args {
		if missingOrdinary(&o.Args[i], f) {
			return true
		}
	}
	if o.Op == "div" && len(o.Args) == 2 {
		if divisor, ok := operand(&o.Args[1], f); ok && divisor == 0 {
			return true
		}
	}
	if !missingLLM(o, f) {
		_, ok := operand(o, f)
		return !ok
	}
	return false
}

func missingLLM(o *Operand, f map[string]float64) bool {
	if o == nil {
		return false
	}
	if strings.HasPrefix(o.Field, "llm.") {
		_, ok := operand(o, f)
		return !ok
	}
	for i := range o.Args {
		if missingLLM(&o.Args[i], f) {
			return true
		}
	}
	return false
}

func (s Strategy) operands(visit func(*Operand)) {
	var walkOperand func(*Operand)
	walkOperand = func(o *Operand) {
		if o == nil {
			return
		}
		visit(o)
		for i := range o.Args {
			walkOperand(&o.Args[i])
		}
	}
	var walkRule func(*Rule)
	walkRule = func(r *Rule) {
		if r == nil {
			return
		}
		walkOperand(r.Left)
		walkOperand(r.Right)
		walkRule(r.Not)
		for i := range r.All {
			walkRule(&r.All[i])
		}
		for i := range r.Any {
			walkRule(&r.Any[i])
		}
	}
	for _, r := range s.Rules.Map() {
		walkRule(r)
	}
	for _, p := range []*Protection{s.Risk.Long, s.Risk.Short} {
		if p != nil {
			walkOperand(p.StopPrice)
			walkOperand(p.TargetPrice)
		}
	}
	for _, setup := range s.Setups {
		walkRule(setup.When)
		walkRule(setup.Cancel)
		walkOperand(setup.Key)
		for _, o := range setup.Capture {
			walkOperand(&o)
		}
	}
}

// Offsets count execution bars, also for carried higher-timeframe fields.
func (s Strategy) historyOperands() []Operand {
	seen := map[string]bool{}
	out := []Operand{}
	s.operands(func(o *Operand) {
		if o.Field != "" && o.Offset > 0 {
			key := historyKey(o.Field, o.Offset)
			if !seen[key] {
				out = append(out, *o)
				seen[key] = true
			}
		}
	})
	return out
}
func addHistory(history []Operand, rows []map[string]float64, i int) {
	for _, o := range history {
		key := historyKey(o.Field, o.Offset)
		delete(rows[i], key)
		if i >= o.Offset {
			if v, ok := rows[i-o.Offset][o.Field]; ok && Finite(v) {
				rows[i][key] = v
			}
		}
	}
}

// ApplyPreviousLLMHistory retains only the requested score windows in session
// state. Missing/skipped scores leave holes; the latest score is never reused.
// The caller must pass nil after a gap in evaluated execution candles.
func (s Strategy) ApplyPreviousLLMHistory(now, prev map[string]float64) {
	s.ApplyPreviousRuntimeHistory(now, prev)
}

// ApplyPreviousRuntimeHistory shifts both LLM scores and captured setup fields.
func (s Strategy) ApplyPreviousRuntimeHistory(now, prev map[string]float64) {
	windows := map[string]int{}
	s.operands(func(o *Operand) {
		if strings.HasPrefix(o.Field, "llm.") || strings.HasPrefix(o.Field, "setup.") {
			windows[o.Field] = max(windows[o.Field], o.Offset)
		}
	})
	for field, n := range windows {
		for offset := 1; offset <= n; offset++ {
			key := historyKey(field, offset)
			delete(now, key)
			if v, ok := prev[historyKey(field, offset-1)]; ok && Finite(v) {
				now[key] = v
			}
		}
	}
}
