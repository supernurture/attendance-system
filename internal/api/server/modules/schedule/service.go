package schedule

import (
	"context"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

type Service struct {
	repo *Repository
	zone *time.Location
}

// NewService measures "today", which decides what is past, in the company's zone rather than the server's.
func NewService(db *gorm.DB, zone *time.Location) *Service {
	return &Service{repo: NewRepository(db), zone: zone}
}

// Hours are the fields one work schedule carries. The times stay text until the service parses them,
// so a malformed one is reported like any other rejected field.
type Hours struct {
	Name             string
	StartTime        string
	EndTime          string
	GraceMinutes     int
	BreakMinutes     int
	Workdays         []int
	ObservesHolidays bool
}

// Place is one office's circle on the map.
type Place struct {
	Name    string
	Lat     float64
	Lng     float64
	RadiusM int
}

// Assignment is one roster entry to write. A nil ScheduleID is a planned day off.
type Assignment struct {
	UserID     int64
	WorkDate   time.Time
	ScheduleID *int64
	Note       string
}

// Days applies rule A across a span for one person: the roster first, then their own schedule, with an
// observed holiday turning either off.
func (s *Service) Days(ctx context.Context, userID int64, span Range) ([]Day, error) {
	span, err := CheckRange(span)
	if err != nil {
		return nil, err
	}

	defaultScheduleID, err := s.repo.DefaultScheduleID(ctx, userID)
	if err != nil {
		return nil, err
	}
	assignments, err := s.repo.ListAssignments(ctx, span, &userID)
	if err != nil {
		return nil, err
	}
	holidays, err := s.repo.ListHolidays(ctx, span)
	if err != nil {
		return nil, err
	}

	wanted := make([]int64, 0, len(assignments)+1)
	if defaultScheduleID != nil {
		wanted = append(wanted, *defaultScheduleID)
	}
	byDate := make(map[string]ShiftAssignment, len(assignments))
	for _, assignment := range assignments {
		byDate[key(assignment.WorkDate)] = assignment
		if assignment.ScheduleID != nil {
			wanted = append(wanted, *assignment.ScheduleID)
		}
	}
	schedules, err := s.repo.SchedulesByID(ctx, wanted)
	if err != nil {
		return nil, err
	}

	named := make(map[string]string, len(holidays))
	for _, holiday := range holidays {
		named[key(holiday.Date)] = holiday.Name
	}

	days := make([]Day, 0, span.days())
	for date := span.From; !date.After(span.To); date = date.AddDate(0, 0, 1) {
		in := Inputs{Holiday: named[key(date)], Default: lookup(schedules, defaultScheduleID)}
		if assignment, rostered := byDate[key(date)]; rostered {
			in.Assignment = &assignment
			in.Assigned = lookup(schedules, assignment.ScheduleID)
		}
		days = append(days, Resolve(date, in))
	}
	return days, nil
}

// ListSchedules is readable by anyone who plans for other people.
func (s *Service) ListSchedules(ctx context.Context, claims middleware.Claims) ([]WorkSchedule, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, apperr.ErrForbidden
	}
	return s.repo.ListSchedules(ctx)
}

func (s *Service) CreateSchedule(ctx context.Context, claims middleware.Claims, hours Hours) (WorkSchedule, error) {
	schedule, err := buildSchedule(claims, hours)
	if err != nil {
		return WorkSchedule{}, err
	}
	if err := s.repo.CreateSchedule(ctx, &schedule); err != nil {
		return WorkSchedule{}, err
	}
	return schedule, nil
}

func (s *Service) ReplaceSchedule(
	ctx context.Context, claims middleware.Claims, id int64, hours Hours,
) (WorkSchedule, error) {
	schedule, err := buildSchedule(claims, hours)
	if err != nil {
		return WorkSchedule{}, err
	}
	schedule.ID = id
	return s.repo.ReplaceSchedule(ctx, schedule)
}

// DeleteSchedule refuses while anyone keeps it or is rostered onto it, which would leave their days
// unresolvable.
func (s *Service) DeleteSchedule(ctx context.Context, claims middleware.Claims, id int64) error {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return apperr.ErrForbidden
	}

	inUse, err := s.repo.ScheduleInUse(ctx, id, s.today())
	if err != nil {
		return err
	}
	if inUse {
		return fmt.Errorf("%w: the schedule is still in use", apperr.ErrConflict)
	}
	return s.repo.DeleteSchedule(ctx, id)
}

func (s *Service) ListHolidays(ctx context.Context, claims middleware.Claims, span Range) ([]Holiday, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, apperr.ErrForbidden
	}
	span, err := CheckRange(span)
	if err != nil {
		return nil, err
	}
	return s.repo.ListHolidays(ctx, span)
}

