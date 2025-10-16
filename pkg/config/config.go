package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	Env      string `yaml:"env"`
	LogLevel string `yaml:"logLevel"`
}

type BitmexConfig struct {
	BaseURL   string `yaml:"baseURL"`
	WSURL     string `yaml:"wsURL"`
	TimeoutMs int    `yaml:"timeoutMs"`
}

type TradingConfig struct {
	Symbol   string `yaml:"symbol"`
	Leverage int    `yaml:"leverage"`
}

type TelebotConfig struct {
	Token  string `yaml:"token"`
	ChatID int64  `yaml:"chatID"`
}

type CandlesDump struct {
	Enabled bool   `yaml:"enabled"`
	Symbol  string `yaml:"symbol"`
	BinSize string `yaml:"binSize"` // 1m,5m,1h,1d и т.д.
	Count   int    `yaml:"count"`   // сколько свечей забирать
	Output  string `yaml:"output"`  // путь к файлу для записи
	Reverse bool   `yaml:"reverse"` // BitMEX reverse=true отдаёт от новых к старым
}

type Config struct {
	App     AppConfig     `yaml:"app"`
	Bitmex  BitmexConfig  `yaml:"bitmex"`
	Trading TradingConfig `yaml:"trading"`
	Telebot TelebotConfig `yaml:"telebot"`
	Candles CandlesDump   `yaml:"candlesDump"`
}

// Load читает YAML-файл конфигурации по указанному пути
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
