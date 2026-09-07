package health

import (
	"context"
	"testing"

	healthcontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/health"
)

func TestGetHealth(t *testing.T) {
	response, err := NewHandler().GetHealth(context.Background(), healthcontract.GetHealthRequestObject{})
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	got, ok := response.(healthcontract.GetHealth200JSONResponse)
	if !ok {
		t.Fatalf("response = %T, want a 200", response)
	}
	if got.Condition != "Healthy" {
		t.Errorf("condition = %q, want %q", got.Condition, "Healthy")
	}
}