// CreateHoliday audits a holiday declared on a date that has already passed, for the same reason
// editing one does: it decides whether people were off or absent, and the reports have answered that.
func (s *Service) CreateHoliday(
	ctx context.Context, claims middleware.Claims, date time.Time, name string,
) (Holiday, error) {
	holiday, err := buildHoliday(claims, date, name)
	if err != nil {
		return Holiday{}, err
	}

	// The repository fills in the entity id, which only exists once the row does.
	var audit *AuditLog
	if isPast(holiday.Date, s.today()) {
		audit = auditRow(claims.UserID, actionHolidayChanged, entityHoliday, 0,
			nullJSON, holidayJSON(holiday.Date, holiday.Name))
	}
	if err := s.repo.CreateHoliday(ctx, &holiday, audit); err != nil {
		return Holiday{}, err
	}
	return holiday, nil
}

// ReplaceHoliday audits an edit that touches a date already past: such a holiday decides whether people
// were off or absent, and the reports have already said so.
func (s *Service) ReplaceHoliday(
	ctx context.Context, claims middleware.Claims, id int64, date time.Time, name string,
) (Holiday, error) {
	holiday, err := buildHoliday(claims, date, name)
	if err != nil {
		return Holiday{}, err
	}
	holiday.ID = id

	before, err := s.repo.HolidayByID(ctx, id)
	if err != nil {
		return Holiday{}, err
	}

	var audit *AuditLog
	if today := s.today(); isPast(before.Date, today) || isPast(holiday.Date, today) {
		audit = auditRow(claims.UserID, actionHolidayChanged, entityHoliday, id,
			holidayJSON(before.Date, before.Name), holidayJSON(holiday.Date, holiday.Name))
	}
	return s.repo.ReplaceHoliday(ctx, holiday, audit)
}

func (s *Service) DeleteHoliday(ctx context.Context, claims middleware.Claims, id int64) error {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return apperr.ErrForbidden
	}

	before, err := s.repo.HolidayByID(ctx, id)
	if err != nil {
		return err
	}

	var audit *AuditLog
	if isPast(before.Date, s.today()) {
		audit = auditRow(claims.UserID, actionHolidayDeleted, entityHoliday, id,
			holidayJSON(before.Date, before.Name), nullJSON)
	}
	return s.repo.DeleteHoliday(ctx, id, audit)
}

func (s *Service) ListLocations(ctx context.Context, claims middleware.Claims) ([]OfficeLocation, error) {
	if !claims.Role.AtLeast(middleware.RoleSupervisor) {
		return nil, apperr.ErrForbidden
	}
	return s.repo.ListLocations(ctx)
}

func (s *Service) CreateLocation(
	ctx context.Context, claims middleware.Claims, place Place,
) (OfficeLocation, error) {
	location, err := buildLocation(claims, place)
	if err != nil {
		return OfficeLocation{}, err
	}
	if err := s.repo.CreateLocation(ctx, &location); err != nil {
		return OfficeLocation{}, err
	}
	return location, nil
}

func (s *Service) ReplaceLocation(
	ctx context.Context, claims middleware.Claims, id int64, place Place,
) (OfficeLocation, error) {
	location, err := buildLocation(claims, place)
	if err != nil {
		return OfficeLocation{}, err
	}
	location.ID = id
	return s.repo.ReplaceLocation(ctx, location)
}

func (s *Service) DeleteLocation(ctx context.Context, claims middleware.Claims, id int64) error {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return apperr.ErrForbidden
	}
	return s.repo.DeleteLocation(ctx, id)
}

// ListAssignments is the roster as planned, which only the people who write it need to read.
func (s *Service) ListAssignments(
	ctx context.Context, claims middleware.Claims, span Range, userID *int64,
) ([]ShiftAssignment, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return nil, apperr.ErrForbidden
	}
	span, err := CheckRange(span)
	if err != nil {
		return nil, err
	}
	return s.repo.ListAssignments(ctx, span, userID)
}

// Assign writes a slice of roster, replacing whatever those people had on those dates. Rows landing on
// a past date are audited, since they change what the reports said.
func (s *Service) Assign(
	ctx context.Context, claims middleware.Claims, assignments []Assignment,
) ([]ShiftAssignment, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return nil, apperr.ErrForbidden
	}
	if err := checkAssignments(assignments); err != nil {
		return nil, err
	}

	rows := make([]ShiftAssignment, 0, len(assignments))
	for _, assignment := range assignments {
		rows = append(rows, ShiftAssignment{
			UserID:     assignment.UserID,
			WorkDate:   dateOnly(assignment.WorkDate),
			ScheduleID: assignment.ScheduleID,
			Note:       assignment.Note,
		})
	}

	// Only a past-dated row is worth auditing, and only then does the write pay for reading what it
	// replaces; the repository calls this back inside its transaction.
	today := s.today()
	var audits func([]ShiftAssignment) []AuditLog
	if slices.ContainsFunc(rows, func(row ShiftAssignment) bool { return isPast(row.WorkDate, today) }) {
		audits = func(before []ShiftAssignment) []AuditLog {
			return auditAssigned(claims.UserID, rows, before, today)
		}
	}

	if err := s.repo.UpsertAssignments(ctx, rows, audits); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) DeleteAssignment(ctx context.Context, claims middleware.Claims, id int64) error {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return apperr.ErrForbidden
	}

	before, err := s.repo.AssignmentByID(ctx, id)
	if err != nil {
		return err
	}

	var audit *AuditLog
	if isPast(before.WorkDate, s.today()) {
		audit = auditRow(claims.UserID, actionShiftDeleted, entityShift, before.UserID,
			assignmentJSON(before.WorkDate, before.ScheduleID), nullJSON)
	}
	return s.repo.DeleteAssignment(ctx, id, audit)
}

