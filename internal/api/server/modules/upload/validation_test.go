package upload

import (
	"errors"
	"testing"
)

func TestRuleFor(t *testing.T) {
	if r, err := ruleFor(LeaveAttachment); err != nil || r.prefix != "leave" {
		t.Errorf("ruleFor(leave_attachment) = %+v, %v", r, err)
	}
	if _, err := ruleFor("avatar"); !isValidationError(err) {
		t.Errorf("unknown purpose: err = %v, want a ValidationError", err)
	}
}

func TestCheckType(t *testing.T) {
	photo, attachment := rules[AttendancePhoto], rules[LeaveAttachment]

	if ext, err := checkType(photo, AttendancePhoto, "image/jpeg"); err != nil || ext != "jpg" {
		t.Errorf("jpeg photo = %q, %v; want jpg", ext, err)
	}
	if ext, err := checkType(attachment, LeaveAttachment, "application/pdf"); err != nil || ext != "pdf" {
		t.Errorf("pdf attachment = %q, %v; want pdf", ext, err)
	}
	for _, contentType := range []string{"application/pdf", "image/png", "text/plain; charset=utf-8"} {
		if _, err := checkType(photo, AttendancePhoto, contentType); !isValidationError(err) {
			t.Errorf("%s as a photo: err = %v, want a ValidationError", contentType, err)
		}
	}
}

func TestCheckSize(t *testing.T) {
	photo := rules[AttendancePhoto]
	limit := photo.maxBytes
	for size, ok := range map[int64]bool{-1: false, 0: false, 1: true, limit: true, limit + 1: false} {
		if err := checkSize(photo, AttendancePhoto, size); (err == nil) != ok {
			t.Errorf("size %d: err = %v, want ok = %v", size, err, ok)
		}
	}
}

func TestCheckKey(t *testing.T) {
	photo := rules[AttendancePhoto]
	const name = "0f8fad5b-d9cb-469f-a165-70867728950e.jpg"

	tests := map[string]struct {
		key       string
		forbidden bool // someone else's; otherwise malformed
	}{
		"another user's":                     {"attendance/8/" + name, true},
		"a user whose id starts with theirs": {"attendance/70/" + name, true},
		"another purpose's prefix":           {"leave/7/" + name, true},
		"path traversal":                     {"attendance/7/../8/" + name, false},
		"not a uuid":                         {"attendance/7/selfie.jpg", false},
		"no extension":                       {"attendance/7/0f8fad5b-d9cb-469f-a165-70867728950e", false},
	}
	for label, test := range tests {
		t.Run(label, func(t *testing.T) {
			err := checkKey(photo, 7, test.key)
			if test.forbidden && !errors.Is(err, ErrForbidden) {
				t.Errorf("err = %v, want ErrForbidden", err)
			}
			if !test.forbidden && !isValidationError(err) {
				t.Errorf("err = %v, want a ValidationError", err)
			}
		})
	}

	if err := checkKey(photo, 7, "attendance/7/"+name); err != nil {
		t.Errorf("the owner's own key: err = %v", err)
	}
}
