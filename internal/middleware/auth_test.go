package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

var testSecret = []byte("a-test-secret-that-is-long-enough-000")

func authRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.ContextWithFallback = true
	router.GET("/me", Auth(testSecret), func(c *gin.Context) {
		claims, ok := ClaimsFrom(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.String(http.StatusOK, "%d|%s", claims.UserID, claims.Role)
	})
	return router
}

func getMe(t *testing.T, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return do(authRouter(), req)
}

func sign(secret []byte, ttl time.Duration) string {
	return SignAccessToken(secret, Claims{UserID: 42, Role: "supervisor"}, ttl)
}

func TestAuthAcceptsAValidToken(t *testing.T) {
	rec := getMe(t, "Bearer "+sign(testSecret, time.Minute))

	if rec.Code != http.StatusOK || rec.Body.String() != "42|supervisor" {
		t.Fatalf("got %d %q, want 200 with the claims", rec.Code, rec.Body)
	}
}

func TestAuthRejects(t *testing.T) {
	noneToken, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "1", "role": "super_admin", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none token: %v", err)
	}
	noExpiry, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "1"}).SignedString(testSecret)
	if err != nil {
		t.Fatalf("sign token without exp: %v", err)
	}
	badSubject, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "admin", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(testSecret)
	if err != nil {
		t.Fatalf("sign token with bad subject: %v", err)
	}

	tests := map[string]string{
		"no header":        "",
		"not bearer":       "Basic dXNlcjpwYXNz",
		"garbage":          "Bearer not.a.jwt",
		"expired":          "Bearer " + sign(testSecret, -time.Minute),
		"other secret":     "Bearer " + sign([]byte("some-other-secret-also-32-bytes-long"), time.Minute),
		"alg none":         "Bearer " + noneToken,
		"no expiry":        "Bearer " + noExpiry,
		"non-numeric user": "Bearer " + badSubject,
	}
	for name, header := range tests {
		t.Run(name, func(t *testing.T) {
			if rec := getMe(t, header); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestClaimsFromOutsideAuth(t *testing.T) {
	if claims, ok := ClaimsFrom(httptest.NewRequest(http.MethodGet, "/", nil).Context()); ok {
		t.Errorf("ClaimsFrom = %+v on an unauthenticated context, want ok=false", claims)
	}
}
