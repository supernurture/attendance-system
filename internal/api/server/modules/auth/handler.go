package auth

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"

	authcontract "attendance-system/internal/api/server/oapicodegen/auth"
	"attendance-system/internal/middleware"
)

type Handler struct {
	svc *Service
}

// NewHandler serves the auth routes with svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

var _ authcontract.StrictServerInterface = (*Handler)(nil)

// Login answers 401 for bad credentials and 429 once the caller is locked out.
func (h *Handler) Login(
	ctx context.Context, req authcontract.LoginRequestObject,
) (authcontract.LoginResponseObject, error) {
	pair, err := h.svc.Login(middleware.RequestContext(ctx), req.Body.Email, req.Body.Password, clientIP(ctx))
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return authcontract.Login401JSONResponse{Message: err.Error()}, nil
	case errors.Is(err, ErrTooManyAttempts):
		return authcontract.Login429JSONResponse{Message: err.Error()}, nil
	case err != nil:
		return nil, err
	}
	return authcontract.Login200JSONResponse(tokenPair(pair)), nil
}

// Refresh answers one generic 401 for every refused token and logs the real reason.
func (h *Handler) Refresh(
	ctx context.Context, req authcontract.RefreshRequestObject,
) (authcontract.RefreshResponseObject, error) {
	pair, err := h.svc.Refresh(middleware.RequestContext(ctx), req.Body.RefreshToken)
	switch {
	case errors.Is(err, ErrInvalidToken):
		if c, ok := ctx.(*gin.Context); ok {
			_ = c.Error(err) // the access log keeps the reason: a reuse means a refresh token leaked
		}
		return authcontract.Refresh401JSONResponse{Message: ErrInvalidToken.Error()}, nil
	case err != nil:
		return nil, err
	}
	return authcontract.Refresh200JSONResponse(tokenPair(pair)), nil
}

// Logout answers 204 even for an unknown or already revoked token.
func (h *Handler) Logout(
	ctx context.Context, req authcontract.LogoutRequestObject,
) (authcontract.LogoutResponseObject, error) {
	if err := h.svc.Logout(middleware.RequestContext(ctx), req.Body.RefreshToken); err != nil {
		return nil, err
	}
	return authcontract.Logout204Response{}, nil
}

func tokenPair(pair TokenPair) authcontract.TokenPair {
	return authcontract.TokenPair{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(AccessTTL.Seconds()),
	}
}

func clientIP(ctx context.Context) string {
	if c, ok := ctx.(*gin.Context); ok {
		return c.ClientIP()
	}
	return ""
}
