// Package accounting implements provider-neutral exact accounting mechanics.
// Provider decoders remain responsible for interpreting their source events.
package accounting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/muxlio/muxlio-adapters/contract"
)

const scaleDigits = 18

// ParseJSONAmount normalizes a bounded source JSON number exactly, including
// scientific notation. The public accounting wire contract still requires
// canonical decimal strings; this helper belongs at source decoding boundaries.
func ParseJSONAmount(raw json.RawMessage) (contract.Amount, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 100 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return "", fmt.Errorf("expected a bounded JSON number")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", err
	}
	if i := strings.IndexAny(number.String(), "eE"); i >= 0 {
		exp, err := strconv.Atoi(number.String()[i+1:])
		if err != nil || exp < -100 || exp > 100 {
			return "", fmt.Errorf("amount exponent out of range")
		}
	}
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || rational.Sign() < 0 {
		return "", fmt.Errorf("invalid nonnegative amount")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(scaleDigits), nil)
	if !new(big.Rat).Mul(rational, new(big.Rat).SetInt(scale)).IsInt() {
		return "", fmt.Errorf("amount exceeds 18 decimal places")
	}
	return ParseAmount(strings.TrimRight(strings.TrimRight(rational.FloatString(scaleDigits), "0"), "."))
}

// ParseAmount normalizes a plain decimal without converting through float64.
func ParseAmount(raw string) (contract.Amount, error) {
	if raw == "" || strings.Trim(raw, "0123456789.") != "" || strings.Count(raw, ".") > 1 {
		return "", fmt.Errorf("invalid decimal amount")
	}
	parts := strings.SplitN(raw, ".", 2)
	whole := strings.TrimLeft(parts[0], "0")
	if whole == "" {
		whole = "0"
	}
	out := whole
	if len(parts) == 2 {
		if parts[1] == "" {
			return "", fmt.Errorf("invalid decimal amount")
		}
		frac := strings.TrimRight(parts[1], "0")
		if frac != "" {
			out += "." + frac
		}
	}
	a := contract.Amount(out)
	return a, a.Validate()
}

func decimalParts(v contract.Amount) (*big.Int, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	s := string(v)
	parts := strings.SplitN(s, ".", 2)
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > scaleDigits {
		return nil, fmt.Errorf("amount precision exceeds %d places", scaleDigits)
	}
	joined := parts[0] + frac + strings.Repeat("0", scaleDigits-len(frac))
	n, ok := new(big.Int).SetString(joined, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	return n, nil
}

func formatAmount(n *big.Int) contract.Amount {
	if n.Sign() == 0 {
		return "0"
	}
	neg := n.Sign() < 0
	if neg {
		n = new(big.Int).Neg(n)
	}
	s := n.String()
	if len(s) <= scaleDigits {
		s = strings.Repeat("0", scaleDigits-len(s)+1) + s
	}
	whole, frac := s[:len(s)-scaleDigits], strings.TrimRight(s[len(s)-scaleDigits:], "0")
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg {
		return contract.Amount("-" + out)
	}
	return contract.Amount(out)
}

// AddAmount performs exact checked-scale addition without float conversion.
func AddAmount(a, b contract.Amount) (contract.Amount, error) {
	x, err := decimalParts(a)
	if err != nil {
		return "", err
	}
	y, err := decimalParts(b)
	if err != nil {
		return "", err
	}
	x.Add(x, y)
	out := formatAmount(x)
	if err := out.Validate(); err != nil {
		return "", err
	}
	return out, nil
}

// SubtractAmount rejects resets rather than clamping a negative delta to zero.
func SubtractAmount(a, b contract.Amount) (contract.Amount, error) {
	x, err := decimalParts(a)
	if err != nil {
		return "", err
	}
	y, err := decimalParts(b)
	if err != nil {
		return "", err
	}
	if x.Cmp(y) < 0 {
		return "", fmt.Errorf("cumulative amount regressed")
	}
	out := formatAmount(x.Sub(x, y))
	return out, out.Validate()
}

// AddCount adds canonical token counts and rejects overflow.
func AddCount(a, b contract.Count) (contract.Count, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	if err := b.Validate(); err != nil {
		return "", err
	}
	x, _ := new(big.Int).SetString(string(a), 10)
	y, _ := new(big.Int).SetString(string(b), 10)
	x.Add(x, y)
	out := contract.Count(x.String())
	if err := out.Validate(); err != nil {
		return "", err
	}
	return out, nil
}
