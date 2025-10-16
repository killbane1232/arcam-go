package telebot

//internal/service/telebot/handlers.go
import (
	"context"
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleStartCommand обработка команды /start
func (tb *TeleBot) handleStartCommand(_ context.Context, message *tgbotapi.Message) {
	text := `🤖 *Добро пожаловать в ApexTrader!*

*Основные команды:*
/start - начать работу
/status - статус системы
/metrics - настройка индикаторов
/rules - управление правилами
/pause - приостановить торговлю
/resume - возобновить торговлю
/help - помощь

Используйте кнопки ниже для быстрого доступа к функциям.`

	tb.userStates.SetState(message.From.ID, StateMainMenu)
	tb.bot.Send(tgbotapi.NewMessage(message.Chat.ID, text))
	tb.sendMainMenu(message.Chat.ID)
}

// handleStatusCommand обработка команды /status
func (tb *TeleBot) handleStatusCommand(_ context.Context, message *tgbotapi.Message) {
	status := tb.tradingEngine.Status()
	config := tb.tradingEngine.GetConfig()

	text := fmt.Sprintf(`📊 *Статус системы*

*Движок:* %s
*Символ:* %s
*Использование капитала:* %s
*Размер позиции:* %s

*Индикаторы:* %d правил
*Риск-менеджмент:* активен`,
		string(status),
		config.Symbol,
		config.MaxCapitalUsage.String(),
		config.PositionSizePercent.String(),
		len(tb.signalBuilder.GetRules()),
	)

	// Добавляем информацию о позициях если есть
	// (в реальной системе здесь был бы запрос к бирже)

	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)
}

// handleMetricsCommand обработка команды /metrics
func (tb *TeleBot) handleMetricsCommand(_ context.Context, message *tgbotapi.Message) {
	text := `📈 *Настройка индикаторов*

Выберите тип индикатора для настройки:`

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 EMA", "settings_ema"),
			tgbotapi.NewInlineKeyboardButtonData("📈 Aroon", "settings_aroon"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📋 Правила сигналов", "settings_rules"),
			tgbotapi.NewInlineKeyboardButtonData("🛡️ Риск-менеджмент", "settings_risk"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "main_menu"),
		),
	)

	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	tb.bot.Send(msg)
}

// handleRulesCommand обработка команды /rules
func (tb *TeleBot) handleRulesCommand(_ context.Context, message *tgbotapi.Message) {
	rules := tb.signalBuilder.GetRules()

	if len(rules) == 0 {
		text := "📋 *Правила сигналов*\\n\\nНет активных правил."
		msg := tgbotapi.NewMessage(message.Chat.ID, text)
		msg.ParseMode = "Markdown"
		tb.bot.Send(msg)
		return
	}

	var rulesText strings.Builder
	rulesText.WriteString("📋 *Активные правила:*\\n\\n")

	for i, rule := range rules {
		rulesText.WriteString(fmt.Sprintf("*%d. %s*\\n", i+1, rule.Name))
		rulesText.WriteString(fmt.Sprintf("Тип: %s\\n", string(rule.SignalType)))
		rulesText.WriteString(fmt.Sprintf("Сила: %d\\n", rule.Strength))
		rulesText.WriteString(fmt.Sprintf("Условия: %d\\n\\n", len(rule.Conditions)))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("➕ Добавить правило", "add_rule"),
			tgbotapi.NewInlineKeyboardButtonData("🗑️ Очистить все", "clear_rules"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Назад", "main_menu"),
		),
	)

	msg := tgbotapi.NewMessage(message.Chat.ID, rulesText.String())
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	tb.bot.Send(msg)
}

// handlePauseCommand обработка команды /pause
func (tb *TeleBot) handlePauseCommand(_ context.Context, message *tgbotapi.Message) {
	if err := tb.tradingEngine.Pause(); err != nil {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка при приостановке: "+err.Error())
		tb.bot.Send(msg)
		return
	}

	msg := tgbotapi.NewMessage(message.Chat.ID, "⏸️ Торговля приостановлена")
	tb.bot.Send(msg)
}

// handleResumeCommand обработка команды /resume
func (tb *TeleBot) handleResumeCommand(_ context.Context, message *tgbotapi.Message) {
	if err := tb.tradingEngine.Resume(); err != nil {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка при возобновлении: "+err.Error())
		tb.bot.Send(msg)
		return
	}

	msg := tgbotapi.NewMessage(message.Chat.ID, "▶️ Торговля возобновлена")
	tb.bot.Send(msg)
}

// handleHelpCommand обработка команды /help
func (tb *TeleBot) handleHelpCommand(_ context.Context, message *tgbotapi.Message) {
	text := `❓ *Помощь по ApexTrader*

*Настройка индикаторов:*
- EMA: Экспоненциальное скользящее среднее
- Aroon: Индикатор определения тренда
- Правила: Логические условия для сигналов

*Управление рисками:*
- Максимальное использование капитала: % от общего баланса
- Размер позиции: % от доступного капитала на сделку

*Пример настройки EMA:*` +
		"\n```" +
		"\nПериод: 20" +
		"\nИмя: ema_20" +
		"\n```" +
		"\nИмя: ema_20" +
		"\n```" +

		"\n*Пример правила:*" +
		"\n```" +
		"\nIF ema_20 > ema_50 AND aroon_up > 70" +
		"\nTHEN BUY (сила: средняя)" +
		"```"

	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)
}

// handleUnknownCommand обработка неизвестной команды
func (tb *TeleBot) handleUnknownCommand(_ context.Context, message *tgbotapi.Message) {
	msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Неизвестная команда. Используйте /help для списка команд.")
	tb.bot.Send(msg)
}

// handleCallback обработка callback запросов
func (tb *TeleBot) handleCallback(_ context.Context, callback *tgbotapi.CallbackQuery) {
	data := callback.Data

	switch {
	case data == "main_menu":
		tb.userStates.SetState(callback.From.ID, StateMainMenu)
		tb.sendMainMenu(callback.Message.Chat.ID)

	case data == "status":
		// Имитация обработки статуса
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, "📊 Получаю статус...")
		tb.bot.Send(msg)

	case data == "settings_ema":
		tb.handleEMASettingsMenu(callback)

	case data == "settings_aroon":
		tb.handleAroonSettingsMenu(callback)

	case data == "settings_rules":
		tb.handleRulesSettingsMenu(callback)

	case data == "settings_risk":
		tb.handleRiskSettingsMenu(callback)

	case data == "add_rule":
		// В текущей версии UI добавления правил не реализован
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, "ℹ️ Добавление правил пока не реализовано из интерфейса бота.")
		tb.bot.Send(msg)
	}

	// Ответ на callback
	callbackResp := tgbotapi.NewCallback(callback.ID, "")
	tb.bot.Send(callbackResp)
}
