package trading

import (
	"context"
	"fmt"
	"sync"
	"time"

	"arcam-go/internal/domain/entities"
	"arcam-go/internal/domain/value_objects"
	"arcam-go/internal/ports/exchange"
	"arcam-go/internal/service/indicator"
)

// EngineStatus статус торгового движка
type EngineStatus string

const (
	EngineStatusStopped EngineStatus = "stopped"
	EngineStatusRunning EngineStatus = "running"
	EngineStatusPaused  EngineStatus = "paused"
)

// TradingConfig конфигурация торговли
type TradingConfig struct {
	Symbol              string
	MaxCapitalUsage     value_objects.Percentage
	PositionSizePercent value_objects.Percentage
	StopLossPercent     value_objects.Percentage
	TakeProfitPercent   value_objects.Percentage
}

// Engine интерфейс торгового движка
type Engine interface {
	Start(ctx context.Context) error
	Stop() error
	Pause() error
	Resume() error
	Status() EngineStatus

	UpdateConfig(config TradingConfig) error
	GetConfig() TradingConfig

	// Сигналы для управления
	ProcessSignal(signal indicator.TradingSignal) error
	ProcessManualOrder(side entities.OrderSide, quantity value_objects.Quantity) error
}

// BitMEXEngine реализация для BitMEX
type BitMEXEngine struct {
	mu     sync.RWMutex
	status EngineStatus
	config TradingConfig

	exchangeClient exchange.ExchangeClient
	riskManager    RiskManager
	auditor        Auditor
	signalBuilder  *indicator.SignalBuilder

	// Каналы для управления
	signalCh   chan indicator.TradingSignal
	orderCh    chan entities.Order
	controlCh  chan controlMessage
	positionCh chan entities.Position

	// Состояние
	currentPosition *entities.Position
	activeOrders    map[string]*entities.Order
}

type controlMessage struct {
	action string
	data   interface{}
}

// NewBitMEXEngine создает новый движок для BitMEX
func NewBitMEXEngine(
	client exchange.ExchangeClient,
	riskManager RiskManager,
	auditor Auditor,
	signalBuilder *indicator.SignalBuilder,
	config TradingConfig,
) *BitMEXEngine {

	return &BitMEXEngine{
		status:         EngineStatusStopped,
		config:         config,
		exchangeClient: client,
		riskManager:    riskManager,
		auditor:        auditor,
		signalBuilder:  signalBuilder,
		signalCh:       make(chan indicator.TradingSignal, 100),
		orderCh:        make(chan entities.Order, 50),
		controlCh:      make(chan controlMessage, 10),
		positionCh:     make(chan entities.Position, 20),
		activeOrders:   make(map[string]*entities.Order),
	}
}

// Start запускает торговый движок
func (e *BitMEXEngine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.status != EngineStatusStopped {
		return nil
	}

	e.status = EngineStatusRunning

	// Запускаем обработчики
	go e.signalProcessor(ctx)
	go e.orderProcessor(ctx)
	go e.positionMonitor(ctx)
	go e.controlProcessor(ctx)

	// Аудит запуска
	e.auditor.LogEvent(ctx, "engine_started", "Trading engine started", nil)

	return nil
}

// Stop останавливает движок
func (e *BitMEXEngine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.status = EngineStatusStopped

	// Закрываем каналы
	close(e.signalCh)
	close(e.orderCh)
	close(e.controlCh)

	// Отменяем активные ордера
	e.cancelAllOrders(context.Background())

	return nil
}

// Pause приостанавливает торговлю
func (e *BitMEXEngine) Pause() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.status != EngineStatusRunning {
		return nil
	}

	e.status = EngineStatusPaused
	e.auditor.LogEvent(context.Background(), "engine_paused", "Trading engine paused", nil)

	return nil
}

// Resume возобновляет торговлю
func (e *BitMEXEngine) Resume() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.status != EngineStatusPaused {
		return nil
	}

	e.status = EngineStatusRunning
	e.auditor.LogEvent(context.Background(), "engine_resumed", "Trading engine resumed", nil)

	return nil
}

// Status возвращает статус движка
func (e *BitMEXEngine) Status() EngineStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status
}

// ProcessSignal обрабатывает торговый сигнал
func (e *BitMEXEngine) ProcessSignal(signal indicator.TradingSignal) error {
	if e.Status() != EngineStatusRunning {
		return nil
	}

	select {
	case e.signalCh <- signal:
		return nil
	default:
		return fmt.Errorf("signal channel is full")
	}
}

