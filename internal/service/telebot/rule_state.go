package telebot

//internal/service/telebot/fsm/rule_state.go
import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleRulesSettingsMenu показывает меню управления правилами
func (tb *TeleBot) handleRulesSettingsMenu(callback *tgbotapi.CallbackQuery) {
	text := `📋 *Управление правилами сигналов*

*Типы правил:*
• EMA crossover (EMA1 > EMA2)
• Aroon strength (Aroon > уровень)
• Price action (цена > индикатор)

Введите правило в формате:
'ТИП ПАРАМЕТРЫ,СИЛА(1-100),СИМВОЛ'

*Примеры:*
'EMA_CROSS 20 50,70,XBTUSD'
'AROON_UP 14 70,60,XBTUSD'
'PRICE_ABOVE ema_20,80,XBTUSD'`

	tb.userStates.SetState(callback.From.ID, StateSettingRule)

	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)
}

// handleRuleSetting обработка ввода правил
func (tb *TeleBot) handleRuleSetting(ctx context.Context, message *tgbotapi.Message) {
	parts := strings.Split(message.Text, ",")
	if len(parts) != 3 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Неверный формат. Используйте: ТИП ПАРАМЕТРЫ,СИЛА,СИМВОЛ")
		tb.bot.Send(msg)
		return
	}

	ruleType := strings.TrimSpace(parts[0])
	strengthStr := strings.TrimSpace(parts[1])
	symbol := strings.TrimSpace(parts[2])

	// Валидация силы
	strength, err := strconv.Atoi(strengthStr)
	if err != nil || strength < 1 || strength > 100 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Сила должна быть числом от 1 до 100")
		tb.bot.Send(msg)
		return
	}

	// Валидация символа
	if symbol == "" {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Символ не может быть пустым")
		tb.bot.Send(msg)
		return
	}

	// Парсинг типа правила
	ruleParts := strings.Fields(ruleType)
	if len(ruleParts) < 1 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Неверный формат типа правила")
		tb.bot.Send(msg)
		return
	}

	//ruleName := fmt.Sprintf("rule_%d", message.Time().Unix())

	// Здесь будет создание правила в signal builder
	// rule, err := tb.createRuleFromInput(ruleParts, strength, symbol)
	// if err != nil {
	//     msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка создания правила: "+err.Error())
	//     tb.bot.Send(msg)
	//     return
	// }

	msg := tgbotapi.NewMessage(message.Chat.ID,
		fmt.Sprintf("✅ Правило создано:\\nТип: %s\\nСила: %d\\nСимвол: %s",
			ruleType, strength, symbol))
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)

	tb.userStates.SetState(message.From.ID, StateMainMenu)
	tb.sendMainMenu(message.Chat.ID)
}
