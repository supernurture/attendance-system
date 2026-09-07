package health

import (
	"context"

	healthcontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/health"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

var _ healthcontract.StrictServerInterface = (*Handler)(nil)

func (h *Handler) GetHealth(
	_ context.Context, _ healthcontract.GetHealthRequestObject) (healthcontract.GetHealthResponseObject, error) {
	return healthcontract.GetHealth200JSONResponse{Condition: "Healthy"}, nil
}
