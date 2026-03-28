package entities

import "time"

// Candle представляет свечу с OHLCV данными
type Candle struct {
	Timestamp time.Time `json:"timestamp"`
	Open      float64   `json:"open"`
	High      float64   `json:"high"`
	Low       float64   `json:"low"`
	Close     float64   `json:"close"`
	Volume    float64   `json:"volume"`
}

// CandleSequence представляет последовательность свечей для обучения
type CandleSequence struct {
	Input  []Candle `json:"input"`  // Входные свечи
	Output []Candle `json:"output"` // Ожидаемые выходные свечи
}

// TrainingData представляет данные для обучения модели
type TrainingData struct {
	Sequences []CandleSequence `json:"sequences"`
	Symbol    string           `json:"symbol"`
	BinSize   string           `json:"binSize"`
}

