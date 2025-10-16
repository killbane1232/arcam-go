package telebot

//internal/service/telebot/fsm/aroon_state.go
import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleAroonSettingsMenu показывает меню настройки Aroon
func (tb *TeleBot) handleAroonSettingsMenu(callback *tgbotapi.CallbackQuery) {
	text := `📈 *Настройка Aroon*

Введите параметры в формате:
'Период,Имя'

*Пример:*
'14,aroon_basic'
'25,aroon_trend'

Aroon измеряет силу тренда от 0 до 100.`

	tb.userStates.SetState(callback.From.ID, StateSettingAroon)

	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)
}

// handleAroonSetting обработка ввода параметров Aroon
func (tb *TeleBot) handleAroonSetting(ctx context.Context, message *tgbotapi.Message) {
	parts := strings.Split(message.Text, ",")
	if len(parts) != 2 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Неверный формат. Используйте: Период,Имя")
		tb.bot.Send(msg)
		return
	}

	period, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || period <= 0 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Период должен быть положительным числом")
		tb.bot.Send(msg)
		return
	}

	name := strings.TrimSpace(parts[1])
	if name == "" {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Имя не может быть пустым")
		tb.bot.Send(msg)
		return
	}

	// Валидация периода
	if period < 5 || period > 100 {
		msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Период должен быть от 5 до 100")
		tb.bot.Send(msg)
		return
	}

	msg := tgbotapi.NewMessage(message.Chat.ID,
		fmt.Sprintf("✅ Aroon создан:\\nПериод: %d\\nИмя: %s", period, name))
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)

	tb.userStates.SetState(message.From.ID, StateMainMenu)
	tb.sendMainMenu(message.Chat.ID)
}
