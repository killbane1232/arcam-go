package value_objects

import (
	"errors"

	"github.com/shopspring/decimal"
)

var (
	ErrPercentageOutOfRange = errors.New("percentage must be between 0 and 100")
)

type Percentage struct {
	value decimal.Decimal
}

func NewPercentage(value decimal.Decimal) (Percentage, error) {
	if value.LessThan(decimal.Zero) || value.GreaterThan(decimal.NewFromInt(100)) {
		return Percentage{}, ErrPercentageOutOfRange
	}
	return Percentage{value: value}, nil
}

func NewPercentageFromString(s string) (Percentage, error) {
	dec, err := decimal.NewFromString(s)
	if err != nil {
		return Percentage{}, err
	}
	return NewPercentage(dec)
}

func MustPercentage(value decimal.Decimal) Percentage {
	p, err := NewPercentage(value)
	if err != nil {
		panic(err)
	}
	return p
}

func (p Percentage) Value() decimal.Decimal {
	return p.value
}

func (p Percentage) String() string {
	return p.value.String() + "%"
}

func (p Percentage) DecimalFraction() (decimal.Decimal, error) {
	if p.value.LessThan(decimal.Zero) || p.value.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Decimal{}, ErrPercentageOutOfRange
	}
	return p.value.Div(decimal.NewFromInt(100)), nil
}

func (p Percentage) Add(other Percentage) (Percentage, error) {
	result := p.value.Add(other.value)
	return NewPercentage(result)
}

func (p Percentage) Subtract(other Percentage) (Percentage, error) {
	result := p.value.Sub(other.value)
	return NewPercentage(result)
}
