package utils

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// MaxAmountDecimals is the highest token precision ParseDecimalAmount accepts.
const MaxAmountDecimals = 38

// ParseDecimalAmount converts a positive decimal string ("12.5") into atomic
// units. It rejects signs, exponents, and more fractional digits than decimals
// instead of truncating, so a client can never be quoted for less than it typed.
func ParseDecimalAmount(s string, decimals int) (*big.Int, error) {
	if decimals < 0 || decimals > MaxAmountDecimals {
		return nil, fmt.Errorf("unsupported decimals %d", decimals)
	}
	if s == "" || len(s) > 40 {
		return nil, errors.New("amount is empty or too long")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || strings.Trim(whole, "0123456789") != "" || (strings.Contains(s, ".") && (frac == "" || strings.Trim(frac, "0123456789") != "")) {
		return nil, fmt.Errorf("amount %q is not a plain decimal number", s)
	}
	if len(frac) > decimals {
		return nil, fmt.Errorf("amount %q has more than %d decimal places", s, decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	v, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok || v.Sign() <= 0 {
		return nil, fmt.Errorf("amount %q must be positive", s)
	}
	return v, nil
}
