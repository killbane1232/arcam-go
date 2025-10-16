package telebot

//internal/service/telebot/validators.go
import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// ValidationResult результат валидации
type ValidationResult struct {
	IsValid bool
	Message string
	Value   interface{}
}

// validatePercentage валидация процентного значения
func validatePercentage(input string, min, max float64) ValidationResult {
	value, err := strconv.ParseFloat(strings.TrimSpace(input), 64)
	if err != nil {
		return ValidationResult{
			IsValid: false,
			Message: fmt.Sprintf("Введите число от %.1f до %.1f", min, max),
		}
	}

	if value < min || value > max {
		return ValidationResult{
			IsValid: false,
			Message: fmt.Sprintf("Значение должно быть от %.1f до %.1f", min, max),
		}
	}

	return ValidationResult{
		IsValid: true,
		Value:   decimal.NewFromFloat(value),
		Message: "",
	}
}

// validateInteger валидация целого числа
func validateInteger(input string, min, max int) ValidationResult {
	value, err := strconv.Atoi(strings.TrimSpace(input))
	if err != nil {
		return ValidationResult{
			IsValid: false,
			Message: fmt.Sprintf("Введите целое число от %d до %d", min, max),
		}
	}

	if value < min || value > max {
		return ValidationResult{
			IsValid: false,
			Message: fmt.Sprintf("Значение должно быть от %d до %d", min, max),
		}
	}

	return ValidationResult{
		IsValid: true,
		Value:   value,
		Message: "",
	}
}

// validateSymbol валидация торгового символа
func validateSymbol(input string) ValidationResult {
	symbol := strings.TrimSpace(strings.ToUpper(input))

	if symbol == "" {
		return ValidationResult{
			IsValid: false,
			Message: "Символ не может быть пустым",
		}
	}

	// Проверяем длину символа
	if utf8.RuneCountInString(symbol) > 20 {
		return ValidationResult{
			IsValid: false,
			Message: "Символ слишком длинный (макс. 20 символов)",
		}
	}

	// Проверяем допустимые символы
	for _, r := range symbol {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '/' && r != '_' && r != '-' {
			return ValidationResult{
				IsValid: false,
				Message: "Символ содержит недопустимые символы. Разрешены A-Z, 0-9, /, _, -",
			}
		}
	}

	return ValidationResult{
		IsValid: true,
		Value:   symbol,
		Message: "",
	}
}

// validateName валидация имени индикатора/правила
func validateName(input string) ValidationResult {
	name := strings.TrimSpace(input)

	if name == "" {
		return ValidationResult{
			IsValid: false,
			Message: "Имя не может быть пустым",
		}
	}

	if utf8.RuneCountInString(name) > 50 {
		return ValidationResult{
			IsValid: false,
			Message: "Имя слишком длинное (макс. 50 символов)",
		}
	}

	// Проверяем допустимые символы
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return ValidationResult{
				IsValid: false,
				Message: "Имя содержит недопустимые символы. Разрешены A-Z, a-z, 0-9, _, -",
			}
		}
	}

	return ValidationResult{
		IsValid: true,
		Value:   name,
		Message: "",
	}
}

// validateEMAInput валидация ввода для EMA
func validateEMAInput(periodStr, name string) ValidationResult {
	// Валидация периода
	periodResult := validateInteger(periodStr, 1, 200)
	if !periodResult.IsValid {
		return periodResult
	}

	// Валидация имени
	nameResult := validateName(name)
	if !nameResult.IsValid {
		return nameResult
	}

	return ValidationResult{
		IsValid: true,
		Value:   map[string]interface{}{"period": periodResult.Value, "name": nameResult.Value},
		Message: "",
	}
}

// validateAroonInput валидация ввода для Aroon
func validateAroonInput(periodStr, name string) ValidationResult {
	// Валидация периода
	periodResult := validateInteger(periodStr, 5, 100)
	if !periodResult.IsValid {
		return periodResult
	}

	// Валидация имени
	nameResult := validateName(name)
	if !nameResult.IsValid {
		return nameResult
	}

	return ValidationResult{
		IsValid: true,
		Value:   map[string]interface{}{"period": periodResult.Value, "name": nameResult.Value},
		Message: "",
	}
}

// validateRuleInput валидация ввода для правила
func validateRuleInput(ruleType, strengthStr, symbol string) ValidationResult {
	// Валидация силы
	strengthResult := validateInteger(strengthStr, 1, 100)
	if !strengthResult.IsValid {
		return strengthResult
	}

	// Валидация символа
	symbolResult := validateSymbol(symbol)
	if !symbolResult.IsValid {
		return symbolResult
	}

	// Валидация типа правила
	ruleType = strings.TrimSpace(strings.ToUpper(ruleType))
	validTypes := map[string]bool{
		"EMA_CROSS":      true,
		"AROON_UP":       true,
		"AROON_DOWN":     true,
		"PRICE_ABOVE":    true,
		"PRICE_BELOW":    true,
		"MACD_CROSS":     true,
		"RSI_OVERBOUGHT": true,
		"RSI_OVERSOLD":   true,
	}

	if !validTypes[ruleType] {
		return ValidationResult{
			IsValid: false,
			Message: "Неизвестный тип правила. Допустимые: EMA_CROSS, AROON_UP, AROON_DOWN, PRICE_ABOVE, PRICE_BELOW, MACD_CROSS, RSI_OVERBOUGHT, RSI_OVERSOLD",
		}
	}

	return ValidationResult{
		IsValid: true,
		Value: map[string]interface{}{
			"type":     ruleType,
			"strength": strengthResult.Value,
			"symbol":   symbolResult.Value,
		},
		Message: "",
	}
}

// validateRiskInput валидация ввода параметров риска
func validateRiskInput(maxCapitalStr, positionSizeStr, stopLossStr, takeProfitStr string) ValidationResult {
	// Валидация максимального использования капитала
	maxCapitalResult := validatePercentage(maxCapitalStr, 1, 100)
	if !maxCapitalResult.IsValid {
		return maxCapitalResult
	}

	// Валидация размера позиции
	positionSizeResult := validatePercentage(positionSizeStr, 1, 50)
	if !positionSizeResult.IsValid {
		return positionSizeResult
	}

	// Валидация Stop Loss
	stopLossResult := validatePercentage(stopLossStr, 0.1, 10)
	if !stopLossResult.IsValid {
		return stopLossResult
	}

	// Валидация Take Profit
	takeProfitResult := validatePercentage(takeProfitStr, 0.1, 20)
	if !takeProfitResult.IsValid {
		return takeProfitResult
	}

	return ValidationResult{
		IsValid: true,
		Value: map[string]interface{}{
			"max_capital":   maxCapitalResult.Value,
			"position_size": positionSizeResult.Value,
			"stop_loss":     stopLossResult.Value,
			"take_profit":   takeProfitResult.Value,
		},
		Message: "",
	}
}
