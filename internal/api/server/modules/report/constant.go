package report

import "attendance-system/internal/api/server/modules/attendance"

// statuses are the values the status filter accepts.
var statuses = []attendance.Status{
	attendance.StatusPresent, attendance.StatusLate, attendance.StatusOnLeave, attendance.StatusHoliday,
	attendance.StatusOff, attendance.StatusNotYet, attendance.StatusAbsent,
}

var csvHeader = []string{
	"user_id", "full_name", "department_id", "work_date", "status", "check_in_at", "check_out_at",
	"late_minutes", "early_leave_minutes", "worked_minutes",
}

var pdfHeader = []string{"Name", "Present", "Late", "On leave", "Absent", "Hours"}

// pdfColumns are the grid widths of pdfHeader's columns, out of maroto's 12.
var pdfColumns = []int{4, 2, 1, 2, 1, 2}
