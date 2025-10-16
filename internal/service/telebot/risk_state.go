package telebot

// internal/service/telebot/fsm/risk_state.go
import (
	"arcam-go/internal/domain/value_objects"
	"arcam-go/internal/service/trading"
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/shopspring/decimal"
)

// handleRiskSettingsMenu показывает меню настройки рисков
func (tb *TeleBot) handleRiskSettingsMenu(callback *tgbotapi.CallbackQuery) {
	currentConfig := tb.tradingEngine.GetConfig()

	text := fmt.Sprintf(`🛡️ *Настройка риск-менеджмента*

*Текущие настройки:*
• Макс. использование капитала: %s
• Размер позиции: %s
• Stop Loss: %s
• Take Profit: %s

Введите новые настройки в формате:
'МАКС_КАПИТАЛ,РАЗМЕР_ПОЗИЦИИ,STOP_LOSS,TAKE_PROFIT'

*Пример:*
'30,10,2,4'
(30%% макс. капитала, 10%% на позицию, 2%% SL, 4%% TP)`,
		currentConfig.MaxCapitalUsage.String(),
		currentConfig.PositionSizePercent.String(),
		currentConfig.StopLossPercent.String(),
		currentConfig.TakeProfitPercent.String(),
	)

	tb.userStates.SetState(callback.From.ID, StateSettingRisk)

	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)
}

// handleRiskSetting обработка ввода параметров рисков
func (tb *TeleBot) handleRiskSetting(ctx context.Context, message *tgbotapi.Message) {
	parts := strings.Split(message.Text, ",")
	if len(parts) != 4 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Неверный формат. Используйте: МАКС_КАПИТАЛ,РАЗМЕР_ПОЗИЦИИ,STOP_LOSS,TAKE_PROFIT")
		tb.bot.Send(msg)
		return
	}

	// Парсинг и валидация параметров
	maxCapital, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil || maxCapital <= 0 || maxCapital > 100 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Макс. капитал должен быть числом от 1 до 100")
		tb.bot.Send(msg)
		return
	}

	positionSize, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil || positionSize <= 0 || positionSize > 50 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Размер позиции должен быть числом от 1 до 50")
		tb.bot.Send(msg)
		return
	}

	stopLoss, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err != nil || stopLoss <= 0 || stopLoss > 10 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Stop Loss должен быть числом от 0.1 до 10")
		tb.bot.Send(msg)
		return
	}

	takeProfit, err := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
	if err != nil || takeProfit <= 0 || takeProfit > 20 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Take Profit должен быть числом от 0.1 до 20")
		tb.bot.Send(msg)
		return
	}

	// Обновление конфигурации
	currentConfig := tb.tradingEngine.GetConfig()

	// Создание новых value objects
	maxCapitalPct, err := value_objects.NewPercentage(decimal.NewFromFloat(maxCapital))
	if err != nil {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка создания параметра макс. капитала")
		tb.bot.Send(msg)
		return
	}

	positionSizePct, err := value_objects.NewPercentage(decimal.NewFromFloat(positionSize))
	if err != nil {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка создания параметра размера позиции")
		tb.bot.Send(msg)
		return
	}

	stopLossPct, err := value_objects.NewPercentage(decimal.NewFromFloat(stopLoss))
	if err != nil {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка создания параметра Stop Loss")
		tb.bot.Send(msg)
		return
	}

	takeProfitPct, err := value_objects.NewPercentage(decimal.NewFromFloat(takeProfit))
	if err != nil {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка создания параметра Take Profit")
		tb.bot.Send(msg)
		return
	}

	newConfig := trading.TradingConfig{
		Symbol:              currentConfig.Symbol,
		MaxCapitalUsage:     maxCapitalPct,
		PositionSizePercent: positionSizePct,
		StopLossPercent:     stopLossPct,
		TakeProfitPercent:   takeProfitPct,
	}

	if err := tb.tradingEngine.UpdateConfig(newConfig); err != nil {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка обновления конфигурации: "+err.Error())
		tb.bot.Send(msg)
		return
	}

	msg := tgbotapi.NewMessage(message.Chat.ID,
		fmt.Sprintf("✅ Настройки рисков обновлены:\\n• Макс. капитал: %.1f%%\\n• Размер позиции: %.1f%%\\n• Stop Loss: %.1f%%\\n• Take Profit: %.1f%%",
			maxCapital, positionSize, stopLoss, takeProfit))
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)

	tb.userStates.SetState(message.From.ID, StateMainMenu)
	tb.sendMainMenu(message.Chat.ID)
}
