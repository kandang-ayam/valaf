package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/valaf/valaf/internal/auth"
	"github.com/valaf/valaf/internal/store"
)

const sessionCookie = "valaf_session"

// AuthConfig tunes session/proxy behavior.
type AuthConfig struct {
	TrustedProxyHeader string        // e.g. "X-Forwarded-User"; empty disables proxy auth
	SessionSecure      bool          // Secure flag on the session cookie (set true behind HTTPS)
	SessionTTL         time.Duration // session lifetime
}

// AuthUser is the authenticated principal carried on the request context.
type AuthUser struct {
	ID       string
	Username string
	Role     string
	CSRF     string // session CSRF token ("" for proxy auth)
	ViaProxy bool
}

type ctxKey int

const userCtxKey ctxKey = iota

// dummyHash equalizes login timing when a username does not exist.
var dummyHash, _ = auth.HashPassword("valaf-timing-equalizer")

// withUser resolves the request principal (session cookie or trusted proxy
// header) and attaches it to the context. Anonymous requests pass through.
func (s *Server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := s.resolveAuth(r); u != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) resolveAuth(r *http.Request) *AuthUser {
	ctx := r.Context()

	// Trusted reverse-proxy header (SSO gateway). The header can't be forged
	// cross-site, so it also serves as the CSRF defense for proxy users.
	if h := s.authCfg.TrustedProxyHeader; h != "" {
		if username := r.Header.Get(h); username != "" {
			if u, err := s.users.UpsertProxyUser(ctx, username); err == nil {
				return &AuthUser{ID: u.ID, Username: u.Username, Role: u.Role, ViaProxy: true}
			}
		}
	}

	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	sess, u, err := s.sessions.Resolve(ctx, c.Value)
	if err != nil {
		return nil
	}
	return &AuthUser{ID: u.ID, Username: u.Username, Role: u.Role, CSRF: sess.CSRFSecret}
}

func currentUser(r *http.Request) *AuthUser {
	u, _ := r.Context().Value(userCtxKey).(*AuthUser)
	return u
}

// requireUser redirects anonymous requests to the login page.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r) == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireRole enforces a minimum role (viewer < engineer < admin).
func (s *Server) requireRole(min string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !roleAtLeast(u.Role, min) {
			http.Error(w, "forbidden: requires "+min+" role", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func roleRank(role string) int {
	switch role {
	case "admin":
		return 3
	case "engineer":
		return 2
	case "viewer":
		return 1
	default:
		return 0
	}
}

func roleAtLeast(have, min string) bool { return roleRank(have) >= roleRank(min) }

// checkCSRF validates a state-changing request. Proxy-authenticated users are
// exempt (their identity header is itself unforgeable cross-site).
func (s *Server) checkCSRF(r *http.Request) bool {
	u := currentUser(r)
	if u == nil {
		return false
	}
	if u.ViaProxy {
		return true
	}
	token := r.PostFormValue("csrf")
	if token == "" {
		token = r.Header.Get("X-CSRF-Token")
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(u.CSRF)) == 1
}

// ---- handlers ----

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login", loginData{baseData: baseData{Title: "Sign in"}})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")

	u, err := s.users.FindByUsername(r.Context(), username)
	if errors.Is(err, store.ErrUserNotFound) {
		_, _ = auth.VerifyPassword(password, dummyHash) // equalize timing
		s.loginError(w)
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "login lookup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ok, _ := auth.VerifyPassword(password, u.PasswordHash)
	if !ok || u.PasswordHash == "" || !u.IsActive {
		s.loginError(w)
		return
	}

	token, err := s.sessions.Create(r.Context(), u.ID, s.authCfg.SessionTTL)
	if err != nil {
		s.log.ErrorContext(r.Context(), "session create failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.users.UpdateLastLogin(r.Context(), u.ID)
	s.setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginError(w http.ResponseWriter) {
	w.WriteHeader(http.StatusUnauthorized)
	s.render(w, "login", loginData{baseData: baseData{Title: "Sign in"}, Error: "Invalid username or password."})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.sessions.Delete(r.Context(), c.Value)
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.authCfg.SessionSecure,
		MaxAge: int(s.authCfg.SessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
}
