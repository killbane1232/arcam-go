package training

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"arcam-go/internal/domain/entities"
)

// CandleParser парсер свечей для подготовки данных обучения
type CandleParser struct{}

// NewCandleParser создает новый парсер свечей
func NewCandleParser() *CandleParser {
	return &CandleParser{}
}

// ParseCSV парсит CSV файл со свечами
func (p *CandleParser) ParseCSV(filePath string) ([]entities.Candle, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия файла %s: %w", filePath, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения CSV: %w", err)
	}

	if len(records) < 2 {
		return nil, fmt.Errorf("файл должен содержать заголовок и хотя бы одну запись")
	}

	// Пропускаем заголовок
	records = records[1:]

	candles := make([]entities.Candle, 0, len(records))
	for i, record := range records {
		if len(record) < 6 {
			return nil, fmt.Errorf("недостаточно колонок в строке %d", i+2)
		}

		candle, err := p.parseCandleRecord(record)
		if err != nil {
			return nil, fmt.Errorf("ошибка парсинга строки %d: %w", i+2, err)
		}

		candles = append(candles, candle)
	}

	return candles, nil
}

// parseCandleRecord парсит одну запись свечи
func (p *CandleParser) parseCandleRecord(record []string) (entities.Candle, error) {
	var candle entities.Candle
	var err error

	// Парсим timestamp
	candle.Timestamp, err = time.Parse(time.RFC3339Nano, record[0])
	if err != nil {
		return candle, fmt.Errorf("неверный формат timestamp: %w", err)
	}

	// Парсим OHLCV
	candle.Open, err = strconv.ParseFloat(record[1], 64)
	if err != nil {
		return candle, fmt.Errorf("неверный формат Open: %w", err)
	}

	candle.High, err = strconv.ParseFloat(record[2], 64)
	if err != nil {
		return candle, fmt.Errorf("неверный формат High: %w", err)
	}

	candle.Low, err = strconv.ParseFloat(record[3], 64)
	if err != nil {
		return candle, fmt.Errorf("неверный формат Low: %w", err)
	}

	candle.Close, err = strconv.ParseFloat(record[4], 64)
	if err != nil {
		return candle, fmt.Errorf("неверный формат Close: %w", err)
	}

	candle.Volume, err = strconv.ParseFloat(record[5], 64)
	if err != nil {
		return candle, fmt.Errorf("неверный формат Volume: %w", err)
	}

	return candle, nil
}

// PrepareTrainingSequences подготавливает последовательности для обучения
func (p *CandleParser) PrepareTrainingSequences(candles []entities.Candle, inputSize, outputSize int) ([]entities.CandleSequence, error) {
	if len(candles) < inputSize+outputSize {
		return nil, fmt.Errorf("недостаточно свечей для создания последовательностей: нужно минимум %d, получено %d", inputSize+outputSize, len(candles))
	}

	sequences := make([]entities.CandleSequence, 0)

	// Создаем скользящее окно для обучения
	for i := 0; i <= len(candles)-inputSize-outputSize; i++ {
		sequence := entities.CandleSequence{
			Input:  make([]entities.Candle, inputSize),
			Output: make([]entities.Candle, outputSize),
		}

		// Копируем входные свечи
		copy(sequence.Input, candles[i:i+inputSize])

		// Копируем выходные свечи (следующие после входных)
		copy(sequence.Output, candles[i+inputSize:i+inputSize+outputSize])

		sequences = append(sequences, sequence)
	}

	return sequences, nil
}

