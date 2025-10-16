package telebot

import (
	"context"
	"fmt"
	"log"
	"sync"

	"arcam-go/internal/service/indicator"
	"arcam-go/internal/service/trading"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// BotConfig конфигурация бота
type BotConfig struct {
	Token        string
	AdminUserIDs []int64 // Белый список пользователей
	WebhookURL   string
	WebhookPort  string
	ChatID       int64
}

// TeleBot телеграм бот
type TeleBot struct {
	bot           *tgbotapi.BotAPI
	config        BotConfig
	tradingEngine trading.Engine
	signalBuilder *indicator.SignalBuilder
	userStates    *UserStateManager
	mu            sync.RWMutex
}

// UserStateManager менеджер состояний пользователей
type UserStateManager struct {
	states map[int64]*UserState
	mu     sync.RWMutex
}

// UserState состояние пользователя
type UserState struct {
	CurrentState StateType
	Data         map[string]interface{}
	LastActive   int64
}

// StateType тип состояния
type StateType string

const (
	StateMainMenu     StateType = "main_menu"
	StateSettingEMA   StateType = "setting_ema"
	StateSettingAroon StateType = "setting_aroon"
	StateSettingRule  StateType = "setting_rule"
	StateSettingRisk  StateType = "setting_risk"
)

// NewTeleBot создает нового бота
func NewTeleBot(config BotConfig, tradingEngine trading.Engine, signalBuilder *indicator.SignalBuilder) (*TeleBot, error) {
	bot, err := tgbotapi.NewBotAPI(config.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	bot.Debug = false

	return &TeleBot{
		bot:           bot,
		config:        config,
		tradingEngine: tradingEngine,
		signalBuilder: signalBuilder,
		userStates: &UserStateManager{
			states: make(map[int64]*UserState),
		},
	}, nil
}

// Start запускает бота
func (tb *TeleBot) Start(ctx context.Context) error {
	log.Printf("Authorized on account %s", tb.bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := tb.bot.GetUpdatesChan(u)

	for update := range updates {
		select {
		case <-ctx.Done():
			return nil
		default:
			tb.handleUpdate(ctx, update)
		}
	}

	return nil
}

// SendText отправляет произвольный текст в чат по ChatID из конфигурации
func (tb *TeleBot) SendText(text string) error {
	if tb.config.ChatID == 0 {
		return fmt.Errorf("chatID is not configured")
	}
	msg := tgbotapi.NewMessage(tb.config.ChatID, text)
	msg.ParseMode = "Markdown"
	_, err := tb.bot.Send(msg)
	return err
}

// handleUpdate обрабатывает обновление
func (tb *TeleBot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	// Проверяем авторизацию
	if !tb.isUserAuthorized(update) {
		tb.sendUnauthorizedMessage(update)
		return
	}

	var userID int64
	if update.Message != nil {
		userID = update.Message.From.ID
	} else if update.CallbackQuery != nil {
		userID = update.CallbackQuery.From.ID
	} else {
		return
	}

	// Обновляем состояние активности
	tb.userStates.UpdateLastActive(userID)

	// Обрабатываем команды
	switch {
	case update.Message != nil && update.Message.IsCommand():
		tb.handleCommand(ctx, update.Message)
	case update.Message != nil:
		tb.handleMessage(ctx, update.Message)
	case update.CallbackQuery != nil:
		tb.handleCallback(ctx, update.CallbackQuery)
	}
}

// isUserAuthorized проверяет авторизацию пользователя
func (tb *TeleBot) isUserAuthorized(update tgbotapi.Update) bool {
	var userID int64

	if update.Message != nil {
		userID = update.Message.From.ID
	} else if update.CallbackQuery != nil {
		userID = update.CallbackQuery.From.ID
	} else {
		return false
	}

	// Проверяем белый список
	for _, id := range tb.config.AdminUserIDs {
		if id == userID {
			return true
		}
	}

	return false
}

// sendUnauthorizedMessage отправляет сообщение о неавторизации
func (tb *TeleBot) sendUnauthorizedMessage(update tgbotapi.Update) {
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, "🚫 У вас нет доступа к этому боту.")
	tb.bot.Send(msg)
}

// handleCommand обрабатывает команды
func (tb *TeleBot) handleCommand(ctx context.Context, message *tgbotapi.Message) {
	command := message.Command()

	switch command {
	case "start":
		tb.handleStartCommand(ctx, message)
	case "status":
		tb.handleStatusCommand(ctx, message)
	case "help":
		tb.handleHelpCommand(ctx, message)
	case "pause":
		tb.handlePauseCommand(ctx, message)
	case "resume":
		tb.handleResumeCommand(ctx, message)
	case "metrics":
		tb.handleMetricsCommand(ctx, message)
	case "rules":
		tb.handleRulesCommand(ctx, message)
	default:
		tb.handleUnknownCommand(ctx, message)
	}
}

// handleMessage обрабатывает текстовые сообщения
func (tb *TeleBot) handleMessage(ctx context.Context, message *tgbotapi.Message) {
	state := tb.userStates.GetState(message.From.ID)

	switch state.CurrentState {
	case StateSettingEMA:
		tb.handleEMASetting(ctx, message)
	case StateSettingAroon:
		tb.handleAroonSetting(ctx, message)
	case StateSettingRule:
		tb.handleRuleSetting(ctx, message)
	case StateSettingRisk:
		tb.handleRiskSetting(ctx, message)
	default:
		tb.sendMainMenu(message.Chat.ID)
	}
}

// sendMainMenu отправляет главное меню
func (tb *TeleBot) sendMainMenu(chatID int64) {
	text := "🤖 *ApexTrader Bot*\\n\\nВыберите действие:"

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Статус", "status"),
			tgbotapi.NewInlineKeyboardButtonData("⚙️ Настройки", "settings"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📈 Индикаторы", "indicators"),
			tgbotapi.NewInlineKeyboardButtonData("📋 Правила", "rules"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Управление", "control"),
			tgbotapi.NewInlineKeyboardButtonData("❓ Помощь", "help"),
		),
	)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	tb.bot.Send(msg)
}
