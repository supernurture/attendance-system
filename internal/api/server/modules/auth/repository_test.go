package auth

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// issue stores a refresh token for the user, as Login does.
func issue(t *testing.T, repo *Repository, userID int64, expiresAt time.Time) (token, hash string) {
	t.Helper()

	token, hash = newRefreshToken()
	if err := repo.CreateRefreshToken(t.Context(), RefreshToken{
		UserID: userID, TokenHash: hash, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("CreateRefreshToken: %v", err)
	}
	return token, hash
}

func TestUserByEmailReportsAMiss(t *testing.T) {
	repo := NewRepository(newServer(t).db)

	if _, err := repo.UserByEmail(t.Context(), "nobody@test.local"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("err = %v, want gorm.ErrRecordNotFound", err)
	}
}

func TestCreateUserIfAbsentLeavesAnExistingUser(t *testing.T) {
	s := newServer(t)
	repo := NewRepository(s.db)
	user := s.createUser(t, true)

	again := User{Email: user.Email, PasswordHash: "another hash", FullName: "Impostor", Role: "super_admin"}
	if err := repo.CreateUserIfAbsent(t.Context(), again); err != nil {
		t.Fatalf("CreateUserIfAbsent: %v", err)
	}

	stored, err := repo.UserByEmail(t.Context(), user.Email)
	if err != nil || stored.PasswordHash != user.PasswordHash || stored.Role != "employee" {
		t.Errorf("stored = %+v, %v; want the first user untouched", stored, err)
	}
}

func TestCreateRefreshTokenDropsTheUsersExpiredOnes(t *testing.T) {
	s := newServer(t)
	repo := NewRepository(s.db)
	user := s.createUser(t, true)

	_, staleHash := issue(t, repo, user.ID, time.Now().Add(-time.Second))
	_, liveHash := issue(t, repo, user.ID, time.Now().Add(refreshTTL))

	for hash, want := range map[string]int64{staleHash: 0, liveHash: 1} {
		var count int64
		s.db.Model(&RefreshToken{}).Where("token_hash = ?", hash).Count(&count)
		if count != want {
			t.Errorf("rows for %q = %d, want %d", hash[:8], count, want)
		}
	}
}

func TestRotateRefreshTokenKeepsTheSessionExpiry(t *testing.T) {
	s := newServer(t)
	repo := NewRepository(s.db)
	user := s.createUser(t, true)

	sessionEnd := time.Now().Add(time.Hour).Truncate(time.Second)
	old, oldHash := issue(t, repo, user.ID, sessionEnd)
	next, nextHash := newRefreshToken()
	_ = next

	owner, err := repo.RotateRefreshToken(t.Context(), oldHash, nextHash)
	if err != nil || owner.ID != user.ID {
		t.Fatalf("RotateRefreshToken = %+v, %v; want the owner", owner, err)
	}

	var rotated RefreshToken
	s.db.Where("token_hash = ?", nextHash).Take(&rotated)
	if !rotated.ExpiresAt.Equal(sessionEnd) {
		t.Errorf("rotated token expires %v, want the session's %v", rotated.ExpiresAt, sessionEnd)
	}

	var revoked int64
	s.db.Model(&RefreshToken{}).Where("token_hash = ? AND revoked_at IS NOT NULL", oldHash).Count(&revoked)
	if revoked != 1 {
		t.Errorf("the old token was not revoked (%q)", old[:8])
	}
}

func TestRotateRefreshTokenTreatsALateReplayAsTheft(t *testing.T) {
	s := newServer(t)
	repo := NewRepository(s.db)
	user := s.createUser(t, true)

	_, phoneHash := issue(t, repo, user.ID, time.Now().Add(refreshTTL))
	_, laptopHash := issue(t, repo, user.ID, time.Now().Add(refreshTTL))
	_, rotatedHash := newRefreshToken()
	if _, err := repo.RotateRefreshToken(t.Context(), phoneHash, rotatedHash); err != nil {
		t.Fatalf("RotateRefreshToken: %v", err)
	}

	// Past the grace a lost-response retry would fall in: whoever presents it now copied it.
	s.db.Exec("UPDATE refresh_tokens SET revoked_at = now() - interval '1 minute' WHERE token_hash = ?", phoneHash)
	_, err := repo.RotateRefreshToken(t.Context(), phoneHash, "unused")
	if !errors.Is(err, ErrTokenReuse) {
		t.Fatalf("err = %v, want ErrTokenReuse", err)
	}

	var live int64
	s.db.Model(&RefreshToken{}).Where("user_id = ? AND revoked_at IS NULL", user.ID).Count(&live)
	if live != 0 {
		t.Errorf("%d sessions still live after a replay, want every one revoked", live)
	}
	for name, hash := range map[string]string{"rotated": rotatedHash, "laptop": laptopHash} {
		if _, err := repo.RotateRefreshToken(t.Context(), hash, "unused"); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s session: err = %v, want ErrInvalidToken", name, err)
		}
	}
}

func TestRotateRefreshTokenRefusesAnEarlyReplay(t *testing.T) {
	s := newServer(t)
	repo := NewRepository(s.db)
	user := s.createUser(t, true)

	_, oldHash := issue(t, repo, user.ID, time.Now().Add(refreshTTL))
	_, nextHash := newRefreshToken()
	if _, err := repo.RotateRefreshToken(t.Context(), oldHash, nextHash); err != nil {
		t.Fatalf("RotateRefreshToken: %v", err)
	}

	// Inside reuseGrace: refused, but the session it produced stays usable.
	if _, err := repo.RotateRefreshToken(t.Context(), oldHash, "unused"); !errors.Is(err, ErrInvalidToken) ||
		errors.Is(err, ErrTokenReuse) {
		t.Errorf("err = %v, want ErrInvalidToken and not ErrTokenReuse", err)
	}
	var live int64
	s.db.Model(&RefreshToken{}).Where("token_hash = ? AND revoked_at IS NULL", nextHash).Count(&live)
	if live != 1 {
		t.Error("the rotated session was revoked by a retry inside the grace")
	}
}

func TestRevokeRefreshTokenIsIdempotent(t *testing.T) {
	s := newServer(t)
	repo := NewRepository(s.db)
	user := s.createUser(t, true)
	_, hash := issue(t, repo, user.ID, time.Now().Add(refreshTTL))

	for range 2 {
		if err := repo.RevokeRefreshToken(t.Context(), hash); err != nil {
			t.Fatalf("RevokeRefreshToken: %v", err)
		}
	}
	if err := repo.RevokeRefreshToken(t.Context(), "never-issued"); err != nil {
		t.Errorf("revoking an unknown hash: err = %v, want none", err)
	}

	var revoked int64
	s.db.Model(&RefreshToken{}).Where("token_hash = ? AND revoked_at IS NOT NULL", hash).Count(&revoked)
	if revoked != 1 {
		t.Error("the token is not revoked")
	}
}
