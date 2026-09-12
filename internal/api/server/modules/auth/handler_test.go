package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	authcontract "attendance-system/internal/api/server/oapicodegen/auth"
)

func TestLoginAnswers200WithAUsableTokenPair(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	// Email is matched case-insensitively.
	pair := tokens(t, s.login(t, "  "+strings.ToUpper(user.Email), password))
	if pair.TokenType != "Bearer" || pair.ExpiresIn != 900 || pair.RefreshToken == "" {
		t.Errorf("token pair = %+v, want a Bearer pair expiring in 900s", pair)
	}

	rec := s.get(t, "/me", pair.AccessToken)
	if want := fmt.Sprintf("%d|employee", user.ID); rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("GET /me = %d %q, want 200 %q", rec.Code, rec.Body, want)
	}
}

func TestLoginAnswers401WithAGenericMessage(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	rec := s.login(t, user.Email, "wrong password")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), ErrInvalidCredentials.Error()) {
		t.Errorf("got %d %s, want 401 with the generic message", rec.Code, rec.Body)
	}
}

func TestLoginAnswers429OnceLockedOut(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	for attempt := 1; attempt <= maxAttemptsPerIP; attempt++ {
		if rec := s.login(t, user.Email, "wrong password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", attempt, rec.Code)
		}
	}
	if rec := s.login(t, user.Email, "wrong password"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("attempt 11: status = %d, want 429", rec.Code)
	}
}

func TestRefreshAnswers200AndRefusesTheOldTokenWith401(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)
	first := tokens(t, s.login(t, user.Email, password))

	second := tokens(t, s.refresh(t, first.RefreshToken))
	rec := s.refresh(t, first.RefreshToken)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), ErrInvalidToken.Error()) {
		t.Errorf("reusing the rotated token: got %d %s, want 401 with the generic message", rec.Code, rec.Body)
	}
	if s.get(t, "/me", second.AccessToken).Code != http.StatusOK {
		t.Error("the access token from a refresh does not work")
	}
}

func TestLogoutAlwaysAnswers204(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)
	pair := tokens(t, s.login(t, user.Email, password))

	for _, token := range []string{pair.RefreshToken, pair.RefreshToken, "never-issued"} {
		rec := s.post(t, "/auth/logout", "192.0.2.1", authcontract.RefreshRequest{RefreshToken: token})
		if rec.Code != http.StatusNoContent {
			t.Errorf("logout %q: status = %d, want 204", token, rec.Code)
		}
	}
}

func TestHandlerPassesUnexpectedErrorsOn(t *testing.T) {
	h := NewHandler(newServer(t).failingService(t, "")) // every statement fails: the database is down

	// A plain context, not a *gin.Context: the handlers must not depend on gin to work.
	ctx := context.Background()
	if _, err := h.Login(ctx, authcontract.LoginRequestObject{
		Body: &authcontract.LoginRequest{Email: "a@test.local", Password: password},
	}); !errors.Is(err, errInjected) {
		t.Errorf("Login err = %v, want the database failure", err)
	}
	if _, err := h.Refresh(ctx, authcontract.RefreshRequestObject{
		Body: &authcontract.RefreshRequest{RefreshToken: "x"},
	}); !errors.Is(err, errInjected) {
		t.Errorf("Refresh err = %v, want the database failure", err)
	}
	if _, err := h.Logout(ctx, authcontract.LogoutRequestObject{
		Body: &authcontract.RefreshRequest{RefreshToken: "x"},
	}); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "revoke refresh token") {
		t.Errorf("Logout err = %v, want the database failure", err)
	}
}