// ProcessManualOrder обрабатывает ручной ордер
func (e *BitMEXEngine) ProcessManualOrder(side entities.OrderSide, quantity value_objects.Quantity) error {
	if e.Status() != EngineStatusRunning {
		return fmt.Errorf("engine is not running")
	}

	// Получаем последнюю цену как ориентир
	price, err := e.exchangeClient.GetLastPrice(context.Background(), e.config.Symbol)
	if err != nil {
		return err
	}

	order, err := entities.NewOrder(e.config.Symbol, side, entities.OrderTypeMarket, price, quantity)
	if err != nil {
		return err
	}

	select {
	case e.orderCh <- *order:
		return nil
	default:
		return fmt.Errorf("order channel is full")
	}
}

// signalProcessor обработчик сигналов
func (e *BitMEXEngine) signalProcessor(ctx context.Context) {
	for {
		select {
		case signal, ok := <-e.signalCh:
			if !ok {
				return
			}
			e.handleTradingSignal(ctx, signal)

		case <-ctx.Done():
			return
		}
	}
}

// handleTradingSignal обрабатывает один торговый сигнал
func (e *BitMEXEngine) handleTradingSignal(ctx context.Context, signal indicator.TradingSignal) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Проверяем риск
	if err := e.riskManager.ValidateSignal(ctx, signal, e.currentPosition); err != nil {
		e.auditor.LogEvent(ctx, "signal_rejected_risk", err.Error(), signal)
		return
	}

	switch signal.Type {
	case indicator.SignalTypeBuy:
		e.executeBuyOrder(ctx)
	case indicator.SignalTypeSell:
		e.executeSellOrder(ctx)
	case indicator.SignalTypeHold:
		// Ничего не делаем, но логируем
		e.auditor.LogEvent(ctx, "signal_hold", "Hold signal received", signal)
	}
}

// executeBuyOrder исполняет ордер на покупку
func (e *BitMEXEngine) executeBuyOrder(ctx context.Context) {
	// Получаем последнюю цену как ориентир для покупки
	lastPrice, err := e.exchangeClient.GetLastPrice(ctx, e.config.Symbol)
	if err != nil {
		e.auditor.LogEvent(ctx, "buy_order_failed", "Failed to get market data", err)
		return
	}

	// Рассчитываем размер позиции через risk manager
	quantity, err := e.riskManager.CalculatePositionSize(ctx, lastPrice, e.config.PositionSizePercent)
	if err != nil {
		e.auditor.LogEvent(ctx, "buy_order_failed", "Failed to calculate position size", err)
		return
	}

	// Создаем рыночный ордер
	order, err := entities.NewOrder(
		e.config.Symbol,
		entities.OrderSideBuy,
		entities.OrderTypeMarket,
		lastPrice,
		quantity,
	)
	if err != nil {
		e.auditor.LogEvent(ctx, "buy_order_failed", "Failed to create order", err)
		return
	}

	// Отправляем на исполнение
	e.orderCh <- *order
	e.auditor.LogEvent(ctx, "buy_order_created", "Buy order created", order)
}

// executeSellOrder исполняет ордер на продажу
func (e *BitMEXEngine) executeSellOrder(ctx context.Context) {
	// Аналогично executeBuyOrder, но для продажи: получаем последнюю цену
	lastPrice, err := e.exchangeClient.GetLastPrice(ctx, e.config.Symbol)
	if err != nil {
		e.auditor.LogEvent(ctx, "sell_order_failed", "Failed to get market data", err)
		return
	}

	// Для продажи используем текущую позицию или рассчитываем размер
	var quantity value_objects.Quantity
	if e.currentPosition != nil && !e.currentPosition.IsFlat() {
		quantity = e.currentPosition.Size
	} else {
		// Частичная продажа или открытие шортов
		quantity, err = e.riskManager.CalculatePositionSize(ctx, lastPrice, e.config.PositionSizePercent)
		if err != nil {
			e.auditor.LogEvent(ctx, "sell_order_failed", "Failed to calculate position size", err)
			return
		}
	}

	order, err := entities.NewOrder(
		e.config.Symbol,
		entities.OrderSideSell,
		entities.OrderTypeMarket,
		lastPrice,
		quantity,
	)
	if err != nil {
		e.auditor.LogEvent(ctx, "sell_order_failed", "Failed to create order", err)
		return
	}

	e.orderCh <- *order
	e.auditor.LogEvent(ctx, "sell_order_created", "Sell order created", order)
}

