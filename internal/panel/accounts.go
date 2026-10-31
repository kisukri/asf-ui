package panel

import (
	"net/http"
	"strconv"
	"strings"
)

var (
	loginUserLimit   = [2]int64{5, 15 * 60}
	loginIPLimit     = [2]int64{20, 15 * 60}
	registerIPLimit  = [2]int64{10, 3600}
	inviteInvalidMsg = "邀请码无效、已过期或已用完。"
)

func register(r *http.Request, inviteCode, username, password string) string {
	ip := clientIP(r)
	if !rateHit(db, "register_ip", ip, registerIPLimit[0], registerIPLimit[1]) {
		fail(429, "rate_limited", "注册尝试过于频繁，请稍后再试。")
	}
	username = validateUsername(username)
	validateNewPassword(password, username)
	code := normalizeInviteCode(inviteCode)
	if code == "" || len(code) > 64 {
		fail(400, "invite_invalid", inviteInvalidMsg)
	}
	pwHash := hashPassword(password)
	var token string
	if e := try(func() {
		db.tx(func(tx Q) {
			t := now()
			inv := one(tx, "SELECT id FROM invites WHERE code_hash = ?", sha256Hex(code))
			if inv == nil {
				fail(400, "invite_invalid", inviteInvalidMsg)
			}
			if one(tx, "SELECT 1 FROM users WHERE username = ?", username) != nil {
				fail(409, "username_taken", "这个用户名已被使用，请换一个。")
			}

			res := exec(tx, `UPDATE invites SET uses = uses + 1 WHERE id = ? AND uses < max_uses
				AND revoked_at IS NULL AND expires_at > ?`, inv.Int("id"), t)
			if affected(res) != 1 {
				fail(400, "invite_invalid", inviteInvalidMsg)
			}
			uid := lastID(exec(tx, `INSERT INTO users (username, password_hash, is_admin, is_active, invite_id, created_at, password_changed_at)
				VALUES (?, ?, 0, 1, ?, ?, ?)`, username, pwHash, inv.Int("id"), t, t))
			exec(tx, "INSERT INTO invite_uses (invite_id, user_id, used_at) VALUES (?, ?, ?)", inv.Int("id"), uid, t)
			exec(tx, "INSERT INTO user_prefs (user_id, default_minutes) VALUES (?, 0)", uid)
			record(tx, "register", "auth", auditOpt{UserID: uid, IP: ip, Detail: M{"invite_id": inv.Int("id")}})
			token = createSession(tx, uid, r)
			exec(tx, "UPDATE users SET last_login_at = ? WHERE id = ?", t, uid)
		})
	}); e != nil {
		record(db, "register", "auth", auditOpt{IP: ip, Outcome: e.Code, Detail: M{"username": username}})
		panic(e)
	}
	return token
}

func login(r *http.Request, username, password string) string {
	ip := clientIP(r)
	key := truncate(strings.ToLower(strings.TrimSpace(username)), 64)

	if !rateHit(db, "login_ip", ip, loginIPLimit[0], loginIPLimit[1]) ||
		!rateHit(db, "login_user", key, loginUserLimit[0], loginUserLimit[1]) {
		record(db, "login", "auth", auditOpt{IP: ip, Outcome: "rate_limited", Detail: M{"username": key}})
		fail(429, "rate_limited", "登录失败次数过多，请 15 分钟后再试。")
	}
	u := one(db, "SELECT id, password_hash, is_active FROM users WHERE username = ?", strings.TrimSpace(username))
	stored := ""
	if u != nil {
		stored = u.Str("password_hash")
	}
	ok, rehash := verifyPassword(stored, password)
	if !ok {
		var uid int64
		if u != nil {
			uid = u.Int("id")
		}
		record(db, "login", "auth", auditOpt{UserID: uid, IP: ip, Outcome: "bad_credentials", Detail: M{"username": key}})
		fail(401, "bad_credentials", "用户名或密码错误。")
	}
	if !u.Bool("is_active") {
		record(db, "login", "auth", auditOpt{UserID: u.Int("id"), IP: ip, Outcome: "disabled"})
		fail(403, "account_disabled", "该账户已被管理员停用。")
	}
	rateReset(db, "login_user", key)
	rateUndo(db, "login_ip", ip)
	var newHash string
	if rehash {
		newHash = hashPassword(password)
	}
	var token string
	db.tx(func(tx Q) {
		if newHash != "" {
			exec(tx, "UPDATE users SET password_hash = ? WHERE id = ?", newHash, u.Int("id"))
		}
		token = createSession(tx, u.Int("id"), r)
		exec(tx, "UPDATE users SET last_login_at = ? WHERE id = ?", now(), u.Int("id"))
		record(tx, "login", "auth", auditOpt{UserID: u.Int("id"), IP: ip})
	})
	return token
}

