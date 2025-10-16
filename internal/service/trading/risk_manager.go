package trading

import (
	"context"
	"fmt"

	"arcam-go/internal/domain/entities"
	"arcam-go/internal/domain/value_objects"
	"arcam-go/internal/ports/exchange"
	"arcam-go/internal/service/indicator"

	"github.com/shopspring/decimal"
)

// RiskManager управление рисками
type RiskManager interface {
	ValidateSignal(ctx context.Context, signal indicator.TradingSignal, position *entities.Position) error
	CalculatePositionSize(ctx context.Context, price value_objects.Price, sizePercent value_objects.Percentage) (value_objects.Quantity, error)
	ValidateConfig(config TradingConfig) error
	GetRiskMetrics(ctx context.Context) (*RiskMetrics, error)
}

// RiskMetrics метрики риска
type RiskMetrics struct {
	CurrentCapitalUsage value_objects.Percentage
	MaxCapitalUsage     value_objects.Percentage
	OpenPositionsCount  int
	DailyPnL            value_objects.Money
}

// BitMEXRiskManager реализация для BitMEX
type BitMEXRiskManager struct {
	exchangeClient  exchange.ExchangeClient
	maxCapitalUsage value_objects.Percentage
}

// NewBitMEXRiskManager создает менеджер рисков для BitMEX
func NewBitMEXRiskManager(client exchange.ExchangeClient, maxCapitalUsage value_objects.Percentage) *BitMEXRiskManager {
	return &BitMEXRiskManager{
		exchangeClient:  client,
		maxCapitalUsage: maxCapitalUsage,
	}
}

// ValidateSignal валидирует сигнал с точки зрения рисков
func (rm *BitMEXRiskManager) ValidateSignal(ctx context.Context, signal indicator.TradingSignal, position *entities.Position) error {
	// Проверяем использование капитала
	metrics, err := rm.GetRiskMetrics(ctx)
	if err != nil {
		return fmt.Errorf("failed to get risk metrics: %w", err)
	}

	// Если использование капитала превышает максимальное, блокируем новые позиции
	if metrics.CurrentCapitalUsage.Value().GreaterThan(rm.maxCapitalUsage.Value()) {
		return fmt.Errorf("capital usage exceeded: %s > %s",
			metrics.CurrentCapitalUsage.String(),
			rm.maxCapitalUsage.String())
	}

	// Дополнительные проверки рисков...
	return nil
}

// CalculatePositionSize рассчитывает размер позиции
func (rm *BitMEXRiskManager) CalculatePositionSize(
	ctx context.Context,
	price value_objects.Price,
	sizePercent value_objects.Percentage,
) (value_objects.Quantity, error) {

	// Получаем баланс
	// Упрощаем: считаем размер позиции от последней цены и заданного процента,
	// не запрашивая баланс/позиции из клиента порта (в текущем порте таких методов нет)
	// Здесь можно интегрировать реальные метрики позже.
	// Примем условный капитал 1.0 в валюте цены и возьмем долю sizePercent
	frac, err := sizePercent.DecimalFraction()
	if err != nil {
		return value_objects.Quantity{}, err
	}
	notional := frac // от условного капитала 1
	positionSize := notional.Div(price.Value())

	// Создаем Quantity (для BitMEX XBTUSD используем соответствующие лимиты)
	// XBTUSD имеет специфичные лимиты лотов
	// lotSize := value_objects.MustQuantity(decimal.NewFromInt(1)) // Базовый лот

	return value_objects.NewQuantity(positionSize)
}

// calculateAvailableCapital рассчитывает доступный капитал
func (rm *BitMEXRiskManager) calculateAvailableCapital(_ context.Context, totalBalance value_objects.Money) (value_objects.Money, error) {
	// Получаем текущие позиции
	// Без доступа к позициям возвращаем весь капитал как доступный (заглушка)
	positions := []*entities.Position{}

	// Рассчитываем занятый капитал
	usedCapital := decimal.Zero
	for _, position := range positions {
		if !position.IsFlat() {
			// Для BitMEX XBTUSD позиции рассчитываются в XBT
			positionValue := position.Size.Value().Mul(position.AvgEntryPrice.Value())
			usedCapital = usedCapital.Add(positionValue.Abs())
		}
	}

	totalCapital := totalBalance.Amount()
	available := totalCapital.Mul(rm.maxCapitalUsage.Value().Div(decimal.NewFromInt(100))).Sub(usedCapital)

	if available.IsNegative() {
		available = decimal.Zero
	}

	return value_objects.NewMoney(available, "XBT")
}

// ValidateConfig валидирует конфигурацию торговли
func (rm *BitMEXRiskManager) ValidateConfig(config TradingConfig) error {
	if config.MaxCapitalUsage.Value().GreaterThan(decimal.NewFromInt(100)) {
		return fmt.Errorf("max capital usage cannot exceed 100%%")
	}

	if config.MaxCapitalUsage.Value().LessThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("max capital usage must be at least 1%%")
	}

	if config.PositionSizePercent.Value().GreaterThan(decimal.NewFromInt(50)) {
		return fmt.Errorf("position size cannot exceed 50%% per trade")
	}

	return nil
}

// GetRiskMetrics возвращает текущие метрики риска
func (rm *BitMEXRiskManager) GetRiskMetrics(ctx context.Context) (*RiskMetrics, error) {
	// Заглушка без доступа к балансу/позициям из порта: возвращаем нули
	xbtBalance := value_objects.MustMoney(decimal.Zero, "USD")
	positions := []*entities.Position{}

	// Расчет использования капитала
	totalCapital := xbtBalance.Amount()
	usedCapital := decimal.Zero
	openPositions := 0

	for _, position := range positions {
		if !position.IsFlat() {
			positionValue := position.Size.Value().Mul(position.AvgEntryPrice.Value())
			usedCapital = usedCapital.Add(positionValue.Abs())
			openPositions++
		}
	}

	// Расчет использования в процентах
	usagePercent := decimal.Zero
	if totalCapital.GreaterThan(decimal.Zero) {
		usagePercent = usedCapital.Div(totalCapital).Mul(decimal.NewFromInt(100))
	}

	currentUsage, err := value_objects.NewPercentage(usagePercent)
	if err != nil {
		return nil, err
	}

	// Расчет PnL за день (упрощенно)
	dailyPnL := calculateDailyPnL(positions)

	return &RiskMetrics{
		CurrentCapitalUsage: currentUsage,
		MaxCapitalUsage:     rm.maxCapitalUsage,
		OpenPositionsCount:  openPositions,
		DailyPnL:            dailyPnL,
	}, nil
}

// calculateDailyPnL рассчитывает дневной PnL (упрощенная реализация)
func calculateDailyPnL(positions []*entities.Position) value_objects.Money {
	// В реальной системе здесь был бы расчет от начала дня
	totalPnL := decimal.Zero
	for _, position := range positions {
		// Упрощенный расчет unrealized PnL
		if !position.IsFlat() {
			// Здесь должен быть расчет от текущей цены
			// Пока возвращаем 0
		}
	}

	return value_objects.MustMoney(totalPnL, "USD")
}
