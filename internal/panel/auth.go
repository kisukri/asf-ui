package panel

import (
	"net"
	"net/http"
	"strings"
)

var (
	db  *DB
	box *CredentialBox
)

type CurrentUser struct {
	ID        int64
	Username  string
	IsAdmin   bool
	SessionID int64
	CSRF      string
	IP        string
}

func sessionCookie() string {
	if cfg.CookieSecure {
		return "__Host-asfp_session"
	}
	return "asfp_session"
}

func clientIP(r *http.Request) string {
	if cfg.TrustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return truncate(strings.TrimSpace(strings.Split(xff, ",")[0]), 64)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return truncate(host, 64)
}

func createSession(q Q, userID int64, r *http.Request) string {
	token := newToken(32)
	t := now()
	exec(q, `INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sha256Hex(token), userID, newToken(24), t, t, t+int64(cfg.SessionAbsoluteHours)*3600,
		clientIP(r), truncate(r.UserAgent(), 160))
	return token
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie(), Value: token, Path: "/", MaxAge: cfg.SessionAbsoluteHours * 3600,
		HttpOnly: true, Secure: cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func loadUser(r *http.Request) *CurrentUser {
	c, err := r.Cookie(sessionCookie())
	if err != nil || c.Value == "" || len(c.Value) > 200 {
		return nil
	}
	row := one(db, `SELECT s.id, s.user_id, s.csrf_token, s.last_seen_at, s.expires_at, u.username, u.is_admin, u.is_active
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, sha256Hex(c.Value))
	if row == nil {
		return nil
	}
	t := now()
	idleExpired := t-row.Int("last_seen_at") > int64(cfg.SessionIdleMinutes)*60
	if row.Int("expires_at") <= t || idleExpired || !row.Bool("is_active") {
		exec(db, "DELETE FROM sessions WHERE id = ?", row.Int("id"))
		return nil
	}
	if t-row.Int("last_seen_at") > 60 {
		exec(db, "UPDATE sessions SET last_seen_at = ? WHERE id = ?", t, row.Int("id"))
	}
	return &CurrentUser{
		ID: row.Int("user_id"), Username: row.Str("username"), IsAdmin: row.Bool("is_admin"),
		SessionID: row.Int("id"), CSRF: row.Str("csrf_token"), IP: clientIP(r),
	}
}