// auditAssigned builds the audit rows for the past-dated part of a bulk write, naming what each date
// held before so the entry says what the roster changed from.
func auditAssigned(actorID int64, rows, before []ShiftAssignment, today time.Time) []AuditLog {
	held := make(map[string]ShiftAssignment, len(before))
	for _, row := range before {
		held[slot(row.UserID, row.WorkDate)] = row
	}

	audits := make([]AuditLog, 0, len(rows))
	for _, row := range rows {
		if !isPast(row.WorkDate, today) {
			continue
		}

		was := nullJSON
		if previous, existed := held[slot(row.UserID, row.WorkDate)]; existed {
			was = assignmentJSON(previous.WorkDate, previous.ScheduleID)
		}
		audits = append(audits, *auditRow(actorID, actionShiftChanged, entityShift, row.UserID,
			was, assignmentJSON(row.WorkDate, row.ScheduleID)))
	}
	return audits
}

func buildSchedule(claims middleware.Claims, hours Hours) (WorkSchedule, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return WorkSchedule{}, apperr.ErrForbidden
	}

	name, err := checkName(hours.Name)
	if err != nil {
		return WorkSchedule{}, err
	}
	workdays, err := checkWorkdays(hours.Workdays)
	if err != nil {
		return WorkSchedule{}, err
	}
	start, err := ParseClock(hours.StartTime)
	if err != nil {
		return WorkSchedule{}, err
	}
	end, err := ParseClock(hours.EndTime)
	if err != nil {
		return WorkSchedule{}, err
	}
	if err := checkSchedule(start, end, hours.GraceMinutes, hours.BreakMinutes); err != nil {
		return WorkSchedule{}, err
	}

	return WorkSchedule{
		Name: name, StartTime: start, EndTime: end,
		GraceMinutes: hours.GraceMinutes, BreakMinutes: hours.BreakMinutes,
		Workdays: pqInt16Array(workdays), ObservesHolidays: hours.ObservesHolidays,
	}, nil
}

func buildHoliday(claims middleware.Claims, date time.Time, name string) (Holiday, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return Holiday{}, apperr.ErrForbidden
	}

	name, err := checkName(name)
	if err != nil {
		return Holiday{}, err
	}
	if date.IsZero() {
		return Holiday{}, apperr.Invalid("date is required")
	}
	return Holiday{Date: dateOnly(date), Name: name}, nil
}

func buildLocation(claims middleware.Claims, place Place) (OfficeLocation, error) {
	if !claims.Role.AtLeast(middleware.RoleHRAdmin) {
		return OfficeLocation{}, apperr.ErrForbidden
	}

	name, err := checkName(place.Name)
	if err != nil {
		return OfficeLocation{}, err
	}
	if err := checkLocation(place.Lat, place.Lng, place.RadiusM); err != nil {
		return OfficeLocation{}, err
	}
	return OfficeLocation{Name: name, Lat: place.Lat, Lng: place.Lng, RadiusM: place.RadiusM}, nil
}

func auditRow(actorID int64, action, entityType string, entityID int64, before, after string) *AuditLog {
	return &AuditLog{
		ActorID: actorID, Action: action, EntityType: entityType, EntityID: entityID,
		Before: []byte(before), After: []byte(after),
	}
}

func holidayJSON(date time.Time, name string) string {
	return fmt.Sprintf(`{"date":%q,"name":%q}`, key(date), name)
}

func assignmentJSON(date time.Time, scheduleID *int64) string {
	if scheduleID == nil {
		return fmt.Sprintf(`{"work_date":%q,"schedule_id":null}`, key(date))
	}
	return fmt.Sprintf(`{"work_date":%q,"schedule_id":%d}`, key(date), *scheduleID)
}

func lookup(schedules map[int64]WorkSchedule, id *int64) *WorkSchedule {
	if id == nil {
		return nil
	}
	schedule, ok := schedules[*id]
	if !ok {
		return nil
	}
	return &schedule
}

// dateOnly drops the time of day: a work date is a calendar day, never an instant.
func dateOnly(date time.Time) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
}

func key(date time.Time) string {
	return dateOnly(date).Format(time.DateOnly)
}

func slot(userID int64, date time.Time) string {
	return fmt.Sprintf("%d@%s", userID, key(date))
}

// today is the calendar date it is now in the company's zone.
func (s *Service) today() time.Time {
	return dateOnly(time.Now().In(s.zone))
}

// isPast reports whether the date is before today, which is what makes an edit worth auditing.
func isPast(date, today time.Time) bool {
	return dateOnly(date).Before(today)
}
