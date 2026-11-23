package bill

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var ErrInvalidAmount = errors.New("invalid bill amount")

// ParseFen parses a plain decimal amount with at most two fractional digits.
// Scientific notation, grouping separators, and values outside int64 fen are
// rejected instead of rounded.
func ParseFen(raw string, allowNegative bool) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, ErrInvalidAmount
	}
	negative := false
	if value[0] == '-' {
		if !allowNegative {
			return 0, ErrInvalidAmount
		}
		negative = true
		value = value[1:]
	} else if value[0] == '+' {
		return 0, ErrInvalidAmount
	}
	if value == "" {
		return 0, ErrInvalidAmount
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, ErrInvalidAmount
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return 0, ErrInvalidAmount
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > math.MaxInt64/100 {
		return 0, ErrInvalidAmount
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) == 0 || len(fraction) > 2 {
			return 0, ErrInvalidAmount
		}
		for _, r := range fraction {
			if r < '0' || r > '9' {
				return 0, ErrInvalidAmount
			}
		}
	}
	if len(fraction) == 1 {
		fraction += "0"
	}
	var cents int64
	if fraction != "" {
		cents, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, ErrInvalidAmount
		}
	}
	if whole == math.MaxInt64/100 && cents > math.MaxInt64%100 {
		return 0, ErrInvalidAmount
	}
	result := whole*100 + cents
	if negative {
		return -result, nil
	}
	return result, nil
}
