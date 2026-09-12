package upload

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"attendance-system/internal/pkg/storage"
)

func TestIntentRejects(t *testing.T) {
	svc := NewService(nil) // refused before any storage call, so no object store is needed

	tests := map[string]struct {
		purpose     Purpose
		contentType string
		size        int64
	}{
		"50 MB photo":      {AttendancePhoto, "image/jpeg", 50 << 20},
		"50 MB attachment": {LeaveAttachment, "application/pdf", 50 << 20},
		"empty file":       {AttendancePhoto, "image/jpeg", 0},
		"pdf as a photo":   {AttendancePhoto, "application/pdf", 1000},
		"unsupported type": {LeaveAttachment, "application/zip", 1000},
		"unknown purpose":  {"avatar", "image/jpeg", 1000},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Intent(t.Context(), 1, test.purpose, test.contentType, test.size)
			if !isValidationError(err) {
				t.Errorf("err = %v, want a ValidationError", err)
			}
		})
	}
}

func TestIntentKeyIsUnderTheUsersPrefix(t *testing.T) {
	svc, _ := newTestService(t)

	intent, err := svc.Intent(t.Context(), 12, LeaveAttachment, "application/pdf", 1000)
	if err != nil {
		t.Fatalf("Intent: %v", err)
	}
	if !strings.HasPrefix(intent.Key, "leave/12/") || !strings.HasSuffix(intent.Key, ".pdf") {
		t.Errorf("key = %q, want leave/12/<uuid>.pdf", intent.Key)
	}
}

func TestIntentPassesPresignFailuresOn(t *testing.T) {
	boom := errors.New("signer broke")
	orig := presignPut
	presignPut = func(*storage.Storage, context.Context, string, string, int64) (storage.PresignedURL, error) {
		return storage.PresignedURL{}, boom
	}
	t.Cleanup(func() { presignPut = orig })

	if _, err := NewService(nil).Intent(t.Context(), 5, AttendancePhoto, "image/jpeg", 10); !errors.Is(err, boom) {
		t.Errorf("Intent err = %v, want the presign failure", err)
	}
	if rec := postIntent(t, NewService(nil), 10); rec.Code != http.StatusInternalServerError {
		t.Errorf("handler status = %d, want 500", rec.Code)
	}
}

func TestPutOfADifferentLengthIsRejectedByTheStore(t *testing.T) {
	svc, _ := newTestService(t)

	intent, err := svc.Intent(t.Context(), 1, AttendancePhoto, "image/jpeg", 100)
	if err != nil {
		t.Fatalf("Intent: %v", err)
	}
	if status := put(t, intent, jpeg); status < 400 {
		t.Errorf("PUT of %d bytes against a URL signed for 100 returned %d, want it refused", len(jpeg), status)
	}
}

func TestVerifiedUploadCannotBeReplaced(t *testing.T) {
	svc, store := newTestService(t)

	intent, err := svc.Intent(t.Context(), 7, AttendancePhoto, "image/jpeg", int64(len(jpeg)))
	if err != nil {
		t.Fatalf("Intent: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), intent.Key) })
	if status := put(t, intent, jpeg); status != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", status)
	}
	if _, err := svc.Verify(t.Context(), 7, AttendancePhoto, intent.Key); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Same length, same type, different picture: the swap a buddy-punching user would try.
	swapped := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("X"), len(jpeg)-4)...)
	if status := put(t, intent, swapped); status != http.StatusPreconditionFailed {
		t.Errorf("re-PUT after Verify = %d, want 412", status)
	}
	if head, _ := store.ReadPrefix(t.Context(), intent.Key, len(jpeg)); !bytes.Equal(head, jpeg) {
		t.Error("the verified object was replaced")
	}
}

func TestVerifyAcceptsTheOwnersUpload(t *testing.T) {
	svc, store := newTestService(t)
	key := upload(t, svc, 7, AttendancePhoto, "image/jpeg", jpeg)
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	contentType, err := svc.Verify(t.Context(), 7, AttendancePhoto, key)
	if err != nil || contentType != "image/jpeg" {
		t.Fatalf("Verify = %q, %v; want image/jpeg", contentType, err)
	}
}

