package telebot

// internal/service/telebot/formatters.go
import (
	"arcam-go/internal/domain/entities"
	"arcam-go/internal/service/indicator"
	"arcam-go/internal/service/trading"
	"fmt"
	"strings"
	"time"
)

// formatStatus форматирует статус системы
func (tb *TeleBot) formatStatus() string {
	status := tb.tradingEngine.Status()
	config := tb.tradingEngine.GetConfig()

	var statusEmoji string
	switch status {
	case trading.EngineStatusRunning:
		statusEmoji = "🟢"
	case trading.EngineStatusPaused:
		statusEmoji = "🟡"
	case trading.EngineStatusStopped:
		statusEmoji = "🔴"
	default:
		statusEmoji = "⚪"
	}

	return fmt.Sprintf(`%s *Статус системы*

*Движок:* %s %s
*Символ:* %s
*Использование капитала:* %s
*Размер позиции:* %s
*Stop Loss:* %s
*Take Profit:* %s

*Активные правила:* %d
*Последнее обновление:* %s`,
		statusEmoji,
		string(status),
		statusEmoji,
		config.Symbol,
		config.MaxCapitalUsage.String(),
		config.PositionSizePercent.String(),
		config.StopLossPercent.String(),
		config.TakeProfitPercent.String(),
		len(tb.signalBuilder.GetRules()),
		time.Now().Format("15:04:05"),
	)
}

// formatIndicatorValue форматирует значение индикатора
func formatIndicatorValue(value indicator.IndicatorValue) string {
	return fmt.Sprintf("%s (%s)",
		value.Value.String(),
		value.Timestamp.Format("15:04:05"),
	)
}

// formatSignal форматирует торговый сигнал
func formatSignal(signal indicator.TradingSignal) string {
	var signalEmoji string
	switch signal.Type {
	case indicator.SignalTypeBuy:
		signalEmoji = "🟢"
	case indicator.SignalTypeSell:
		signalEmoji = "🔴"
	case indicator.SignalTypeHold:
		signalEmoji = "🟡"
	default:
		signalEmoji = "⚪"
	}

	var indicatorsStr strings.Builder
	for name, v := range signal.Indicators {
		indicatorsStr.WriteString(fmt.Sprintf("\n  • %s: %s", name, v.String()))
	}

	return fmt.Sprintf(`%s *Сигнал %s*

*Сила:* %d/100
*Цена:* %s
*Символ:* %s
*Индикаторы:*%s
*Время:* %s`,
		signalEmoji,
		string(signal.Type),
		signal.Strength,
		"-",
		signal.Symbol,
		indicatorsStr.String(),
		time.Unix(signal.Timestamp, 0).Format("2006-01-02 15:04:05"),
	)
}

// formatOrder форматирует информацию об ордере
func formatOrder(order *entities.Order) string {
	var sideEmoji string
	if order.Side == entities.OrderSideBuy {
		sideEmoji = "🟢"
	} else {
		sideEmoji = "🔴"
	}

	var statusEmoji string
	switch order.Status {
	case entities.OrderStatusNew:
		statusEmoji = "🟡"
	case entities.OrderStatusFilled:
		statusEmoji = "🟢"
	case entities.OrderStatusCancelled:
		statusEmoji = "🔴"
	case entities.OrderStatusPartiallyFilled:
		statusEmoji = "🟠"
	default:
		statusEmoji = "⚪"
	}

	return fmt.Sprintf(`%s%s *Ордер %s*

*Тип:* %s %s
*Цена:* %s
*Количество:* %s
*Статус:* %s %s
*Исполнено:* %s/%s
*Создан:* %s`,
		sideEmoji,
		statusEmoji,
		order.ID,
		string(order.Side),
		string(order.Type),
		order.Price.String(),
		order.Quantity.String(),
		string(order.Status),
		statusEmoji,
		order.FilledQuantity.String(),
		order.Quantity.String(),
		order.CreatedAt.Format("15:04:05"),
	)
}

// formatPosition форматирует информацию о позиции
func formatPosition(position *entities.Position) string {
	if position == nil || position.IsFlat() {
		return "📭 *Нет активных позиций*"
	}

	var sideEmoji string
	if position.IsLong() {
		sideEmoji = "🟢"
	} else if position.IsShort() {
		sideEmoji = "🔴"
	} else {
		sideEmoji = "⚪"
	}

	unrealizedPnL, _ := position.CalculateUnrealizedPnL()

	return fmt.Sprintf(`%s *Позиция %s*

*Размер:* %s
*Средняя цена:* %s
*Текущая цена:* %s
*P&L:* %s
*Плечо:* %dx
*Обновлено:* %s`,
		sideEmoji,
		position.Symbol,
		position.Size.String(),
		position.AvgEntryPrice.String(),
		position.CurrentPrice.String(),
		unrealizedPnL.String(),
		position.Leverage,
		position.UpdatedAt.Format("15:04:05"),
	)
}

// formatError форматирует сообщение об ошибке
func formatError(err error) string {
	return fmt.Sprintf("❌ *Ошибка:* %s", err.Error())
}

// formatSuccess форматирует сообщение об успехе
func formatSuccess(message string) string {
	return fmt.Sprintf("✅ %s", message)
}

// formatWarning форматирует предупреждение
func formatWarning(message string) string {
	return fmt.Sprintf("⚠️ %s", message)
}
