package upload

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	uploadcontract "attendance-system/internal/api/server/oapicodegen/upload"
)

func TestCreateUploadIntentAnswers201WithTheUploadDetails(t *testing.T) {
	svc, _ := newTestService(t)

	rec := postIntent(t, svc, 1000)
	var got uploadcontract.UploadIntent
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("got %d %s, want 201 with an intent", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(got.Key, "attendance/5/") || got.Url == "" || got.ExpiresAt.Before(time.Now()) {
		t.Errorf("intent = %+v, want a key under attendance/5/ and a live URL", got)
	}
	if got.Headers["If-None-Match"] != "*" || got.Headers["Content-Type"] != "image/jpeg" {
		t.Errorf("headers = %v, want the signed Content-Type and If-None-Match", got.Headers)
	}
}

func TestCreateUploadIntentAnswers400ForARefusedRequest(t *testing.T) {
	svc, _ := newTestService(t)

	if rec := postIntent(t, svc, 50<<20); rec.Code != http.StatusBadRequest {
		t.Errorf("50 MB: status = %d, want 400", rec.Code)
	}
}

func TestCreateUploadIntentWithoutClaimsFails(t *testing.T) {
	req := uploadcontract.CreateUploadIntentRequestObject{Body: &uploadcontract.UploadIntentRequest{
		Purpose: uploadcontract.AttendancePhoto, ContentType: "image/jpeg", SizeBytes: 10,
	}}

	_, err := NewHandler(NewService(nil)).CreateUploadIntent(t.Context(), req)
	if err == nil {
		t.Error("an unguarded route issued an upload URL; want an error")
	}
}
