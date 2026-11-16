package plan

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
)

// equalValues compares JSON values independently of their Go number types.
// Persisted values use json.Number; arithmetic and executor outputs may use
// native numbers. Normalize decimal representations without rounding either side.
func equalValues(left, right any) bool {
	if l, ok := decimalValue(left); ok {
		r, ok := decimalValue(right)
		return ok && l == r
	}
	switch l := left.(type) {
	case map[string]any:
		r, ok := right.(map[string]any)
		if !ok || len(l) != len(r) || (l == nil) != (r == nil) {
			return false
		}
		for key, value := range l {
			other, ok := r[key]
			if !ok || !equalValues(value, other) {
				return false
			}
		}
		return true
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) || (l == nil) != (r == nil) {
			return false
		}
		for i, value := range l {
			if !equalValues(value, r[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(left, right)
	}
}

// compareNumbers orders numbers using the same exact decimal representation as
// equalValues, so ordering agrees with equality beyond float64 precision.
func compareNumbers(left, right any) (int, error) {
	l, lok := decimalValue(left)
	r, rok := decimalValue(right)
	if lok && rok {
		return compareDecimals(l, r), nil
	}
	lf, err := toFloat64Val(left)
	if err != nil {
		return 0, err
	}
	rf, err := toFloat64Val(right)
	if err != nil {
		return 0, err
	}
	switch {
	case math.IsNaN(lf) || math.IsNaN(rf):
		return 0, fmt.Errorf("muxlio: cannot compare NaN")
	case lf < rf:
		return -1, nil
	case lf > rf:
		return 1, nil
	}
	return 0, nil
}

// compareDecimals orders canonical decimalValue forms without expanding
// exponents: zero is "0", otherwise an optionally signed coefficient without
// leading or trailing zeros followed by "e" and a base-10 exponent.
func compareDecimals(left, right string) int {
	sign := func(s string) int {
		switch {
		case s == "0":
			return 0
		case strings.HasPrefix(s, "-"):
			return -1
		}
		return 1
	}
	ls, rs := sign(left), sign(right)
	if ls != rs || ls == 0 {
		switch {
		case ls < rs:
			return -1
		case ls > rs:
			return 1
		}
		return 0
	}
	magnitude := func(s string) (string, *big.Int) {
		s = strings.TrimPrefix(s, "-")
		cut := strings.IndexByte(s, 'e')
		exponent, _ := new(big.Int).SetString(s[cut+1:], 10)
		// The adjusted exponent locates the most significant digit.
		return s[:cut], exponent.Add(exponent, big.NewInt(int64(cut)))
	}
	lc, le := magnitude(left)
	rc, re := magnitude(right)
	cmp := le.Cmp(re)
	if cmp == 0 {
		// Equal leading positions: digit strings compare lexicographically,
		// and a longer coefficient with the same prefix has more nonzero digits.
		cmp = strings.Compare(lc, rc)
	}
	return cmp * ls
}

// decimalValue returns a canonical coefficient and decimal exponent. Keeping
// the exponent separate also handles very large JSON exponents without allocating
// memory proportional to the numeric magnitude.
func decimalValue(value any) (string, bool) {
	var text string
	if number, ok := value.(json.Number); ok {
		text = string(number)
	} else {
		v := reflect.ValueOf(value)
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			text = strconv.FormatInt(v.Int(), 10)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			text = strconv.FormatUint(v.Uint(), 10)
		case reflect.Float32, reflect.Float64:
			text = strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits())
		default:
			return "", false
		}
	}
	if !json.Valid([]byte(text)) {
		return "", false
	}
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	exponent := new(big.Int)
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		if _, ok := exponent.SetString(text[i+1:], 10); !ok {
			return "", false
		}
		text = text[:i]
	}
	if i := strings.IndexByte(text, '.'); i >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(text)-i-1)))
		text = text[:i] + text[i+1:]
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return "0", true
	}
	coefficient := strings.TrimRight(text, "0")
	exponent.Add(exponent, big.NewInt(int64(len(text)-len(coefficient))))
	if negative {
		coefficient = "-" + coefficient
	}
	return coefficient + "e" + exponent.String(), true
}
