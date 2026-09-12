package upload

import "errors"

type Purpose string

const (
	AttendancePhoto Purpose = "attendance_photo"
	LeaveAttachment Purpose = "leave_attachment"
)

// rule is what one purpose accepts, and where its objects live.
type rule struct {
	prefix   string
	maxBytes int64
	types    map[string]string // allowed content type -> file extension
}

var rules = map[Purpose]rule{
	AttendancePhoto: {prefix: "attendance", maxBytes: 5 << 20, types: map[string]string{"image/jpeg": "jpg"}},
	LeaveAttachment: {prefix: "leave", maxBytes: 10 << 20, types: map[string]string{
		"image/jpeg": "jpg", "image/png": "png", "application/pdf": "pdf",
	}},
}

// sniffBytes is all http.DetectContentType looks at.
const sniffBytes = 512

// ErrForbidden means the key belongs to another user.
var ErrForbidden = errors.New("upload key belongs to another user")
