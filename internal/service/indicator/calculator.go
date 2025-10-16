package indicator

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// OHLCV представляет рыночные данные свечи
type OHLCV struct {
	Timestamp time.Time
	Open      decimal.Decimal
	High      decimal.Decimal
	Low       decimal.Decimal
	Close     decimal.Decimal
	Volume    decimal.Decimal
}

// IndicatorType тип индикатора
type IndicatorType string

const (
	IndicatorTypeEMA   IndicatorType = "ema"
	IndicatorTypeAroon IndicatorType = "aroon"
)

// IndicatorConfig конфигурация индикатора
type IndicatorConfig struct {
	Type   IndicatorType
	Period int
	Name   string // Уникальное имя индикатора (например "ema_20", "aroon_14")
}

// IndicatorValue значение индикатора
type IndicatorValue struct {
	Timestamp time.Time
	Value     decimal.Decimal
	Values    map[string]decimal.Decimal // Для индикаторов с несколькими значениями (Aroon Up/Down)
}

// Calculator интерфейс калькулятора индикаторов
type Calculator interface {
	// Calculate вычисляет значение индикатора для переданных данных
	Calculate(ctx context.Context, data []OHLCV) (*IndicatorValue, error)

	// Type возвращает тип индикатора
	Type() IndicatorType

	// Period возвращает период индикатора
	Period() int

	// Name возвращает имя индикатора
	Name() string

	// ValidateConfig валидирует конфигурацию
	ValidateConfig() error
}

// CalculatorFactory фабрика индикаторов
type CalculatorFactory interface {
	CreateCalculator(config IndicatorConfig) (Calculator, error)
}

// DefaultFactory реализует фабрику для встроенных индикаторов
type DefaultFactory struct{}

func (DefaultFactory) CreateCalculator(config IndicatorConfig) (Calculator, error) {
	switch config.Type {
	case IndicatorTypeEMA:
		return NewEMA(EMAConfig{Period: config.Period, Name: config.Name})
	case IndicatorTypeAroon:
		return NewAroon(AroonConfig{Period: config.Period, Name: config.Name})
	default:
		return nil, fmt.Errorf("unsupported indicator type: %s", config.Type)
	}
}

// Registry реестр индикаторов
type Registry struct {
	calculators map[string]Calculator
	factory     CalculatorFactory
}

// NewRegistry создает новый реестр индикаторов
func NewRegistry(factory CalculatorFactory) *Registry {
	return &Registry{
		calculators: make(map[string]Calculator),
		factory:     factory,
	}
}

// Register регистрирует индикатор
func (r *Registry) Register(config IndicatorConfig) error {
	calculator, err := r.factory.CreateCalculator(config)
	if err != nil {
		return err
	}

	if err := calculator.ValidateConfig(); err != nil {
		return err
	}

	r.calculators[config.Name] = calculator
	return nil
}

// Get возвращает калькулятор по имени
func (r *Registry) Get(name string) (Calculator, bool) {
	calculator, exists := r.calculators[name]
	return calculator, exists
}

// CalculateAll вычисляет все зарегистрированные индикаторы
func (r *Registry) CalculateAll(ctx context.Context, data []OHLCV) (map[string]*IndicatorValue, error) {
	results := make(map[string]*IndicatorValue)

	for name, calculator := range r.calculators {
		value, err := calculator.Calculate(ctx, data)
		if err != nil {
			return nil, err
		}
		results[name] = value
	}

	return results, nil
}
