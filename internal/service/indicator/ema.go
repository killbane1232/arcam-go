package indicator

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
)

// EMAConfig конфигурация EMA
type EMAConfig struct {
	Period int
	Name   string
}

// EMA представляет Exponential Moving Average
type EMA struct {
	config EMAConfig
}

// NewEMA создает новый EMA индикатор
func NewEMA(config EMAConfig) (*EMA, error) {
	if config.Period <= 0 {
		return nil, fmt.Errorf("EMA period must be positive, got %d", config.Period)
	}
	if config.Name == "" {
		config.Name = fmt.Sprintf("ema_%d", config.Period)
	}

	return &EMA{
		config: config,
	}, nil
}

// Calculate вычисляет EMA
func (e *EMA) Calculate(ctx context.Context, data []OHLCV) (*IndicatorValue, error) {
	if len(data) < e.config.Period {
		return nil, fmt.Errorf("insufficient data for EMA period %d, have %d", e.config.Period, len(data))
	}

	// Берем закрытия для расчета
	closes := make([]decimal.Decimal, len(data))
	for i, d := range data {
		closes[i] = d.Close
	}

	ema := calculateEMA(closes, e.config.Period)

	return &IndicatorValue{
		Timestamp: data[len(data)-1].Timestamp,
		Value:     ema,
		Values:    map[string]decimal.Decimal{"value": ema},
	}, nil
}

// calculateEMA вычисляет EMA из массива цен
func calculateEMA(prices []decimal.Decimal, period int) decimal.Decimal {
	if len(prices) < period {
		return decimal.Zero
	}

	// Множитель для EMA
	multiplier := decimal.NewFromFloat(2.0).Div(decimal.NewFromInt(int64(period + 1)))

	// Начинаем с SMA
	sma := decimal.Zero
	for i := 0; i < period; i++ {
		sma = sma.Add(prices[i])
	}
	sma = sma.Div(decimal.NewFromInt(int64(period)))

	// Вычисляем EMA
	ema := sma
	for i := period; i < len(prices); i++ {
		ema = prices[i].Mul(multiplier).Add(ema.Mul(decimal.NewFromInt(1).Sub(multiplier)))
	}

	return ema
}

// Type возвращает тип индикатора
func (e *EMA) Type() IndicatorType {
	return IndicatorTypeEMA
}

// Period возвращает период
func (e *EMA) Period() int {
	return e.config.Period
}

// Name возвращает имя
func (e *EMA) Name() string {
	return e.config.Name
}

// ValidateConfig валидирует конфигурацию
func (e *EMA) ValidateConfig() error {
	if e.config.Period <= 0 {
		return fmt.Errorf("EMA period must be positive, got %d", e.config.Period)
	}
	return nil
}
