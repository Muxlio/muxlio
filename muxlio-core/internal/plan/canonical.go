package plan

import (
	"bytes"
	"encoding/json"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Preserve the compiler's JSON representation (including optional zero values)
// while it matches the typed plan. A changed typed plan never reuses stale bytes.
type planJSON Plan

func (p *Plan) UnmarshalJSON(data []byte) error {
	var v planJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*p = Plan(v)
	p.sourceJSON = append([]byte(nil), data...)
	v.Digest = ""
	p.sourceTyped, _ = json.Marshal(v)
	return nil
}

func (p Plan) MarshalJSON() ([]byte, error) {
	v := planJSON(p)
	v.Digest = ""
	typed, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(p.sourceJSON) > 0 && bytes.Equal(typed, p.sourceTyped) {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(p.sourceJSON, &raw); err != nil {
			return nil, err
		}
		delete(raw, "digest")
		if p.Digest != "" {
			raw["digest"], _ = json.Marshal(p.Digest)
		}
		return json.Marshal(raw)
	}
	v.Digest = p.Digest
	return json.Marshal(v)
}

// Canonical JSON uses sorted keys, UTF-8 strings and lossless ECMAScript number spelling.
// The TypeScript compiler implements the same encoding before SHA-256 hashing.
func canonicalJSON(value any) ([]byte, error) {
	var b bytes.Buffer
	var write func(any) error
	write = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			// JavaScript sorts UTF-16 code units, including supplementary keys.
			sort.Slice(keys, func(i, j int) bool {
				a, b := utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))
				for k := 0; k < len(a) && k < len(b); k++ {
					if a[k] != b[k] {
						return a[k] < b[k]
					}
				}
				return len(a) < len(b)
			})
			b.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := write(k); err != nil {
					return err
				}
				b.WriteByte(':')
				if err := write(x[k]); err != nil {
					return err
				}
			}
			b.WriteByte('}')
		case []any:
			b.WriteByte('[')
			for i, v := range x {
				if i > 0 {
					b.WriteByte(',')
				}
				if err := write(v); err != nil {
					return err
				}
			}
			b.WriteByte(']')
		case string:
			var s bytes.Buffer
			e := json.NewEncoder(&s)
			e.SetEscapeHTML(false)
			if err := e.Encode(x); err != nil {
				return err
			}
			encoded := strings.TrimSuffix(s.String(), "\n")
			encoded = strings.ReplaceAll(encoded, `\u2028`, "\u2028")
			encoded = strings.ReplaceAll(encoded, `\u2029`, "\u2029")
			b.WriteString(encoded)
		case json.Number:
			// Keep the SDK's ECMAScript spelling when it preserves the value.
			// HTTP clients can supply more precise numbers than JavaScript can;
			// rounding those here would let different definitions share a digest.
			f, err := x.Float64()
			if err != nil {
				return err
			}
			encoded, err := canonicalJSON(f)
			if err != nil {
				return err
			}
			if decimalIdentity(string(x)) == decimalIdentity(string(encoded)) {
				b.Write(encoded)
				break
			}
			b.WriteString(string(x))
		case float64:
			if x == 0 {
				b.WriteByte('0')
				break
			}
			format := byte('f')
			abs := x
			if abs < 0 {
				abs = -abs
			}
			if abs < 1e-6 || abs >= 1e21 {
				format = 'e'
			}
			s := strconv.FormatFloat(x, format, -1, 64)
			s = strings.ReplaceAll(s, "e-0", "e-")
			s = strings.ReplaceAll(s, "e+0", "e+")
			b.WriteString(s)
		default:
			data, err := json.Marshal(v)
			if err != nil {
				return err
			}
			b.Write(data)
		}
		return nil
	}
	if err := write(value); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// decimalIdentity compares JSON decimal values without floating point rounding
// or expanding exponents (which may be arbitrarily large in untrusted JSON).
func decimalIdentity(s string) string {
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var exponent big.Int
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exponent.SetString(strings.TrimPrefix(s[i+1:], "+"), 10)
		s = s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		exponent.Sub(&exponent, big.NewInt(int64(len(s)-i-1)))
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	digits := strings.TrimRight(s, "0")
	exponent.Add(&exponent, big.NewInt(int64(len(s)-len(digits))))
	if negative {
		digits = "-" + digits
	}
	return digits + "e" + exponent.String()
}
