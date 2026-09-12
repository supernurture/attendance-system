package schedule

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
)

// pqInt16Array maps a Postgres smallint[] to a Go slice. database/sql knows nothing about arrays, so
// the driver hands over and expects the literal "{1,2,3}" form.
type pqInt16Array []int16

func (a *pqInt16Array) Scan(src any) error {
	var literal string
	switch value := src.(type) {
	case string:
		literal = value
	case []byte:
		literal = string(value)
	default:
		return fmt.Errorf("scan smallint[]: got %T, want the array literal Postgres sends", src)
	}

	literal = strings.Trim(strings.TrimSpace(literal), "{}")
	if literal == "" {
		*a = nil
		return nil
	}

	parts := strings.Split(literal, ",")
	parsed := make(pqInt16Array, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.ParseInt(strings.TrimSpace(part), 10, 16)
		if err != nil {
			return fmt.Errorf("scan smallint[]: %w", err)
		}
		parsed = append(parsed, int16(number))
	}
	*a = parsed
	return nil
}

func (a pqInt16Array) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}

	parts := make([]string, 0, len(a))
	for _, number := range a {
		parts = append(parts, strconv.Itoa(int(number)))
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}
