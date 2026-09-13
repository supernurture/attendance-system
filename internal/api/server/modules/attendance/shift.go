package attendance

import (
	"time"

	"attendance-system/internal/api/server/modules/schedule"
	"attendance-system/internal/pkg/apperr"
)

// shift is one work date's hours placed on the clock; hours is nil on a date that has none.
type shift struct {
	date  time.Time
	hours *schedule.WorkSchedule
	start time.Time
	end   time.Time
}

// place reads the schedule's times in zone on that date, moving the end a day on for a night shift.
func place(date time.Time, hours *schedule.WorkSchedule, zone *time.Location) shift {
	placed := shift{date: dateOnly(date), hours: hours}
	if hours == nil {
		return placed
	}

	endDay := date.Day()
	if hours.CrossesMidnight() {
		endDay++
	}
	// Minutes past 59 normalize into hours, and time.Date keeps the wall clock right across a DST change.
	placed.start = time.Date(date.Year(), date.Month(), date.Day(), 0, int(hours.StartTime), 0, 0, zone)
	placed.end = time.Date(date.Year(), date.Month(), endDay, 0, int(hours.EndTime), 0, 0, zone)
	return placed
}

// pickShift finds the shift a check-in at now belongs to, from yesterday, today and tomorrow in zone: the
// working one whose window, from checkInOpensBefore its start to its end, holds now, nearest start first.
// Failing that it is today, even past its end; only a today whose check-in has not opened yet is refused.
func pickShift(now time.Time, zone *time.Location, days []schedule.Day) (shift, error) {
	var best *shift
	for _, day := range days {
		if !day.Working {
			continue
		}
		candidate := place(day.Date, day.Schedule, zone)
		if now.Before(candidate.start.Add(-checkInOpensBefore)) || now.After(candidate.end) {
			continue
		}
		if best == nil || now.Sub(candidate.start).Abs() < now.Sub(best.start).Abs() {
			best = &candidate
		}
	}
	if best != nil {
		return *best, nil
	}

	today := place(days[1].Date, days[1].Schedule, zone)
	if opens := today.start.Add(-checkInOpensBefore); today.hours != nil && now.Before(opens) {
		return shift{}, apperr.Invalid("check-in for today's %s shift opens at %s",
			today.hours.StartTime, opens.In(zone).Format("15:04"))
	}
	return today, nil
}

func (s shift) scheduleID() *int64 {
	if s.hours == nil {
		return nil
	}
	return &s.hours.ID
}

// lateMinutes counts whole minutes past the start, less the grace, and never more than the whole shift.
func (s shift) lateMinutes(at time.Time) int {
	if s.hours == nil {
		return 0
	}
	return min(s.minutes(), max(0, int(at.Sub(s.start)/time.Minute)-s.hours.GraceMinutes))
}

// earlyLeaveMinutes counts whole minutes left before the end, and never more than the whole shift: leaving
// before it even started still missed only that shift.
func (s shift) earlyLeaveMinutes(at time.Time) int {
	if s.hours == nil {
		return 0
	}
	return min(s.minutes(), max(0, int(s.end.Sub(at)/time.Minute)))
}

// minutes is how long the shift runs.
func (s shift) minutes() int {
	return int(s.end.Sub(s.start) / time.Minute)
}

// dateOnly drops the time of day: a work date is a calendar day, stored at UTC midnight.
func dateOnly(at time.Time) time.Time {
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
}

// dateIn is the calendar day it is in zone at that moment.
func dateIn(at time.Time, zone *time.Location) time.Time {
	return dateOnly(at.In(zone))
}
