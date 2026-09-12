package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"attendance-system/internal/middleware"
)

type TokenPair struct {
	AccessToken  string
	RefreshToken string
}

type Service struct {
	repo   *Repository
	redis  *goredis.Client
	secret []byte
}

func NewService(db *gorm.DB, redis *goredis.Client, jwtSecret []byte) *Service {
	return &Service{repo: NewRepository(db), redis: redis, secret: jwtSecret}
}

// Login issues a token pair; attempts are capped per email+IP first, then per email across all IPs.
func (s *Service) Login(ctx context.Context, email, password, clientIP string) (TokenPair, error) {
	email = normalizeEmail(email)
	ipKey := ipAttemptsKey + clientIP + ":" + email

	for _, limit := range []struct {
		key    string
		max    int64
		window time.Duration
	}{
		{ipKey, maxAttemptsPerIP, ipWindow},
		{emailAttemptsKey + email, maxAttemptsPerEmail, emailWindow},
	} {
		attempts, err := s.countAttempt(ctx, limit.key, limit.window)
		if err != nil {
			return TokenPair{}, err
		}
		if attempts > limit.max {
			return TokenPair{}, ErrTooManyAttempts
		}
	}

	user, err := s.repo.UserByEmail(ctx, email)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return TokenPair{}, ErrInvalidCredentials
	case err != nil:
		return TokenPair{}, fmt.Errorf("find user: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil || !user.IsActive {
		return TokenPair{}, ErrInvalidCredentials
	}

	// Only the per-IP count resets; clearing the email budget would hand a distributed guesser a new one.
	if err := s.redis.Del(ctx, ipKey).Err(); err != nil {
		return TokenPair{}, fmt.Errorf("reset login attempts: %w", err)
	}

	refresh, hash := newRefreshToken()
	if err := s.repo.CreateRefreshToken(ctx, RefreshToken{
		UserID: user.ID, TokenHash: hash, ExpiresAt: time.Now().Add(refreshTTL),
	}); err != nil {
		return TokenPair{}, fmt.Errorf("store refresh token: %w", err)
	}
	return s.pair(user, refresh), nil
}

// Refresh trades a refresh token for a new pair, revoking the old one.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	next, nextHash := newRefreshToken()
	user, err := s.repo.RotateRefreshToken(ctx, hashToken(refreshToken), nextHash)
	switch {
	case errors.Is(err, ErrInvalidToken):
		return TokenPair{}, err
	case err != nil:
		return TokenPair{}, fmt.Errorf("rotate refresh token: %w", err)
	}
	return s.pair(user, next), nil
}

func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	if err := s.repo.RevokeRefreshToken(ctx, hashToken(refreshToken)); err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}

// SeedAdmin creates a super_admin unless the email is already taken; an existing user is never changed.
func (s *Service) SeedAdmin(ctx context.Context, email, password string) error {
	email = normalizeEmail(email)
	if err := validateSeedAdmin(email, password); err != nil {
		return err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("seed admin password: %w", err)
	}
	if err := s.repo.CreateUserIfAbsent(ctx, User{
		Email: email, PasswordHash: string(hash), FullName: "Super Admin",
		Role: string(middleware.RoleSuperAdmin), IsActive: true,
	}); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	return nil
}

// countAttempt counts before the password check, or a burst of concurrent requests would slip past.
// The window starts at the first attempt and is not extended.
func (s *Service) countAttempt(ctx context.Context, key string, window time.Duration) (int64, error) {
	var count *goredis.IntCmd
	_, err := s.redis.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		pipe.SetNX(ctx, key, 0, window)
		count = pipe.Incr(ctx, key)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("count login attempt: %w", err)
	}
	return count.Val(), nil
}

func (s *Service) pair(user User, refresh string) TokenPair {
	claims := middleware.Claims{UserID: user.ID, Role: middleware.Role(user.Role)}
	return TokenPair{AccessToken: middleware.SignAccessToken(s.secret, claims, AccessTTL), RefreshToken: refresh}
}

// newRefreshToken returns a random token (rand.Text: 26 base32 chars, 130 bits) and the hash stored for it.
func newRefreshToken() (token, hash string) {
	token = rand.Text()
	return token, hashToken(token)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