// orderProcessor обработчик ордеров
func (e *BitMEXEngine) orderProcessor(ctx context.Context) {
	for {
		select {
		case order, ok := <-e.orderCh:
			if !ok {
				return
			}
			e.executeOrder(ctx, &order)

		case <-ctx.Done():
			return
		}
	}
}

// executeOrder исполняет ордер на бирже
func (e *BitMEXEngine) executeOrder(ctx context.Context, order *entities.Order) {
	// Отправляем на биржу напрямую по типу
	var exchangeOrder *entities.Order
	var err error
	switch order.Type {
	case entities.OrderTypeMarket:
		exchangeOrder, err = e.exchangeClient.PlaceMarketOrder(ctx, order.Symbol, order.Side, order.Quantity)
	case entities.OrderTypeLimit:
		exchangeOrder, err = e.exchangeClient.PlaceLimitOrder(ctx, order.Symbol, order.Side, order.Price, order.Quantity)
	default:
		err = fmt.Errorf("unsupported order type: %s", order.Type)
	}
	if err != nil {
		e.auditor.LogEvent(ctx, "order_execution_failed", "Failed to execute order", err)
		return
	}

	// Сохраняем в активные ордера
	e.mu.Lock()
	e.activeOrders[exchangeOrder.ID] = exchangeOrder
	e.mu.Unlock()

	e.auditor.LogEvent(ctx, "order_executed", "Order executed successfully", exchangeOrder)
}

// positionMonitor монитор позиций
func (e *BitMEXEngine) positionMonitor(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			e.updatePosition(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// updatePosition обновляет текущую позицию
func (e *BitMEXEngine) updatePosition(ctx context.Context) {
	position, err := e.exchangeClient.GetPosition(ctx, e.config.Symbol)
	if err != nil {
		e.auditor.LogEvent(ctx, "position_update_failed", "Failed to update position", err)
		return
	}

	e.mu.Lock()
	e.currentPosition = position
	e.mu.Unlock()

	// Логируем изменение позиции
	if position != nil && !position.IsFlat() {
		e.auditor.LogEvent(ctx, "position_updated", "Position updated", position)
	}
}

// controlProcessor обработчик управляющих сообщений
func (e *BitMEXEngine) controlProcessor(ctx context.Context) {
	for {
		select {
		case msg, ok := <-e.controlCh:
			if !ok {
				return
			}
			e.handleControlMessage(ctx, msg)
		case <-ctx.Done():
			return
		}
	}
}

// handleControlMessage обрабатывает управляющее сообщение
func (e *BitMEXEngine) handleControlMessage(ctx context.Context, msg controlMessage) {
	switch msg.action {
	case "update_config":
		if config, ok := msg.data.(TradingConfig); ok {
			e.mu.Lock()
			e.config = config
			e.mu.Unlock()
			e.auditor.LogEvent(ctx, "config_updated", "Trading config updated", config)
		}
	case "cancel_orders":
		e.cancelAllOrders(ctx)
	}
}

// cancelAllOrders отменяет все активные ордера
func (e *BitMEXEngine) cancelAllOrders(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for orderID := range e.activeOrders {
		if err := e.exchangeClient.CancelOrder(ctx, e.config.Symbol, orderID); err != nil {
			e.auditor.LogEvent(ctx, "order_cancel_failed", "Failed to cancel order", err)
		}
	}

	e.activeOrders = make(map[string]*entities.Order)
	e.auditor.LogEvent(ctx, "all_orders_cancelled", "All active orders cancelled", nil)
}

// UpdateConfig обновляет конфигурацию
func (e *BitMEXEngine) UpdateConfig(config TradingConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Валидация конфигурации
	if err := e.riskManager.ValidateConfig(config); err != nil {
		return err
	}

	e.config = config

	// Отправляем сообщение для асинхронной обработки
	select {
	case e.controlCh <- controlMessage{action: "update_config", data: config}:
		return nil
	default:
		return fmt.Errorf("control channel is full")
	}
}

// GetConfig возвращает текущую конфигурацию
func (e *BitMEXEngine) GetConfig() TradingConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config
}
