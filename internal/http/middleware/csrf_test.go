package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/csrf"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

type csrfCase struct {
	method     string
	origin     string
	referer    string
	bearer     bool
	authCookie bool
	// csrfCookie and csrfHeader: "valid" uses the signed token, "" omits it,
	// anything else is sent as is.
	csrfCookie string
	csrfHeader string
	wantStatus int
	wantCode   string
}

func TestCSRFGuard(t *testing.T) {
	signer, err := csrf.NewSigner(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	other, err := csrf.NewSigner(bytes.Repeat([]byte{8}, 32))
	require.NoError(t, err)
	valid := signer.Issue()
	forged := other.Issue()

	tests := map[string]csrfCase{
		"safe methods pass": {
			method: http.MethodGet, origin: "https://evil.example", authCookie: true, wantStatus: http.StatusOK,
		},
		"native client without cookies is exempt": {
			method: http.MethodPost, wantStatus: http.StatusOK,
		},
		"native bearer client is exempt": {
			method: http.MethodPost, bearer: true, wantStatus: http.StatusOK,
		},
		"browser with a valid token passes": {
			method: http.MethodPost, origin: appOrigin, authCookie: true, csrfCookie: "valid", csrfHeader: "valid", wantStatus: http.StatusOK,
		},
		"browser login (no auth cookie yet) still needs the token": {
			method: http.MethodPost, origin: appOrigin, wantStatus: http.StatusForbidden, wantCode: "csrf_invalid",
		},
		"browser login with a valid token passes": {
			method: http.MethodPost, origin: appOrigin, csrfCookie: "valid", csrfHeader: "valid", wantStatus: http.StatusOK,
		},
		"foreign origin is rejected even with a token": {
			method: http.MethodPost, origin: "https://evil.example", authCookie: true, csrfCookie: "valid", csrfHeader: "valid",
			wantStatus: http.StatusForbidden, wantCode: "origin_not_allowed",
		},
		"null origin is rejected": {
			method: http.MethodDelete, origin: "null", wantStatus: http.StatusForbidden, wantCode: "origin_not_allowed",
		},
		"cookie without origin falls back to a foreign referer": {
			method: http.MethodPost, referer: "https://evil.example/page", authCookie: true, csrfCookie: "valid", csrfHeader: "valid",
			wantStatus: http.StatusForbidden, wantCode: "origin_not_allowed",
		},
		"cookie without origin and an allowed referer passes": {
			method: http.MethodPost, referer: appOrigin + "/settings", authCookie: true, csrfCookie: "valid", csrfHeader: "valid",
			wantStatus: http.StatusOK,
		},
		"cookie without origin or referer still needs the token": {
			method: http.MethodPatch, authCookie: true, wantStatus: http.StatusForbidden, wantCode: "csrf_invalid",
		},
		"bearer mixed with an auth cookie is rejected": {
			method: http.MethodPost, bearer: true, authCookie: true, csrfCookie: "valid", csrfHeader: "valid",
			wantStatus: http.StatusForbidden, wantCode: "ambiguous_credentials",
		},
		"missing header": {
			method: http.MethodPost, origin: appOrigin, authCookie: true, csrfCookie: "valid",
			wantStatus: http.StatusForbidden, wantCode: "csrf_invalid",
		},
		"missing cookie": {
			method: http.MethodPost, origin: appOrigin, authCookie: true, csrfHeader: "valid",
			wantStatus: http.StatusForbidden, wantCode: "csrf_invalid",
		},
		"header and cookie differ": {
			method: http.MethodPut, origin: appOrigin, authCookie: true, csrfCookie: "valid", csrfHeader: signer.Issue(),
			wantStatus: http.StatusForbidden, wantCode: "csrf_invalid",
		},
		"matching but unsigned token (cookie tossing)": {
			method: http.MethodPost, origin: appOrigin, authCookie: true, csrfCookie: forged, csrfHeader: forged,
			wantStatus: http.StatusForbidden, wantCode: "csrf_invalid",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			metrics := observability.NewMetrics("test", "local")
			policy := cookie.Policy{Secure: true}
			called := false
			h := CSRF(CSRFOptions{
				Signer:  signer,
				Cookies: policy,
				Allowed: NewOriginAllowlist([]string{appOrigin}),
				Metrics: metrics,
			})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			r := httptest.NewRequest(tc.method, "/api/v1/things", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				r.Header.Set("Referer", tc.referer)
			}
			if tc.bearer {
				r.Header.Set("Authorization", "Bearer token")
			}
			if tc.authCookie {
				r.AddCookie(&http.Cookie{Name: policy.Name(cookie.AccessToken, cookie.RootPath), Value: "jwt"})
			}
			resolve := func(v string) string {
				if v == "valid" {
					return valid
				}
				return v
			}
			if tc.csrfCookie != "" {
				r.AddCookie(&http.Cookie{Name: policy.Name(cookie.CSRF, cookie.RootPath), Value: resolve(tc.csrfCookie)})
			}
			if tc.csrfHeader != "" {
				r.Header.Set(CSRFHeader, resolve(tc.csrfHeader))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			assert.Equal(t, tc.wantStatus, w.Code)
			assert.Equal(t, tc.wantStatus == http.StatusOK, called)
			if tc.wantCode != "" {
				assert.Equal(t, problem.ContentType, w.Header().Get("Content-Type"))
				var p problem.Problem
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
				assert.Equal(t, tc.wantCode, p.Code)
				assert.Contains(t, metricsBody(t, metrics), "monthra_csrf_rejected_total")
			}
		})
	}
}
