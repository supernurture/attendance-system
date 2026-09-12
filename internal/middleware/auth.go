package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Claims identifies the caller of an authenticated request.
type Claims struct {
	UserID int64
	Role   Role
}

type claimsContextKey struct{}

type accessClaims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// SignAccessToken issues an HS256 JWT for claims that expires after ttl.
func SignAccessToken(secret []byte, claims Claims, ttl time.Duration) string {
	now := time.Now()
	signed, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
		Role: string(claims.Role),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(claims.UserID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}).SignedString(secret) // HMAC over a []byte key has no failure path
	return signed
}

// Auth rejects a request without a valid "Authorization: Bearer <access token>" with 401.
func Auth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, found := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !found {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "missing bearer token"})
			return
		}

		claims, err := parseAccessToken(secret, raw)
		if err != nil {
			_ = c.Error(err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "invalid or expired token"})
			return
		}

		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), claimsContextKey{}, claims))
		c.Next()
	}
}

// ClaimsFrom returns the caller set by Auth; ok is false on a route Auth does not guard.
func ClaimsFrom(ctx context.Context) (claims Claims, ok bool) {
	claims, ok = ctx.Value(claimsContextKey{}).(Claims)
	return claims, ok
}

func parseAccessToken(secret []byte, raw string) (Claims, error) {
	var parsed accessClaims
	_, err := jwt.ParseWithClaims(raw, &parsed, func(*jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		return Claims{}, err
	}

	userID, err := strconv.ParseInt(parsed.Subject, 10, 64)
	if err != nil {
		return Claims{}, fmt.Errorf("subject %q is not a user id", parsed.Subject)
	}
	return Claims{UserID: userID, Role: Role(parsed.Role)}, nil
}
