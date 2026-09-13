package attendance

import (
	"strings"
	"testing"
	"time"

	"attendance-system/internal/pkg/apperr"
)

func TestCheckMark(t *testing.T) {
	good := Mark{PhotoKey: "attendance/1/x.jpg", Lat: -6.2, Lng: 106.8, AccuracyM: 0}
	if err := checkMark(good); err != nil {
		t.Errorf("a good mark: err = %v", err)
	}

	tests := map[string]func(*Mark){
		"no photo":          func(m *Mark) { m.PhotoKey = "" },
		"lat past a pole":   func(m *Mark) { m.Lat = 90.1 },
		"lat below a pole":  func(m *Mark) { m.Lat = -90.1 },
		"lng past 180":      func(m *Mark) { m.Lng = 180.1 },
		"lng below -180":    func(m *Mark) { m.Lng = -180.1 },
		"negative accuracy": func(m *Mark) { m.AccuracyM = -1 },
	}
	for name, spoil := range tests {
		mark := good
		spoil(&mark)
		if err := checkMark(mark); !apperr.IsValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}

func TestCheckReport(t *testing.T) {
	content, err := checkReport(date(2030, time.March, 4), "  fixed the rack  ")
	if err != nil || content != "fixed the rack" {
		t.Errorf("checkReport = %q, %v; want it trimmed", content, err)
	}
	// Characters, not bytes: 5000 Indonesian letters with diacritics still fit.
	if _, err := checkReport(date(2030, time.March, 4), strings.Repeat("é", maxReportChars)); err != nil {
		t.Errorf("5000 characters: err = %v", err)
	}

	tests := map[string]struct {
		workDate time.Time
		content  string
	}{
		"no work date":  {time.Time{}, "done"},
		"blank content": {date(2030, time.March, 4), "   "},
		"too long":      {date(2030, time.March, 4), strings.Repeat("a", maxReportChars+1)},
	}
	for name, test := range tests {
		if _, err := checkReport(test.workDate, test.content); !apperr.IsValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}

func TestCheckNote(t *testing.T) {
	if err := checkNote(strings.Repeat("a", maxReasonChars)); err != nil {
		t.Errorf("500 characters: err = %v", err)
	}
	if err := checkNote(strings.Repeat("a", maxReasonChars+1)); !apperr.IsValidation(err) {
		t.Errorf("501 characters: err = %v, want a validation error", err)
	}
}

func TestCheckProposal(t *testing.T) {
	current := local(2030, time.March, 10, 12, 0)
	in, out := local(2030, time.March, 4, 22, 0), local(2030, time.March, 5, 6, 0) // a night shift

	proposal, err := checkProposal(Proposal{
		WorkDate: time.Date(2030, time.March, 4, 15, 0, 0, 0, time.UTC), CheckInAt: &in, CheckOutAt: &out,
		Reason: " forgot to check out ",
	}, current, jakarta)
	if err != nil {
		t.Fatalf("a night shift's times: err = %v", err)
	}
	if proposal.Reason != "forgot to check out" || !proposal.WorkDate.Equal(date(2030, time.March, 4)) {
		t.Errorf("proposal = %+v, want the reason trimmed and the date at midnight", proposal)
	}

	tooEarly := local(2030, time.March, 3, 11, 59) // 12 hours and a minute before the date starts
	tooLate := local(2030, time.March, 5, 12, 1)   // 12 hours and a minute after it ends
	future := local(2030, time.March, 10, 12, 1)
	tests := map[string]Proposal{
		"no work date":    {CheckInAt: &in, Reason: "x"},
		"no time":         {WorkDate: date(2030, time.March, 4), Reason: "x"},
		"no reason":       {WorkDate: date(2030, time.March, 4), CheckInAt: &in, Reason: " "},
		"reason too long": {WorkDate: date(2030, time.March, 4), CheckInAt: &in, Reason: strings.Repeat("a", 501)},
		"before the date": {WorkDate: date(2030, time.March, 4), CheckInAt: &tooEarly, Reason: "x"},
		"after the date":  {WorkDate: date(2030, time.March, 4), CheckOutAt: &tooLate, Reason: "x"},
		"in the future":   {WorkDate: date(2030, time.March, 10), CheckOutAt: &future, Reason: "x"},
		// Tomorrow's date with a time already past today, inside the slack: approving it would take tomorrow.
		"a work date not come yet": {WorkDate: date(2030, time.March, 11), CheckInAt: ptr(local(2030, 3, 10, 11, 0)),
			Reason: "x"},
	}
	for name, proposal := range tests {
		if _, err := checkProposal(proposal, current, jakarta); !apperr.IsValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}
