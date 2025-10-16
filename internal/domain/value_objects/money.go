package value_objects

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrInvalidCurrency  = errors.New("invalid currency code")
	ErrNegativeMoney    = errors.New("money amount cannot be negative")
	ErrDivisionByZero   = errors.New("division by zero")
)

// Money представляет денежную сумму в определенной валюте с поддержкой финансовых операций
type Money struct {
	amount   decimal.Decimal
	currency string
}

// NewMoney создает Money с валидацией валюты и неотрицательной суммы
func NewMoney(amount decimal.Decimal, currency string) (Money, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if len(currency) == 0 {
		return Money{}, ErrInvalidCurrency
	}

	if amount.IsNegative() {
		return Money{}, ErrNegativeMoney
	}

	return Money{
		amount:   amount,
		currency: currency,
	}, nil
}

// NewMoneyFromString создает Money из строки
func NewMoneyFromString(amountStr, currency string) (Money, error) {
	amount, err := decimal.NewFromString(strings.TrimSpace(amountStr))
	if err != nil {
		return Money{}, fmt.Errorf("invalid amount format: %w", err)
	}
	return NewMoney(amount, currency)
}

// NewMoneyFromMajorUnits создает Money из основных единиц (доллары, евро)
func NewMoneyFromMajorUnits(units float64, currency string) (Money, error) {
	amount := decimal.NewFromFloat(units)
	return NewMoney(amount, currency)
}

// ZeroMoney создает нулевую денежную сумму
func ZeroMoney(currency string) (Money, error) {
	return NewMoney(decimal.Zero, currency)
}

// MustMoney создает Money паникуя при ошибке
func MustMoney(amount decimal.Decimal, currency string) Money {
	m, err := NewMoney(amount, currency)
	if err != nil {
		panic(err)
	}
	return m
}

// Amount возвращает денежную сумму
func (m Money) Amount() decimal.Decimal {
	return m.amount
}

// Currency возвращает валюту
func (m Money) Currency() string {
	return m.currency
}

// String возвращает строковое представление
func (m Money) String() string {
	return m.amount.String() + " " + m.currency
}

// FormattedString возвращает отформатированную строку с учетом валюты
func (m Money) FormattedString() string {
	decimals := m.getDecimalsForCurrency()
	return m.getCurrencySymbol() + m.amount.StringFixed(decimals)
}

// getCurrencySymbol возвращает символ валюты
func (m Money) getCurrencySymbol() string {
	switch m.currency {
	case "USD", "USDT", "USDC":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	case "JPY":
		return "¥"
	case "BTC", "XBT":
		return "₿"
	case "ETH":
		return "Ξ"
	default:
		return m.currency + " "
	}
}

// getDecimalsForCurrency возвращает количество знаков для валюты
func (m Money) getDecimalsForCurrency() int32 {
	switch m.currency {
	case "BTC", "XBT":
		return 8
	case "ETH":
		return 8
	case "JPY", "KRW":
		return 0
	case "USDT", "USDC", "USD", "EUR", "GBP":
		return 2
	default:
		return 2
	}
}

// IsPositive проверяет положительная ли сумма
func (m Money) IsPositive() bool {
	return m.amount.GreaterThan(decimal.Zero)
}

// IsZero проверяет нулевую сумму
func (m Money) IsZero() bool {
	return m.amount.IsZero()
}

// Compare сравнивает денежные суммы
func (m Money) Compare(other Money) int {
	if m.currency != other.currency {
		return -2
	}
	return m.amount.Cmp(other.amount)
}

// GreaterThan проверяет больше ли сумма
func (m Money) GreaterThan(other Money) bool {
	return m.Compare(other) == 1
}

// LessThan проверяет меньше ли сумма
func (m Money) LessThan(other Money) bool {
	return m.Compare(other) == -1
}

// Equal проверяет равенство сумм
func (m Money) Equal(other Money) bool {
	return m.Compare(other) == 0
}

// Add добавляет денежные суммы
func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, ErrCurrencyMismatch
	}
	result := m.amount.Add(other.amount)
	return NewMoney(result, m.currency)
}

// Subtract вычитает денежные суммы
func (m Money) Subtract(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, ErrCurrencyMismatch
	}
	result := m.amount.Sub(other.amount)
	if result.IsNegative() {
		return Money{}, ErrNegativeMoney
	}
	return NewMoney(result, m.currency)
}

// Multiply умножает на коэффициент
func (m Money) Multiply(factor decimal.Decimal) (Money, error) {
	if factor.IsNegative() {
		return Money{}, ErrNegativeMoney
	}
	result := m.amount.Mul(factor)
	return NewMoney(result, m.currency)
}

// Divide делит на коэффициент
func (m Money) Divide(factor decimal.Decimal) (Money, error) {
	if factor.IsZero() {
		return Money{}, ErrDivisionByZero
	}
	if factor.IsNegative() {
		return Money{}, ErrNegativeMoney
	}
	result := m.amount.Div(factor)
	return NewMoney(result, m.currency)
}

// Allocate распределяет сумму по пропорциям
func (m Money) Allocate(ratios []decimal.Decimal) ([]Money, error) {
	if len(ratios) == 0 {
		return nil, errors.New("allocation ratios cannot be empty")
	}

	total := decimal.Zero
	for _, ratio := range ratios {
		if ratio.IsNegative() {
			return nil, errors.New("allocation ratio cannot be negative")
		}
		total = total.Add(ratio)
	}

	if total.IsZero() {
		return nil, errors.New("total allocation ratio cannot be zero")
	}

	results := make([]Money, len(ratios))
	allocated := decimal.Zero

	for i, ratio := range ratios {
		var amount decimal.Decimal
		if i == len(ratios)-1 {
			amount = m.amount.Sub(allocated)
		} else {
			amount = m.amount.Mul(ratio).Div(total).Truncate(int32(m.getDecimalsForCurrency()))
			allocated = allocated.Add(amount)
		}

		money, err := NewMoney(amount, m.currency)
		if err != nil {
			return nil, err
		}
		results[i] = money
	}

	return results, nil
}

// Percentage вычисляет процент от суммы
func (m Money) Percentage(p Percentage) (Money, error) {
	factor, err := p.DecimalFraction()
	if err != nil {
		return Money{}, err
	}
	return m.Multiply(factor)
}

// MarshalJSON для сериализации в JSON
func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(`"` + m.String() + `"`), nil
}

// UnmarshalJSON для десериализации из JSON
func (m *Money) UnmarshalJSON(data []byte) error {
	str := string(data)
	if len(str) < 2 || str[0] != '"' || str[len(str)-1] != '"' {
		return errors.New("invalid money format")
	}

	parts := strings.Split(str[1:len(str)-1], " ")
	if len(parts) < 2 {
		return errors.New("invalid money format")
	}

	amount, err := decimal.NewFromString(parts[0])
	if err != nil {
		return err
	}

	currency := parts[1]
	for i := 2; i < len(parts); i++ {
		currency += " " + parts[i]
	}

	money, err := NewMoney(amount, currency)
	if err != nil {
		return err
	}

	*m = money
	return nil
}

// Round округляет до указанного количества знаков
func (m Money) Round(places int32) (Money, error) {
	rounded := m.amount.Round(places)
	return NewMoney(rounded, m.currency)
}

// Truncate обрезает до указанного количества знаков
func (m Money) Truncate(places int32) (Money, error) {
	truncated := m.amount.Truncate(places)
	return NewMoney(truncated, m.currency)
}
