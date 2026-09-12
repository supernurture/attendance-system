package auth

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestLoginRefusesBadCredentials(t *testing.T) {
	s := newServer(t)
	active := s.createUser(t, true)
	inactive := s.createUser(t, false)

	tests := map[string][2]string{
		"wrong password":   {active.Email, "wrong password"},
		"unknown email":    {"nobody@test.local", password},
		"inactive account": {inactive.Email, password},
	}
	for name, creds := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := s.svc.Login(t.Context(), creds[0], creds[1], "192.0.2.1")
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Errorf("err = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}

func TestLoginLimitsAttemptsPerIP(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	for attempt := 1; attempt <= maxAttemptsPerIP; attempt++ {
		_, err := s.svc.Login(t.Context(), user.Email, "wrong", "192.0.2.1")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: err = %v, want the password checked", attempt, err)
		}
	}
	if _, err := s.svc.Login(t.Context(), user.Email, password, "192.0.2.1"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("the right password while locked: err = %v, want ErrTooManyAttempts", err)
	}

	s.signIn(t, user.Email, "198.51.100.7") // another IP is unaffected
	s.redis.FastForward(ipWindow)
	s.signIn(t, user.Email, "192.0.2.1") // and the lock lifts with the window
}

func TestLoginLimitHoldsUnderConcurrentAttempts(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	checked := make(chan bool, 3*maxAttemptsPerIP)
	var wg sync.WaitGroup
	for range cap(checked) {
		wg.Go(func() {
			_, err := s.svc.Login(t.Context(), user.Email, "wrong", "192.0.2.1")
			checked <- errors.Is(err, ErrInvalidCredentials)
		})
	}
	wg.Wait()
	close(checked)

	guessed := 0
	for wasChecked := range checked {
		if wasChecked {
			guessed++
		}
	}
	if guessed != maxAttemptsPerIP {
		t.Errorf("%d concurrent attempts reached %d password checks, want %d", cap(checked), guessed, maxAttemptsPerIP)
	}
}

func TestSuccessfulLoginResetsThePerIPCount(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	for range maxAttemptsPerIP - 1 {
		_, _ = s.svc.Login(t.Context(), user.Email, "wrong", "192.0.2.1")
	}
	s.signIn(t, user.Email, "192.0.2.1")
	for range maxAttemptsPerIP - 1 {
		_, _ = s.svc.Login(t.Context(), user.Email, "wrong", "192.0.2.1")
	}

	s.signIn(t, user.Email, "192.0.2.1") // failures before a success must not count
}

func TestLoginCapsAttemptsPerEmailAcrossIPs(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	for i := range maxAttemptsPerEmail / maxAttemptsPerIP {
		for range maxAttemptsPerIP {
			_, _ = s.svc.Login(t.Context(), user.Email, "wrong", fmt.Sprintf("203.0.113.%d", i))
		}
	}

	_, err := s.svc.Login(t.Context(), user.Email, password, "198.51.100.1")
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("from a fresh IP after %d guesses: err = %v, want ErrTooManyAttempts", maxAttemptsPerEmail, err)
	}
	s.redis.FastForward(emailWindow)
	s.signIn(t, user.Email, "198.51.100.1")
}

func TestLockedIPCannotSpendTheEmailBudget(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	// One attacker IP hammering far past its own limit must not lock the account for everyone.
	for range maxAttemptsPerEmail + maxAttemptsPerIP {
		_, _ = s.svc.Login(t.Context(), user.Email, "wrong", "192.0.2.1")
	}

	s.signIn(t, user.Email, "198.51.100.1")
}

func TestRefreshRotatesTheToken(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)
	first := s.signIn(t, user.Email, "192.0.2.1")

	second, err := s.svc.Refresh(t.Context(), first.RefreshToken)
	if err != nil || second.RefreshToken == first.RefreshToken {
		t.Fatalf("Refresh = %+v, %v; want a new refresh token", second, err)
	}
	if _, err := s.svc.Refresh(t.Context(), first.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("reusing the rotated token: err = %v, want ErrInvalidToken", err)
	}
	// Within the grace, as a client retrying a lost response would be: the new session must survive.
	if _, err := s.svc.Refresh(t.Context(), second.RefreshToken); err != nil {
		t.Errorf("the rotated session: err = %v, want it still usable", err)
	}
}

