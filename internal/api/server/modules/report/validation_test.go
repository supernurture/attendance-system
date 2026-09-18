package report

import (
	"testing"

	"attendance-system/internal/api/server/modules/attendance"
)

func TestCheckStatus(t *testing.T) {
	absent, asleep := attendance.StatusAbsent, attendance.Status("asleep")
	if err := checkStatus(nil); err != nil {
		t.Errorf("no filter: %v", err)
	}
	if err := checkStatus(&absent); err != nil {
		t.Errorf("absent: %v", err)
	}
	if err := checkStatus(&asleep); err == nil {
		t.Error("asleep: want a validation error")
	}
}
