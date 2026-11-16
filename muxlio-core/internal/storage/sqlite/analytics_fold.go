package sqlite

import (
	"database/sql/driver"
	"strings"
	"time"

	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("go_lower", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if len(args) == 0 || args[0] == nil {
			return "", nil
		}
		s, _ := args[0].(string)
		return strings.ToLower(s), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("muxlio_time_after", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if len(args) < 2 {
			return int64(0), nil
		}
		left, lok := sqliteTimestamp(args[0])
		right, rok := sqliteTimestamp(args[1])
		if !lok || !rok || !left.After(right) {
			return int64(0), nil
		}
		return int64(1), nil
	})
}

func sqliteTimestamp(value driver.Value) (time.Time, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
