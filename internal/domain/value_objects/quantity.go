package value_objects

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

var (
	ErrNegativeQuantity = errors.New("quantity cannot be negative")
	ErrZeroQuantity     = errors.New("quantity cannot be zero")
	ErrInvalidLotSize   = errors.New("quantity does not conform to lot size requirements")
)

// Quantity представляет количество актива
type Quantity struct {
	value decimal.Decimal
}

// NewQuantity создает Quantity с базовой валидацией (неотрицательность и ненулевое)
func NewQuantity(value decimal.Decimal) (Quantity, error) {
	if value.IsNegative() {
		return Quantity{}, ErrNegativeQuantity
	}
	if value.IsZero() {
		return Quantity{}, ErrZeroQuantity
	}
	return Quantity{value: value}, nil
}

// NewQuantityFromString - основной конструктор
func NewQuantityFromString(s string) (Quantity, error) {
	dec, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return Quantity{}, fmt.Errorf("invalid quantity format: %w", err)
	}
	return NewQuantity(dec)
}

// MustQuantity создает Quantity паникуя при ошибке
func MustQuantity(value decimal.Decimal) Quantity {
	q, err := NewQuantity(value)
	if err != nil {
		panic(err)
	}
	return q
}

// Value возвращает decimal значение
func (q Quantity) Value() decimal.Decimal {
	return q.value
}

// String реализует Stringer
func (q Quantity) String() string {
	return q.value.String()
}

// FormattedString возвращает отформатированную строку
func (q Quantity) FormattedString() string {
	return q.value.StringFixed(int32(-q.value.Exponent()))
}

// Compare сравнивает два количества
func (q Quantity) Compare(other Quantity) int {
	return q.value.Cmp(other.value)
}

// GreaterThan проверяет больше ли количество
func (q Quantity) GreaterThan(other Quantity) bool {
	return q.Compare(other) == 1
}

// LessThan проверяет меньше ли количество
func (q Quantity) LessThan(other Quantity) bool {
	return q.Compare(other) == -1
}

// Equal проверяет равенство количеств
func (q Quantity) Equal(other Quantity) bool {
	return q.Compare(other) == 0
}

// Validate проверяет количество на соответствие ограничениям
func (q Quantity) Validate(lotSize, minQuantity, maxQuantity decimal.Decimal) error {
	if minQuantity.IsPositive() && q.value.LessThan(minQuantity) {
		return fmt.Errorf("quantity %s is less than minimum %s", q.value.String(), minQuantity.String())
	}
	if maxQuantity.IsPositive() && q.value.GreaterThan(maxQuantity) {
		return fmt.Errorf("quantity %s exceeds maximum %s", q.value.String(), maxQuantity.String())
	}
	if lotSize.IsPositive() {
		scaled := q.value.Div(lotSize)
		if !scaled.IsInteger() {
			return fmt.Errorf("%w: %s not multiple of lot size %s", ErrInvalidLotSize, q.value.String(), lotSize.String())
		}
	}
	return nil
}

// MarshalJSON для сериализации в JSON
func (q Quantity) MarshalJSON() ([]byte, error) {
	return []byte(`"` + q.String() + `"`), nil
}

// UnmarshalJSON для десериализации из JSON
func (q *Quantity) UnmarshalJSON(data []byte) error {
	str := string(data)
	if len(str) < 2 || str[0] != '"' || str[len(str)-1] != '"' {
		return errors.New("invalid quantity format")
	}

	dec, err := decimal.NewFromString(str[1 : len(str)-1])
	if err != nil {
		return err
	}

	*q = Quantity{value: dec}
	return nil
}
