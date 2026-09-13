package attendance

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

func TestCreateRejectsAReusedPhotoKey(t *testing.T) {
	s := newServer(t)
	first := s.person(t, middleware.RoleEmployee, nil, nil)
	second := s.person(t, middleware.RoleEmployee, nil, nil)
	used := s.checkedIn(t, first.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), nil)

	// Straight to the repository, past the service's own lookup: the column's UNIQUE is what holds.
	reused := used
	reused.ID, reused.UserID = 0, second.UserID
	if err := s.repo.Create(t.Context(), &reused); !errors.Is(err, errPhotoKeyUsed) {
		t.Errorf("a photo_key already backing a check-in: err = %v, want errPhotoKeyUsed", err)
	}

	sameDay := Attendance{UserID: first.UserID, WorkDate: date(2030, 3, 4), CheckInAt: local(2030, 3, 4, 9, 0)}
	if err := s.repo.Create(t.Context(), &sameDay); !errors.Is(err, errCheckedIn) {
		t.Errorf("a second attendance on one work date: err = %v, want errCheckedIn", err)
	}

	// A violation nothing maps is passed on as it is.
	backwards := Attendance{UserID: second.UserID, WorkDate: date(2030, 3, 4), CheckInAt: local(2030, 3, 4, 9, 0),
		CheckOutAt: ptr(local(2030, 3, 4, 8, 0))}
	if err := s.repo.Create(t.Context(), &backwards); !isPgError(err, "23514") {
		t.Errorf("a check-out before the check-in: err = %v, want the check_violation itself", err)
	}
}

func TestTranslatePassesOtherErrorsOn(t *testing.T) {
	if err := translate(errInjected); !errors.Is(err, errInjected) {
		t.Errorf("translate = %v, want the error unchanged", err)
	}
}

func TestCheckOutLosesARace(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	row := s.checkedIn(t, employee.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), nil)

	row.CheckOutAt = ptr(local(2030, 3, 4, 17, 0))
	if err := s.repo.CheckOut(t.Context(), row); err != nil {
		t.Fatalf("first CheckOut: %v", err)
	}
	if err := s.repo.CheckOut(t.Context(), row); !errors.Is(err, errNoOpenCheckIn) {
		t.Errorf("a check-out that lost the race: err = %v, want errNoOpenCheckIn", err)
	}
}

func TestApproveRepository(t *testing.T) {
	s := newServer(t)
	employee := s.person(t, middleware.RoleEmployee, nil, nil)
	ctx := t.Context()
	keep := func(_ *gorm.DB, correction Correction, current *Attendance) (Attendance, error) {
		return corrected(current, correction)
	}
	decided := func() map[string]any { return map[string]any{"status": correctionApproved} }

	// Settled already, which only a decision racing this one could cause.
	cancelled := s.fileCorrection(t, employee.UserID, date(2030, 3, 1), ptr(local(2030, 3, 1, 8, 0)), nil)
	if _, err := s.repo.Settle(ctx, cancelled.ID, map[string]any{"status": correctionCancelled}); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if _, err := s.repo.Approve(ctx, cancelled.ID, decided(), keep); !errors.Is(err, errNotPending) {
		t.Errorf("approving a cancelled correction: err = %v, want errNotPending", err)
	}

	// A check-in lands after the lock found no attendance: the insert conflicts and nothing is approved.
	racing := s.fileCorrection(t, employee.UserID, date(2030, 3, 2), ptr(local(2030, 3, 2, 8, 0)), nil)
	_, err := s.repo.Approve(ctx, racing.ID, decided(), func(_ *gorm.DB, correction Correction,
		current *Attendance) (Attendance, error) {
		s.checkedIn(t, employee.UserID, correction.WorkDate, local(2030, 3, 2, 8, 5), nil)
		return corrected(current, correction)
	})
	if !errors.Is(err, errCheckedIn) {
		t.Errorf("approving into a day that was just checked in: err = %v, want errCheckedIn", err)
	}

	s.checkedIn(t, employee.UserID, date(2030, 3, 3), local(2030, 3, 3, 8, 0), nil)
	held := s.fileCorrection(t, employee.UserID, date(2030, 3, 3), nil, ptr(local(2030, 3, 3, 17, 0)))
	faults := map[string]string{
		"locking the correction": "FOR UPDATE",
		"locking the attendance": `"attendances" WHERE user_id`,
		"writing the attendance": `UPDATE "attendances"`,
	}
	for name, match := range faults {
		repo := NewRepository(failingAfterDB(t, match), s.store)
		if _, err := repo.Approve(ctx, held.ID, decided(), keep); !errors.Is(err, errInjected) {
			t.Errorf("%s failing: err = %v, want the injected failure", name, err)
		}
	}

	approved, err := s.repo.Approve(ctx, held.ID, decided(), keep)
	if err != nil || approved.Status != correctionApproved || approved.OldCheckOutAt != nil ||
		!approved.OldCheckInAt.Equal(local(2030, 3, 3, 8, 0)) {
		t.Errorf("Approve = %+v, %v", approved, err)
	}
	if _, err := s.repo.CorrectionByID(ctx, 0); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("CorrectionByID(0) = %v, want ErrNotFound", err)
	}
}

func TestOnDateAndPeople(t *testing.T) {
	s := newServer(t)
	lead := s.person(t, middleware.RoleSupervisor, nil, nil)
	employee := s.person(t, middleware.RoleEmployee, &lead.UserID, nil)
	s.checkedIn(t, employee.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), nil)

	s.checkedIn(t, lead.UserID, date(2030, 3, 4), local(2030, 3, 4, 8, 0), nil)

	rows, err := s.repo.OnDate(t.Context(), &lead.UserID, date(2030, 3, 4))
	if err != nil || len(rows) != 1 || rows[0].UserID != employee.UserID {
		t.Errorf("OnDate below the lead = %+v, %v; want only their report, not the lead", rows, err)
	}
	everyone, err := s.repo.OnDate(t.Context(), nil, date(2030, 3, 4))
	if err != nil || !attends(everyone, employee.UserID) || !attends(everyone, lead.UserID) {
		t.Errorf("OnDate for everyone = %+v, %v; want both", everyone, err)
	}

	people, err := s.repo.People(t.Context(), &lead.UserID, date(2030, 3, 4))
	if err != nil || len(people) != 1 || people[0].ID != employee.UserID || people[0].FullName == "" {
		t.Errorf("People below the lead = %+v, %v", people, err)
	}
}

func isPgError(err error, code string) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == code
}

func attends(rows []Attendance, userID int64) bool {
	for _, row := range rows {
		if row.UserID == userID {
			return true
		}
	}
	return false
}
