package bitmex

import (
	"sync"
	"time"
)

// TokenBucket простой thread-safe токен-бакет
type TokenBucket struct {
	capacity       int
	tokens         int
	refillInterval time.Duration
	refillAmount   int

	mu         sync.Mutex
	lastRefill time.Time
}

func NewTokenBucket(capacity int, refillAmount int, refillInterval time.Duration) *TokenBucket {
	if capacity <= 0 {
		capacity = 1
	}
	if refillAmount <= 0 {
		refillAmount = 1
	}
	if refillInterval <= 0 {
		refillInterval = time.Second
	}
	return &TokenBucket{
		capacity:       capacity,
		tokens:         capacity,
		refillInterval: refillInterval,
		refillAmount:   refillAmount,
		lastRefill:     time.Now(),
	}
}

// Take блокирует до получения одного токена.
func (tb *TokenBucket) Take() {
	for {
		tb.mu.Lock()
		tb.refill()
		if tb.tokens > 0 {
			tb.tokens--
			tb.mu.Unlock()
			return
		}
		// Недостаточно токенов — ждём до следующего рефила
		next := tb.lastRefill.Add(tb.refillInterval)
		wait := time.Until(next)
		tb.mu.Unlock()
		if wait <= 0 {
			wait = tb.refillInterval
		}
		time.Sleep(wait)
	}
}

func (tb *TokenBucket) refill() {
	now := time.Now()
	if now.Sub(tb.lastRefill) >= tb.refillInterval {
		intervals := int(now.Sub(tb.lastRefill) / tb.refillInterval)
		tb.tokens += intervals * tb.refillAmount
		if tb.tokens > tb.capacity {
			tb.tokens = tb.capacity
		}
		tb.lastRefill = tb.lastRefill.Add(time.Duration(intervals) * tb.refillInterval)
	}
}

