package auth

import (
	"errors"
	"fmt"
	"time"
)

const (
	AccessTTL  = 15 * time.Minute
	refreshTTL = 30 * 24 * time.Hour // absolute: rotation keeps the login's expiry

	// reuseGrace is how long a rotated token is only refused, not treated as stolen: a client whose
	// refresh response was lost retries within it.
	reuseGrace = 30 * time.Second

	maxAttemptsPerIP    = 10 // per email from one client IP
	ipWindow            = 15 * time.Minute
	maxAttemptsPerEmail = 100 // across all IPs, against guessing from many addresses
	emailWindow         = time.Hour

	ipAttemptsKey    = "login_attempts:ip:"
	emailAttemptsKey = "login_attempts:email:"

	minPasswordLen = 8 // the maximum, 72 bytes, is bcrypt's own and it reports it

)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrTooManyAttempts    = errors.New("too many failed login attempts, try again later")
	ErrInvalidToken       = errors.New("invalid or expired refresh token")
	ErrTokenReuse         = fmt.Errorf("%w: reused after rotation, every session of its user revoked", ErrInvalidToken)
)

// dummyHash makes an unknown email cost the same bcrypt compare as a wrong password.
// Keep its cost at bcrypt.DefaultCost (10), what real hashes use, or the timing gives emails away.
var dummyHash = []byte("$2a$10$DDEY9DK7wp.xrj2cLgezQePzf0UlRVq1zpLT205cLMIWU02ShDutG")
