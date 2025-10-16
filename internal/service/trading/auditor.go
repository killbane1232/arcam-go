package trading

import (
	"context"
	"encoding/json"
	"time"
)

// AuditEvent событие аудита
type AuditEvent struct {
	ID        string
	Timestamp time.Time
	Type      string
	Message   string
	Data      interface{}
	UserID    string
}

// Auditor интерфейс аудита
type Auditor interface {
	LogEvent(ctx context.Context, eventType, message string, data interface{}) error
	GetEvents(ctx context.Context, filter AuditFilter) ([]AuditEvent, error)
}

// AuditFilter фильтр для событий аудита
type AuditFilter struct {
	Type      string
	UserID    string
	StartTime time.Time
	EndTime   time.Time
}

// BitMEXAuditor реализация аудита для BitMEX
type BitMEXAuditor struct {
	storage AuditStorage
}

// AuditStorage хранилище событий аудита
type AuditStorage interface {
	SaveEvent(ctx context.Context, event AuditEvent) error
	GetEvents(ctx context.Context, filter AuditFilter) ([]AuditEvent, error)
}

// NewBitMEXAuditor создает аудитор для BitMEX
func NewBitMEXAuditor(storage AuditStorage) *BitMEXAuditor {
	return &BitMEXAuditor{
		storage: storage,
	}
}

// LogEvent логирует событие
func (a *BitMEXAuditor) LogEvent(ctx context.Context, eventType, message string, data interface{}) error {
	event := AuditEvent{
		ID:        generateEventID(),
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Message:   message,
		Data:      data,
		UserID:    "system", // Для системных событий
	}

	return a.storage.SaveEvent(ctx, event)
}

// GetEvents возвращает события по фильтру
func (a *BitMEXAuditor) GetEvents(ctx context.Context, filter AuditFilter) ([]AuditEvent, error) {
	return a.storage.GetEvents(ctx, filter)
}

// generateEventID генерирует ID события
func generateEventID() string {
	return "event_" + time.Now().Format("20060102150405")
}

// LogEventWithUser логирует событие с указанием пользователя
func (a *BitMEXAuditor) LogEventWithUser(ctx context.Context, eventType, message string, data interface{}, userID string) error {
	event := AuditEvent{
		ID:        generateEventID(),
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Message:   message,
		Data:      data,
		UserID:    userID,
	}

	return a.storage.SaveEvent(ctx, event)
}

// SerializeData сериализует данные события
func (e *AuditEvent) SerializeData() (string, error) {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
