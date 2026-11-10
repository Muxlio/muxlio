package sqlite

import (
	"database/sql/driver"
	"strings"

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
}
