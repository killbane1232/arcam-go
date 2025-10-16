package value_objects

import (
	"github.com/shopspring/decimal"
)

// PriceDelta представляет изменение цены (может быть отрицательным)
type PriceDelta struct {
	value decimal.Decimal
}

func NewPriceDelta(value decimal.Decimal) PriceDelta {
	return PriceDelta{value: value}
}

func NewPriceDeltaFromString(s string) (PriceDelta, error) {
	dec, err := decimal.NewFromString(s)
	if err != nil {
		return PriceDelta{}, err
	}
	return NewPriceDelta(dec), nil
}

func (d PriceDelta) Value() decimal.Decimal {
	return d.value
}

func (d PriceDelta) String() string {
	return d.value.String()
}

func (d PriceDelta) Add(other PriceDelta) PriceDelta {
	return NewPriceDelta(d.value.Add(other.value))
}

func (d PriceDelta) Subtract(other PriceDelta) PriceDelta {
	return NewPriceDelta(d.value.Sub(other.value))
}

func (d PriceDelta) IsPositive() bool {
	return d.value.GreaterThan(decimal.Zero)
}

func (d PriceDelta) IsNegative() bool {
	return d.value.LessThan(decimal.Zero)
}

func (d PriceDelta) IsZero() bool {
	return d.value.IsZero()
}
