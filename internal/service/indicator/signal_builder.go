package indicator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shopspring/decimal"
)

// SignalType тип торгового сигнала
type SignalType string

const (
	SignalTypeBuy  SignalType = "buy"
	SignalTypeSell SignalType = "sell"
	SignalTypeHold SignalType = "hold"
)

// SignalStrength сила сигнала
type SignalStrength int

const (
	SignalStrengthWeak SignalStrength = iota
	SignalStrengthMedium
	SignalStrengthStrong
)

// TradingSignal торговый сигнал
type TradingSignal struct {
	Type       SignalType
	Strength   SignalStrength
	Symbol     string
	Timestamp  int64
	Reason     string
	Indicators map[string]decimal.Decimal // Значения индикаторов в момент сигнала
}

// RuleCondition условие правила
type RuleCondition struct {
	Indicator string          `json:"indicator"`
	Field     string          `json:"field"`    // Поле индикатора (value, up, down, oscillator)
	Operator  string          `json:"operator"` // >, <, >=, <=, ==, !=
	Value     decimal.Decimal `json:"value"`
}

// SignalRule правило генерации сигнала
type SignalRule struct {
	Name        string          `json:"name"`
	Conditions  []RuleCondition `json:"conditions"`
	SignalType  SignalType      `json:"signal_type"`
	Strength    SignalStrength  `json:"strength"`
	Description string          `json:"description"`
}

// SignalBuilder построитель сигналов
type SignalBuilder struct {
	rules    []SignalRule
	registry *Registry
}

// NewSignalBuilder создает новый построитель сигналов
func NewSignalBuilder(registry *Registry) *SignalBuilder {
	return &SignalBuilder{
		rules:    make([]SignalRule, 0),
		registry: registry,
	}
}

// AddRule добавляет правило
func (sb *SignalBuilder) AddRule(rule SignalRule) error {
	// Валидация правила
	for _, condition := range rule.Conditions {
		if _, exists := sb.registry.Get(condition.Indicator); !exists {
			return fmt.Errorf("indicator %s not found in registry", condition.Indicator)
		}

		// Валидация оператора
		if !isValidOperator(condition.Operator) {
			return fmt.Errorf("invalid operator: %s", condition.Operator)
		}
	}

	sb.rules = append(sb.rules, rule)
	return nil
}

// LoadRulesFromJSON загружает правила из JSON
func (sb *SignalBuilder) LoadRulesFromJSON(jsonData []byte) error {
	var rules []SignalRule
	if err := json.Unmarshal(jsonData, &rules); err != nil {
		return fmt.Errorf("failed to unmarshal rules: %w", err)
	}

	for _, rule := range rules {
		if err := sb.AddRule(rule); err != nil {
			return fmt.Errorf("failed to add rule %s: %w", rule.Name, err)
		}
	}

	return nil
}

// Evaluate оценивает условия и возвращает сигналы
func (sb *SignalBuilder) Evaluate(ctx context.Context, symbol string, data []OHLCV) ([]TradingSignal, error) {
	// Вычисляем все индикаторы
	indicatorValues, err := sb.registry.CalculateAll(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate indicators: %w", err)
	}

	var signals []TradingSignal

	// Проверяем каждое правило
	for _, rule := range sb.rules {
		if sb.evaluateRule(rule, indicatorValues) {
			signal := TradingSignal{
				Type:       rule.SignalType,
				Strength:   rule.Strength,
				Symbol:     symbol,
				Timestamp:  data[len(data)-1].Timestamp.Unix(),
				Reason:     rule.Description,
				Indicators: extractIndicatorValues(indicatorValues),
			}
			signals = append(signals, signal)
		}
	}

	return signals, nil
}

// evaluateRule оценивает выполнение одного правила
func (sb *SignalBuilder) evaluateRule(rule SignalRule, indicatorValues map[string]*IndicatorValue) bool {
	for _, condition := range rule.Conditions {
		indicatorValue, exists := indicatorValues[condition.Indicator]
		if !exists {
			return false
		}

		var valueToCompare decimal.Decimal
		if condition.Field == "" || condition.Field == "value" {
			valueToCompare = indicatorValue.Value
		} else {
			var ok bool
			valueToCompare, ok = indicatorValue.Values[condition.Field]
			if !ok {
				return false
			}
		}

		if !compareValues(valueToCompare, condition.Operator, condition.Value) {
			return false
		}
	}
	return true
}

// compareValues сравнивает значения по оператору
func compareValues(a decimal.Decimal, operator string, b decimal.Decimal) bool {
	switch operator {
	case ">":
		return a.GreaterThan(b)
	case ">=":
		return a.GreaterThanOrEqual(b)
	case "<":
		return a.LessThan(b)
	case "<=":
		return a.LessThanOrEqual(b)
	case "==":
		return a.Equal(b)
	case "!=":
		return !a.Equal(b)
	default:
		return false
	}
}

// isValidOperator проверяет валидность оператора
func isValidOperator(operator string) bool {
	validOps := []string{">", ">=", "<", "<=", "==", "!="}
	for _, op := range validOps {
		if op == operator {
			return true
		}
	}
	return false
}

// extractIndicatorValues извлекает значения индикаторов
func extractIndicatorValues(indicatorValues map[string]*IndicatorValue) map[string]decimal.Decimal {
	result := make(map[string]decimal.Decimal)
	for name, value := range indicatorValues {
		result[name] = value.Value
		for field, fieldValue := range value.Values {
			result[name+"_"+field] = fieldValue
		}
	}
	return result
}

// GetRules возвращает текущие правила
func (sb *SignalBuilder) GetRules() []SignalRule {
	return sb.rules
}

// ClearRules очищает правила
func (sb *SignalBuilder) ClearRules() {
	sb.rules = make([]SignalRule, 0)
}
