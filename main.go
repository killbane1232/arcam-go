package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Использование: go run main.go <команда>")
		fmt.Println("Доступные команды:")
		fmt.Println("  apex-trader - запуск торгового бота")
		fmt.Println("  telebot     - запуск Telegram бота")
		fmt.Println("  train       - обучение модели")
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "apex-trader":
		runApexTrader()
	case "telebot":
		runTelebot()
	case "train":
		runTrain()
	default:
		fmt.Printf("Неизвестная команда: %s\n", command)
		os.Exit(1)
	}
}

func runApexTrader() {
	// Запуск apex-trader
	cmd := execCommand("go", "run", "cmd/apex-trader/main.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Ошибка запуска apex-trader: %v", err)
	}
}

func runTelebot() {
	// Запуск telebot
	cmd := execCommand("go", "run", "cmd/telebot/main.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Ошибка запуска telebot: %v", err)
	}
}

func runTrain() {
	// Запуск train
	cmd := execCommand("go", "run", "cmd/train/main.go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Ошибка запуска train: %v", err)
	}
}

func execCommand(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.Dir = filepath.Dir(os.Args[0])
	return cmd
}
