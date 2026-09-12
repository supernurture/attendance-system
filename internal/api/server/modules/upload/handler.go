package upload

import (
	"context"
	"errors"

	uploadcontract "attendance-system/internal/api/server/oapicodegen/upload"
	"attendance-system/internal/middleware"
)

type Handler struct {
	svc *Service
}

// NewHandler serves the upload routes with svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

var _ uploadcontract.StrictServerInterface = (*Handler)(nil)

// CreateUploadIntent issues an upload URL under the caller's prefix; the route needs middleware.Auth.
func (h *Handler) CreateUploadIntent(
	ctx context.Context, req uploadcontract.CreateUploadIntentRequestObject,
) (uploadcontract.CreateUploadIntentResponseObject, error) {
	ctx = middleware.RequestContext(ctx)
	claims, ok := middleware.ClaimsFrom(ctx)
	if !ok {
		return nil, errors.New("upload intent reached without middleware.Auth")
	}

	intent, err := h.svc.Intent(ctx, claims.UserID, Purpose(req.Body.Purpose), req.Body.ContentType, req.Body.SizeBytes)
	if validationErr, ok := errors.AsType[*ValidationError](err); ok {
		return uploadcontract.CreateUploadIntent400JSONResponse{Message: validationErr.Error()}, nil
	}
	if err != nil {
		return nil, err
	}
	return uploadcontract.CreateUploadIntent201JSONResponse{
		Key: intent.Key, Url: intent.URL, Headers: intent.Headers, ExpiresAt: intent.ExpiresAt,
	}, nil
}
