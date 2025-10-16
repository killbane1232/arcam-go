package value_objects

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

var (
	ErrNegativePrice       = errors.New("price cannot be negative")
	ErrInvalidPriceFormat  = errors.New("invalid price format")
	ErrPricePrecision      = errors.New("price precision exceeds allowed limits")
	ErrDifferentCurrencies = errors.New("currencies must be the same")
)

// Price представляет денежную сумму с фиксированной точностью для финансовых расчетов
type Price struct {
	value    decimal.Decimal
	currency string
}

// NewPrice создает новый Price с валидацией
func NewPrice(value decimal.Decimal, currency string) (Price, error) {
	if value.IsNegative() {
		return Price{}, ErrNegativePrice
	}
	if currency == "" {
		return Price{}, errors.New("currency cannot be empty")
	}

	// Валидация точности (максимум 8 знаков для криптовалют)
	if value.Exponent() < -8 {
		return Price{}, ErrPricePrecision
	}

	return Price{
		value:    value,
		currency: strings.ToUpper(currency),
	}, nil
}

// NewPriceFromString создает Price из строки (основной метод)
func NewPriceFromString(s string, currency string) (Price, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Price{}, ErrInvalidPriceFormat
	}

	dec, err := decimal.NewFromString(s)
	if err != nil {
		return Price{}, fmt.Errorf("%w: %v", ErrInvalidPriceFormat, err)
	}
	return NewPrice(dec, currency)
}

// NewPriceFromFloat создает Price из float64 (только для тестов)
func NewPriceFromFloat(f float64, currency string) (Price, error) {
	dec := decimal.NewFromFloat(f)
	return NewPrice(dec, currency)
}

// MustPrice создает Price паникуя при ошибке (только для тестов и инициализации)
func MustPrice(value decimal.Decimal, currency string) Price {
	p, err := NewPrice(value, currency)
	if err != nil {
		panic(err)
	}
	return p
}

// Value возвращает внутреннее decimal значение
func (p Price) Value() decimal.Decimal {
	return p.value
}

// Currency возвращает валюту цены
func (p Price) Currency() string {
	return p.currency
}

// String возвращает строковое представление
func (p Price) String() string {
	return p.value.String() + " " + p.currency
}

// FormattedString возвращает отформатированную строку
func (p Price) FormattedString() string {
	return p.value.StringFixed(int32(-p.value.Exponent()))
}

// Multiply умножает цену на количество для расчета стоимости
func (p Price) Multiply(q Quantity) (Money, error) {
	amount := p.value.Mul(q.Value())
	return NewMoney(amount, p.currency)
}

// Compare сравнивает две цены. Паникует, если валюты разные.
func (p Price) Compare(other Price) int {
	if p.currency != other.currency {
		panic(ErrDifferentCurrencies)
	}
	return p.value.Cmp(other.value)
}

// GreaterThan проверяет больше ли цена
func (p Price) GreaterThan(other Price) bool {
	return p.Compare(other) == 1
}

// LessThan проверяет меньше ли цена
func (p Price) LessThan(other Price) bool {
	return p.Compare(other) == -1
}

// Equal проверяет равенство цен
func (p Price) Equal(other Price) bool {
	return p.Compare(other) == 0
}

// Add добавляет другую цену (для расчетов спредов)
func (p Price) Add(other Price) (Price, error) {
	if p.currency != other.currency {
		return Price{}, ErrDifferentCurrencies
	}
	return NewPrice(p.value.Add(other.value), p.currency)
}

// Subtract вычитает другую цену
func (p Price) Subtract(other Price) (Price, error) {
	if p.currency != other.currency {
		return Price{}, ErrDifferentCurrencies
	}
	result := p.value.Sub(other.value)
	if result.IsNegative() {
		return Price{}, errors.New("resulting price cannot be negative")
	}
	return NewPrice(result, p.currency)
}

// Percentage вычисляет процентное отношение к другой цене
func (p Price) Percentage(of Price) (Percentage, error) {
	if p.currency != of.currency {
		return Percentage{}, ErrDifferentCurrencies
	}
	if of.value.IsZero() {
		return Percentage{}, errors.New("cannot calculate percentage relative to zero")
	}

	percentage := p.value.Div(of.value).Mul(decimal.NewFromInt(100))
	return NewPercentage(percentage)
}

// RoundToTick округляет цену до ближайшего шага цены (tick size)
func (p Price) RoundToTick(tickSize decimal.Decimal) (Price, error) {
	if tickSize.IsZero() {
		return p, nil
	}
	rounded := p.value.Div(tickSize).Round(0).Mul(tickSize)
	return NewPrice(rounded, p.currency)
}

// CheckTickSize проверяет, что цена соответствует шагу цены
func (p Price) CheckTickSize(tickSize decimal.Decimal) error {
	if tickSize.IsZero() {
		return nil
	}
	remainder := p.value.Mod(tickSize)
	if !remainder.IsZero() {
		return errors.New("price does not conform to tick size")
	}
	return nil
}

// MarshalJSON для сериализации в JSON
func (p Price) MarshalJSON() ([]byte, error) {
	return []byte(`"` + p.String() + `"`), nil
}

// UnmarshalJSON для десериализации из JSON
func (p *Price) UnmarshalJSON(data []byte) error {
	str := string(data)
	if len(str) < 2 || str[0] != '"' || str[len(str)-1] != '"' {
		return ErrInvalidPriceFormat
	}

	parts := strings.Split(str[1:len(str)-1], " ")
	if len(parts) != 2 {
		return ErrInvalidPriceFormat
	}

	price, err := NewPriceFromString(parts[0], parts[1])
	if err != nil {
		return err
	}

	*p = price
	return nil
}