func TestRefreshRefusesTokensItShould(t *testing.T) {
	s := newServer(t)

	loggedOut := s.createUser(t, true)
	pair := s.signIn(t, loggedOut.Email, "192.0.2.1")
	if err := s.svc.Logout(t.Context(), pair.RefreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	expired := s.createUser(t, true)
	expiredPair := s.signIn(t, expired.Email, "192.0.2.1")
	s.db.Exec("UPDATE refresh_tokens SET expires_at = now() - interval '1 second' WHERE user_id = ?", expired.ID)

	deactivated := s.createUser(t, true)
	deactivatedPair := s.signIn(t, deactivated.Email, "192.0.2.1")
	s.db.Exec("UPDATE users SET is_active = false WHERE id = ?", deactivated.ID)

	tests := map[string]string{
		"logged out":       pair.RefreshToken,
		"expired":          expiredPair.RefreshToken,
		"deactivated user": deactivatedPair.RefreshToken,
		"never issued":     "never-issued",
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := s.svc.Refresh(t.Context(), token); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestSeedAdmin(t *testing.T) {
	s := newServer(t)
	email := fmt.Sprintf("admin-%d@test.local", time.Now().UnixNano())
	t.Cleanup(func() { s.deleteUser(email) })

	if err := s.svc.SeedAdmin(t.Context(), strings.ToUpper(email), password); err != nil {
		t.Fatalf("SeedAdmin: %v", err)
	}
	if err := s.svc.SeedAdmin(t.Context(), email, "a different password"); err != nil {
		t.Fatalf("SeedAdmin again: %v", err)
	}

	pair := s.signIn(t, email, "192.0.2.1") // the second seed must not have reset the password
	if rec := s.get(t, "/me", pair.AccessToken); !strings.HasSuffix(rec.Body.String(), "|super_admin") {
		t.Errorf("GET /me = %q, want the super_admin role", rec.Body)
	}

	for _, bad := range [][2]string{{"not-an-email", password}, {email, "short"}, {email, strings.Repeat("x", 73)}} {
		if err := s.svc.SeedAdmin(t.Context(), bad[0], bad[1]); err == nil {
			t.Errorf("SeedAdmin(%q, %d-byte password) = nil, want an error", bad[0], len(bad[1]))
		}
	}
}

func TestDatabaseFailuresSurface(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)
	session := s.signIn(t, user.Email, "192.0.2.1")

	login := func(svc *Service) error {
		_, err := svc.Login(t.Context(), user.Email, password, "192.0.2.9")
		return err
	}
	refresh := func(token string) func(*Service) error {
		return func(svc *Service) error { _, err := svc.Refresh(t.Context(), token); return err }
	}
	seed := func(svc *Service) error { return svc.SeedAdmin(t.Context(), "seed-fault@test.local", password) }

	const rotate = "rotate refresh token"
	tests := map[string]struct {
		match string
		call  func(*Service) error
		want  string
	}{
		"login, finding the user":            {"users", login, "find user"},
		"login, storing the refresh token":   {"DELETE FROM refresh_tokens", login, "store refresh token"},
		"refresh, revoking the old token":    {"RETURNING user_id", refresh(session.RefreshToken), rotate},
		"refresh, loading its owner":         {"users", refresh(session.RefreshToken), rotate},
		"refresh, checking an unknown token": {"make_interval", refresh("never-issued"), rotate},
		"seeding the admin":                  {"users", seed, "seed admin"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := test.call(s.failingService(t, test.match))
			if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), test.want) {
				t.Errorf("err = %v, want the injected failure wrapped as %q", err, test.want)
			}
		})
	}

	// Every failure above rolled back: the session still refreshes.
	if _, err := s.svc.Refresh(t.Context(), session.RefreshToken); err != nil {
		t.Errorf("Refresh after the rolled-back failures: %v", err)
	}
}

func TestRedisFailuresSurface(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)

	down := miniredis.RunT(t)
	addr := down.Addr()
	down.Close()
	svc := NewService(s.db, goredis.NewClient(&goredis.Options{Addr: addr, MaxRetries: -1}), testSecret)
	if _, err := svc.Login(t.Context(), user.Email, password, "192.0.2.9"); err == nil ||
		!strings.Contains(err.Error(), "count login attempt") {
		t.Errorf("Redis down: err = %v, want the attempt count to fail the login", err)
	}

	client := goredis.NewClient(&goredis.Options{Addr: s.redis.Addr()})
	client.AddHook(failDel{})
	svc = NewService(s.db, client, testSecret)
	if _, err := svc.Login(t.Context(), user.Email, password, "192.0.2.9"); !errors.Is(err, errInjected) {
		t.Errorf("DEL failing: err = %v, want the reset to fail the login", err)
	}
}

func TestDeletedUserLosesAccess(t *testing.T) {
	s := newServer(t)
	user := s.createUser(t, true)
	pair := s.signIn(t, user.Email, "192.0.2.1")

	// What the user module's delete does: the row stays, deleted_at is set.
	s.db.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", user.ID)

	if _, err := s.svc.Refresh(t.Context(), pair.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("refresh for a deleted account: err = %v, want ErrInvalidToken", err)
	}
	_, err := s.svc.Login(t.Context(), user.Email, password, "198.51.100.9")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("login for a deleted account: err = %v, want ErrInvalidCredentials", err)
	}
}
