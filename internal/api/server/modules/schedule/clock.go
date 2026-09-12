package schedule

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"

	"attendance-system/internal/pkg/apperr"
)

// Clock is a time of day as minutes since midnight. Postgres hands a `time` column back as text, and
// minutes are what the schedule rules actually do arithmetic on, so neither side needs a time.Time.
type Clock int

const minutesPerDay = 24 * 60

// ParseClock reads "HH:MM" or "HH:MM:SS"; seconds are ignored because a schedule is minute-grained.
func ParseClock(value string) (Clock, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, apperr.Invalid("time %q must look like HH:MM", value)
	}

	hours, hoursErr := strconv.Atoi(parts[0])
	minutes, minutesErr := strconv.Atoi(parts[1])
	if hoursErr != nil || minutesErr != nil || hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return 0, apperr.Invalid("time %q must look like HH:MM", value)
	}
	return Clock(hours*60 + minutes), nil
}

// String is the "HH:MM" the API speaks.
func (c Clock) String() string {
	return fmt.Sprintf("%02d:%02d", int(c)/60, int(c)%60)
}

// Scan reads the text Postgres returns for a `time` column.
func (c *Clock) Scan(src any) error {
	text, ok := src.(string)
	if !ok {
		return fmt.Errorf("scan time: got %T, want the text Postgres sends for a time column", src)
	}

	parsed, err := ParseClock(text)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// Value writes a form Postgres casts to `time`.
func (c Clock) Value() (driver.Value, error) {
	return c.String() + ":00", nil
}
