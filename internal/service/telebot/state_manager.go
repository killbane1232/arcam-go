package telebot

//internal/service/telebot/fsm/state_manager.go
import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// StateManager менеджер состояний FSM
type StateManager struct {
	states map[int64]StateType
	data   map[int64]map[string]interface{}
}

// NewStateManager создает новый менеджер состояний
func NewStateManager() *StateManager {
	return &StateManager{
		states: make(map[int64]StateType),
		data:   make(map[int64]map[string]interface{}),
	}
}

// SetState устанавливает состояние пользователя
func (sm *StateManager) SetState(userID int64, state StateType) {
	sm.states[userID] = state
	if _, exists := sm.data[userID]; !exists {
		sm.data[userID] = make(map[string]interface{})
	}
}

// GetState возвращает состояние пользователя
func (sm *StateManager) GetState(userID int64) StateType {
	if state, exists := sm.states[userID]; exists {
		return state
	}
	return StateMainMenu
}

// SetData устанавливает данные состояния
func (sm *StateManager) SetData(userID int64, key string, value interface{}) {
	if _, exists := sm.data[userID]; !exists {
		sm.data[userID] = make(map[string]interface{})
	}
	sm.data[userID][key] = value
}

// GetData возвращает данные состояния
func (sm *StateManager) GetData(userID int64, key string) interface{} {
	if data, exists := sm.data[userID]; exists {
		return data[key]
	}
	return nil
}

// ClearState очищает состояние пользователя
func (sm *StateManager) ClearState(userID int64) {
	delete(sm.states, userID)
	delete(sm.data, userID)
}

// handleEMASettingsMenu показывает меню настройки EMA
func (tb *TeleBot) handleEMASettingsMenu(callback *tgbotapi.CallbackQuery) {
	text := `📊 *Настройка EMA*

Введите параметры в формате:
'Период,Имя'

*Пример:*
'20,ema_fast'
'50,ema_slow'

Имя будет использоваться в правилах сигналов.`

	tb.userStates.SetState(callback.From.ID, StateSettingEMA)

	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)
}

// handleEMASetting обработка ввода параметров EMA
func (tb *TeleBot) handleEMASetting(ctx context.Context, message *tgbotapi.Message) {
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

	// Создаем конфигурацию EMA
	//config := indicator.EMAConfig{
	//	Period: period,
	//	Name:   name,
	//}

	// Здесь будет регистрация индикатора в системе
	// ema, err := indicator.NewEMA(config)
	// if err != nil {
	//     msg := tgbotapi.NewMessage(message.Chat.ID, "❌ Ошибка создания EMA: "+err.Error())
	//     tb.bot.Send(msg)
	//     return
	// }

	msg := tgbotapi.NewMessage(message.Chat.ID,
		fmt.Sprintf("✅ EMA создан:\\nПериод: %d\\nИмя: %s", period, name))
	msg.ParseMode = "Markdown"
	tb.bot.Send(msg)

	tb.userStates.SetState(message.From.ID, StateMainMenu)
	tb.sendMainMenu(message.Chat.ID)
}
