package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"arcam-go/internal/service/training"
	"arcam-go/pkg/config"
)

func main() {
	// Обработка сигналов для graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fmt.Println("Команда train запущена...")

	// Загружаем конфигурацию
	cfg, err := config.Load("configs/config.yaml")
	if err != nil {
		log.Fatalf("Ошибка загрузки конфигурации: %v", err)
	}

	// Проверяем, включено ли обучение
	if !cfg.Training.Enabled {
		log.Fatal("Обучение отключено в конфигурации. Установите training.enabled: true")
	}

	// Проверяем наличие файла с данными для обучения
	if _, err := os.Stat(cfg.Training.TrainingDataPath); os.IsNotExist(err) {
		log.Fatalf("Файл с данными для обучения не найден: %s", cfg.Training.TrainingDataPath)
	}

	// Создаем сервис обучения
	trainer := training.NewTrainer(&cfg.Training)

	// Запускаем обучение
	if err := trainer.Train(); err != nil {
		log.Fatalf("Ошибка обучения модели: %v", err)
	}

	fmt.Println("Обучение модели завершено успешно!")

	// Ждем сигнал завершения
	<-ctx.Done()
	fmt.Println("Команда train завершена")
}