func logout(u *CurrentUser) {
	exec(db, "DELETE FROM sessions WHERE id = ?", u.SessionID)
	record(db, "logout", "auth", auditOpt{UserID: u.ID, IP: u.IP})
}

func changePassword(u *CurrentUser, current, next string) {
	row := one(db, "SELECT password_hash FROM users WHERE id = ?", u.ID)
	if !rateHit(db, "pwchange", strconv.FormatInt(u.ID, 10), 10, 3600) {
		fail(429, "rate_limited", "尝试次数过多，请稍后再试。")
	}
	if ok, _ := verifyPassword(row.Str("password_hash"), current); !ok {
		record(db, "change_password", "account", auditOpt{UserID: u.ID, IP: u.IP, Outcome: "bad_current"})
		fail(400, "bad_credentials", "当前密码不正确。")
	}
	validateNewPassword(next, u.Username)
	h := hashPassword(next)
	db.tx(func(tx Q) {
		exec(tx, "UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?", h, now(), u.ID)
		exec(tx, "DELETE FROM sessions WHERE user_id = ? AND id != ?", u.ID, u.SessionID)
		record(tx, "change_password", "account", auditOpt{UserID: u.ID, IP: u.IP})
	})
}

func listSessions(u *CurrentUser) []M {
	out := []M{}
	for _, r := range all(db, "SELECT id, created_at, last_seen_at, ip, user_agent FROM sessions WHERE user_id = ? ORDER BY last_seen_at DESC", u.ID) {
		out = append(out, M{"id": r["id"], "current": r.Int("id") == u.SessionID, "created_at": r["created_at"],
			"last_seen_at": r["last_seen_at"], "ip": r["ip"], "user_agent": r["user_agent"]})
	}
	return out
}

func revokeOtherSessions(u *CurrentUser) int64 {
	n := affected(exec(db, "DELETE FROM sessions WHERE user_id = ? AND id != ?", u.ID, u.SessionID))
	record(db, "revoke_sessions", "account", auditOpt{UserID: u.ID, IP: u.IP, Detail: M{"count": n}})
	return n
}

func getPrefs(userID int64) M {
	r := one(db, "SELECT default_connection_id, default_minutes FROM user_prefs WHERE user_id = ?", userID)
	if r == nil {
		return M{"default_connection_id": nil, "default_minutes": 0}
	}
	return M{"default_connection_id": r["default_connection_id"], "default_minutes": r["default_minutes"]}
}

func setPrefs(u *CurrentUser, defaultConn *int64, minutes int64) M {
	var dc any
	if defaultConn != nil {
		getAccessibleConnection(u.ID, *defaultConn)
		dc = *defaultConn
	}
	exec(db, `INSERT INTO user_prefs (user_id, default_connection_id, default_minutes) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET default_connection_id = excluded.default_connection_id,
		default_minutes = excluded.default_minutes`, u.ID, dc, minutes)
	return getPrefs(u.ID)
}

func activity(u *CurrentUser) []M {
	out := []M{}
	for _, r := range all(db, `SELECT action, category, target, outcome, detail, created_at FROM audit_log
		WHERE user_id = ? ORDER BY id DESC LIMIT 100`, u.ID) {
		out = append(out, M(r))
	}
	return out
}
