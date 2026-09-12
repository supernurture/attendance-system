package auth

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// User is the subset of users that auth reads and writes.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	FullName     string
	Role         string
	IsActive     bool
}

func (User) TableName() string { return "users" }

type RefreshToken struct {
	ID        int64
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
}

func (RefreshToken) TableName() string { return "refresh_tokens" }

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) UserByEmail(ctx context.Context, email string) (User, error) {
	var user User
	err := r.db.WithContext(ctx).Where("email = ?", email).Take(&user).Error
	return user, err
}

// CreateUserIfAbsent inserts user unless the email is taken, leaving the existing row untouched.
func (r *Repository) CreateUserIfAbsent(ctx context.Context, user User) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&user).Error
}

// CreateRefreshToken stores token and drops the user's expired ones, so the table stays bounded.
func (r *Repository) CreateRefreshToken(ctx context.Context, token RefreshToken) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		expired := tx.Exec("DELETE FROM refresh_tokens WHERE user_id = ? AND expires_at <= now()", token.UserID)
		if expired.Error != nil {
			return expired.Error
		}
		return tx.Create(&token).Error
	})
}

// RotateRefreshToken swaps oldHash for newHash, keeping the login's expiry, and returns the owner.
// Fails with ErrInvalidToken, or ErrTokenReuse when oldHash had already been traded in.
func (r *Repository) RotateRefreshToken(ctx context.Context, oldHash, newHash string) (User, error) {
	var user User
	var outcome error // returned after commit: returned from inside, it would roll back the revokes
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The conditional UPDATE is the lock: of two concurrent refreshes, only one gets a row back.
		var old struct {
			UserID    int64
			ExpiresAt time.Time
		}
		if err := tx.Raw(`UPDATE refresh_tokens SET revoked_at = now()
			WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > now()
			RETURNING user_id, expires_at`, oldHash).Scan(&old).Error; err != nil {
			return err
		}
		if old.UserID == 0 {
			reused, err := revokeAllIfReused(tx, oldHash)
			outcome = ErrInvalidToken
			if reused {
				outcome = ErrTokenReuse
			}
			return err
		}

		result := tx.Where("id = ? AND is_active", old.UserID).Limit(1).Find(&user)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			outcome = ErrInvalidToken
			return nil
		}
		return tx.Create(&RefreshToken{UserID: user.ID, TokenHash: newHash, ExpiresAt: old.ExpiresAt}).Error
	})
	if err != nil {
		return User{}, err
	}
	return user, outcome
}

// revokeAllIfReused revokes every session of hash's owner if hash was revoked more than reuseGrace ago.
func revokeAllIfReused(tx *gorm.DB, hash string) (reused bool, err error) {
	var userID int64
	if err := tx.Raw(`SELECT user_id FROM refresh_tokens
		WHERE token_hash = ? AND revoked_at < now() - make_interval(secs => ?)`,
		hash, reuseGrace.Seconds()).Scan(&userID).Error; err != nil {
		return false, err
	}
	if userID == 0 {
		return false, nil
	}
	err = tx.Exec("UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = ? AND revoked_at IS NULL", userID).Error
	return true, err
}

func (r *Repository) RevokeRefreshToken(ctx context.Context, hash string) error {
	return r.db.WithContext(ctx).
		Exec("UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = ? AND revoked_at IS NULL", hash).Error
}
