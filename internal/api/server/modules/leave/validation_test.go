package leave

import (
	"strings"
	"testing"
	"time"

	"attendance-system/internal/pkg/apperr"
)

func TestCheckFiling(t *testing.T) {
	valid := Filing{Type: " annual ", StartDate: time.Date(year, 3, 3, 9, 30, 0, 0, time.UTC),
		EndDate: date(time.March, 4), Reason: " away "}
	checked, err := checkFiling(valid)
	if err != nil || checked.Type != "annual" || checked.Reason != "away" || !checked.StartDate.Equal(date(time.March,
		3)) {
		t.Errorf("checkFiling = %+v, %v; want it trimmed to calendar days", checked, err)
	}

	broken := map[string]func(*Filing){
		"no type":          func(f *Filing) { f.Type = " " },
		"no reason":        func(f *Filing) { f.Reason = "" },
		"a long reason":    func(f *Filing) { f.Reason = strings.Repeat("x", maxReasonChars+1) },
		"an empty key":     func(f *Filing) { f.AttachmentKey = ptr("") },
		"no start":         func(f *Filing) { f.StartDate = time.Time{} },
		"no end":           func(f *Filing) { f.EndDate = time.Time{} },
		"ending too early": func(f *Filing) { f.EndDate = date(time.March, 2) },
		"a year and a day": func(f *Filing) { f.EndDate = f.StartDate.AddDate(1, 0, 1) },
	}
	for name, breakIt := range broken {
		filing := valid
		breakIt(&filing)
		if _, err := checkFiling(filing); !apperr.IsValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}

func TestCheckRules(t *testing.T) {
	annual := LeaveType{Code: "annual", QuotaDaysPerYear: ptr(12)}
	marriage := LeaveType{Code: "marriage", MaxWorkingDaysPerRequest: ptr(3), AttachmentRequiredFromDays: ptr(1)}
	sick := LeaveType{Code: "sick", AttachmentRequiredFromDays: ptr(2)}
	march := Filing{StartDate: date(time.March, 3), EndDate: date(time.March, 7)}
	attached := march
	attached.AttachmentKey = ptr("leave/1/x.pdf")
	newYear := Filing{StartDate: date(time.December, 31), EndDate: time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)}
	lastYear := Filing{StartDate: march.StartDate.AddDate(-1, 0, 0), EndDate: march.EndDate.AddDate(-1, 0, 0)}

	tests := []struct {
		name    string
		kind    LeaveType
		filing  Filing
		working int
		ok      bool
	}{
		{"annual within a year", annual, march, 5, true},
		{"no working days", annual, march, 0, false},
		{"annual across a new year", annual, newYear, 2, false},
		{"annual in a year already past", annual, lastYear, 5, false},
		{"sick in a year already past", sick, lastYear, 1, true},
		{"sick across a new year", sick, newYear, 1, true},
		{"marriage at its cap", marriage, attached, 3, true},
		{"marriage past its cap", marriage, attached, 4, false},
		{"marriage without a note", marriage, march, 1, false},
		{"a day sick without a note", sick, march, 1, true},
		{"two days sick without a note", sick, march, 2, false},
	}
	for _, test := range tests {
		if err := checkRules(test.kind, test.filing, test.working, year); (err == nil) != test.ok {
			t.Errorf("%s: err = %v, want ok = %v", test.name, err, test.ok)
		}
	}
}

func TestCheckType(t *testing.T) {
	valid := LeaveType{Code: " study_2 ", Name: " Study ", QuotaDaysPerYear: ptr(0), MaxWorkingDaysPerRequest: ptr(366),
		AttachmentRequiredFromDays: ptr(1)}
	if checked, err := checkType(valid); err != nil || checked.Code != "study_2" || checked.Name != "Study" {
		t.Errorf("checkType = %+v, %v", checked, err)
	}

	broken := map[string]func(*LeaveType){
		"an uppercase code":     func(k *LeaveType) { k.Code = "Study" },
		"a code led by a digit": func(k *LeaveType) { k.Code = "2study" },
		"a long code":           func(k *LeaveType) { k.Code = strings.Repeat("s", maxCodeChars+1) },
		"no name":               func(k *LeaveType) { k.Name = "" },
		"a long name":           func(k *LeaveType) { k.Name = strings.Repeat("n", maxNameChars+1) },
		"a negative quota":      func(k *LeaveType) { k.QuotaDaysPerYear = ptr(-1) },
		"a zero cap":            func(k *LeaveType) { k.MaxWorkingDaysPerRequest = ptr(0) },
		"a note past a year":    func(k *LeaveType) { k.AttachmentRequiredFromDays = ptr(367) },
	}
	for name, breakIt := range broken {
		kind := valid
		breakIt(&kind)
		if _, err := checkType(kind); !apperr.IsValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
}

func TestCheckQuotaAndNote(t *testing.T) {
	if err := checkQuota(Quota{Year: year, QuotaDays: 366, CarriedOverDays: 0}); err != nil {
		t.Errorf("a full quota: %v", err)
	}
	for name, quota := range map[string]Quota{
		"year 10000":            {Year: 10000},
		"a quota past a year":   {Year: year, QuotaDays: 367},
		"negative carried days": {Year: year, CarriedOverDays: -1},
	} {
		if err := checkQuota(quota); !apperr.IsValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}

	if checkNote(strings.Repeat("n", maxReasonChars)) != nil || !apperr.IsValidation(checkNote(strings.Repeat("n",
		maxReasonChars+1))) {
		t.Error("checkNote must take exactly the column's 500 characters")
	}
}
