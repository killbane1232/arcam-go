package indicator

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
)

// AroonConfig конфигурация Aroon
type AroonConfig struct {
	Period int
	Name   string
}

// Aroon представляет Aroon индикатор
type Aroon struct {
	config AroonConfig
}

// NewAroon создает новый Aroon индикатор
func NewAroon(config AroonConfig) (*Aroon, error) {
	if config.Period <= 0 {
		return nil, fmt.Errorf("Aroon period must be positive, got %d", config.Period)
	}
	if config.Name == "" {
		config.Name = fmt.Sprintf("aroon_%d", config.Period)
	}

	return &Aroon{
		config: config,
	}, nil
}

// Calculate вычисляет Aroon
func (a *Aroon) Calculate(ctx context.Context, data []OHLCV) (*IndicatorValue, error) {
	if len(data) < a.config.Period {
		return nil, fmt.Errorf("insufficient data for Aroon period %d, have %d", a.config.Period, len(data))
	}

	// Берем последние period+1 свечей (нужны high/low за период)
	startIdx := len(data) - a.config.Period - 1
	if startIdx < 0 {
		startIdx = 0
	}
	periodData := data[startIdx:]

	aroonUp, aroonDown := a.calculateAroon(periodData)

	return &IndicatorValue{
		Timestamp: data[len(data)-1].Timestamp,
		Value:     aroonUp.Sub(aroonDown), // Aroon Oscillator как основное значение
		Values: map[string]decimal.Decimal{
			"up":         aroonUp,
			"down":       aroonDown,
			"oscillator": aroonUp.Sub(aroonDown),
		},
	}, nil
}

// calculateAroon вычисляет значения Aroon Up и Down
func (a *Aroon) calculateAroon(data []OHLCV) (decimal.Decimal, decimal.Decimal) {
	if len(data) <= a.config.Period {
		return decimal.Zero, decimal.Zero
	}

	// Находим индексы highest high и lowest low за период
	highestHighIdx := 0
	lowestLowIdx := 0
	highestHigh := data[0].High
	lowestLow := data[0].Low

	for i := 1; i < len(data); i++ {
		if data[i].High.GreaterThan(highestHigh) {
			highestHigh = data[i].High
			highestHighIdx = i
		}
		if data[i].Low.LessThan(lowestLow) {
			lowestLow = data[i].Low
			lowestLowIdx = i
		}
	}

	// Вычисляем Aroon Up и Down
	periodsSinceHigh := len(data) - 1 - highestHighIdx
	periodsSinceLow := len(data) - 1 - lowestLowIdx

	aroonUp := decimal.NewFromInt(100).Mul(
		decimal.NewFromInt(int64(a.config.Period - periodsSinceHigh)).Div(
			decimal.NewFromInt(int64(a.config.Period)),
		),
	)

	aroonDown := decimal.NewFromInt(100).Mul(
		decimal.NewFromInt(int64(a.config.Period - periodsSinceLow)).Div(
			decimal.NewFromInt(int64(a.config.Period)),
		),
	)

	return aroonUp, aroonDown
}

// Type возвращает тип индикатора
func (a *Aroon) Type() IndicatorType {
	return IndicatorTypeAroon
}

// Period возвращает период
func (a *Aroon) Period() int {
	return a.config.Period
}

// Name возвращает имя
func (a *Aroon) Name() string {
	return a.config.Name
}

// ValidateConfig валидирует конфигурацию
func (a *Aroon) ValidateConfig() error {
	if a.config.Period <= 0 {
		return fmt.Errorf("Aroon period must be positive, got %d", a.config.Period)
	}
	return nil
}