func TestVerifyRefusesAnotherUsersKey(t *testing.T) {
	svc, store := newTestService(t)
	key := upload(t, svc, 7, AttendancePhoto, "image/jpeg", jpeg)
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	for _, userID := range []int64{8, 70} { // 70 guards against a bare "attendance/7" prefix match
		if _, err := svc.Verify(t.Context(), userID, AttendancePhoto, key); !errors.Is(err, ErrForbidden) {
			t.Errorf("user %d: err = %v, want ErrForbidden", userID, err)
		}
	}
	if _, err := store.Size(t.Context(), key); err != nil {
		t.Errorf("the owner's object must survive a foreign claim: %v", err)
	}
}

func TestVerifyDeletesContentThatIsNotWhatItClaims(t *testing.T) {
	svc, store := newTestService(t)

	// Declared and signed as image/jpeg, so the store accepts it; only sniffing catches it.
	key := upload(t, svc, 7, AttendancePhoto, "image/jpeg", pdf)

	if _, err := svc.Verify(t.Context(), 7, AttendancePhoto, key); !isValidationError(err) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	if _, err := store.Size(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("after rejection: Size err = %v, want ErrNotFound", err)
	}
}

func TestVerifyReturnsTheSniffedType(t *testing.T) {
	svc, store := newTestService(t)

	// A PDF declared as a JPEG is still a valid leave attachment; the caller stores what it really is.
	key := upload(t, svc, 7, LeaveAttachment, "image/jpeg", pdf)
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	contentType, err := svc.Verify(t.Context(), 7, LeaveAttachment, key)
	if err != nil || contentType != "application/pdf" {
		t.Errorf("Verify = %q, %v; want application/pdf", contentType, err)
	}
}

func TestVerifyRejectsKeysThatWereNeverUploaded(t *testing.T) {
	svc, _ := newTestService(t)

	tests := map[string]string{
		"never uploaded": "attendance/7/00000000-0000-0000-0000-000000000000.jpg",
		"path traversal": "attendance/7/../8/00000000-0000-0000-0000-000000000000.jpg",
		"not a uuid":     "attendance/7/selfie.jpg",
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Verify(t.Context(), 7, AttendancePhoto, key); !isValidationError(err) {
				t.Errorf("err = %v, want a ValidationError", err)
			}
		})
	}
}

func TestVerifyRejectsAnUnknownPurpose(t *testing.T) {
	if _, err := NewService(nil).Verify(t.Context(), 7, "avatar", faultKey); !isValidationError(err) {
		t.Errorf("err = %v, want a ValidationError", err)
	}
}

func TestVerifyOnStorageFaults(t *testing.T) {
	noContent := func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

	tests := map[string]struct {
		replies     replies
		validation  bool // the client is told why
		wantDeleted bool
	}{
		"size lookup fails": {
			replies: replies{},
		},
		"larger than the limit": {
			replies:     replies{http.MethodHead: sized(6 << 20), http.MethodDelete: noContent},
			validation:  true,
			wantDeleted: true,
		},
		"read fails": {
			replies: replies{http.MethodHead: sized(len(jpeg))},
		},
		"body cut short": {
			replies: replies{
				http.MethodHead: sized(len(jpeg)),
				http.MethodGet: func(w http.ResponseWriter) {
					w.Header().Set("Content-Length", "512")
					_, _ = w.Write(jpeg[:10]) // then the connection closes
				},
			},
		},
		"delete of refused content fails": {
			replies:     replies{http.MethodHead: sized(len(pdf)), http.MethodGet: body(pdf)},
			validation:  true,
			wantDeleted: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			svc, fake := newFaultyService(t, test.replies)

			_, err := svc.Verify(t.Context(), 7, AttendancePhoto, faultKey)
			if err == nil {
				t.Fatal("Verify succeeded, want an error")
			}
			if isValidationError(err) != test.validation {
				t.Errorf("err = %v; ValidationError = %v, want %v", err, !test.validation, test.validation)
			}
			if fake.deleted() != test.wantDeleted {
				t.Errorf("DELETE sent = %v, want %v", !test.wantDeleted, test.wantDeleted)
			}
		})
	}
}
