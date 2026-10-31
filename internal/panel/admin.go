package panel

import (
	"fmt"
	"strings"
)

func inviteStatus(r Row, t int64) (string, string) {
	switch {
	case !r.Null("revoked_at"):
		return "revoked", "已撤销"
	case r.Int("uses") >= r.Int("max_uses"):
		if r.Int("max_uses") > 1 {
			return "used_up", "已用完"
		}
		return "used_up", "已使用"
	case r.Int("expires_at") <= t:
		return "expired", "已过期"
	}
	return "active", "可用"
}

func listInvites() []M {
	t := now()
	out := []M{}
	for _, r := range all(db, "SELECT i.*, u.username AS creator FROM invites i LEFT JOIN users u ON u.id = i.created_by ORDER BY i.id DESC LIMIT 200") {
		key, text := inviteStatus(r, t)
		usedBy := []string{}
		for _, x := range all(db, "SELECT u.username FROM invite_uses iu LEFT JOIN users u ON u.id = iu.user_id WHERE iu.invite_id = ?", r.Int("id")) {
			if !x.Null("username") {
				usedBy = append(usedBy, x.Str("username"))
			}
		}
		out = append(out, M{"id": r["id"], "hint": r["code_hint"], "note": r["note"], "max_uses": r["max_uses"], "uses": r["uses"],
			"expires_at": r["expires_at"], "revoked_at": r["revoked_at"], "created_at": r["created_at"], "creator": r["creator"],
			"status": key, "status_text": text, "used_by": usedBy})
	}
	return out
}

func createInvite(admin *CurrentUser, maxUses, hours int, note *string) M {
	if maxUses < 1 || maxUses > 100 {
		fail(400, "invalid_request", "可使用次数需在 1–100 之间。")
	}
	if hours < 1 || hours > 24*90 {
		fail(400, "invalid_request", "有效期需在 1 小时到 90 天之间。")
	}
	var n any
	if note != nil {
		if s := truncate(strings.TrimSpace(stripCtrl(*note, " ")), 100); s != "" {
			n = s
		}
	}
	code := newInviteCode()
	t := now()
	var id int64
	db.tx(func(tx Q) {
		id = lastID(exec(tx, "INSERT INTO invites (code_hash, code_hint, note, max_uses, expires_at, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			sha256Hex(code), "…"+code[len(code)-5:], n, maxUses, t+int64(hours)*3600, admin.ID, t))
		record(tx, "invite_create", "admin", auditOpt{UserID: admin.ID, IP: admin.IP, Target: fmt.Sprintf("invite:%d", id),
			Detail: M{"max_uses": maxUses, "expires_in_hours": hours}})
	})
	return M{"id": id, "code": code}
}

func revokeInvite(admin *CurrentUser, id int64) {
	res := exec(db, "UPDATE invites SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now(), id)
	if affected(res) == 0 && one(db, "SELECT 1 FROM invites WHERE id = ?", id) == nil {
		panic(notFound("邀请码"))
	}
	record(db, "invite_revoke", "admin", auditOpt{UserID: admin.ID, IP: admin.IP, Target: fmt.Sprintf("invite:%d", id)})
}

func listUsers() []M {
	out := []M{}
	for _, r := range all(db, `SELECT u.id, u.username, u.is_admin, u.is_active, u.created_at, u.last_login_at,
		(SELECT COUNT(*) FROM asf_connections c WHERE c.user_id = u.id) AS connections,
		(SELECT COUNT(*) FROM bot_assignments ba WHERE ba.user_id = u.id) AS assigned_bots,
		u.bot_quota, (SELECT COUNT(*) FROM bots b WHERE b.created_by = u.id AND b.present = 1) AS created_bots,
		(SELECT COUNT(*) FROM playtime_tasks t WHERE t.user_id = u.id AND t.status IN ('running','queued')) AS active_tasks
		FROM users u ORDER BY u.id`) {
		out = append(out, M(r))
	}
	return out
}

func setUserActive(admin *CurrentUser, uid int64, active bool) []string {
	if one(db, "SELECT id FROM users WHERE id = ?", uid) == nil {
		panic(notFound("用户"))
	}
	if uid == admin.ID {
		fail(400, "invalid_request", "不能停用自己的账户。")
	}
	warnings := []string{}

	var botIDs []int64
	for _, r := range all(db, `SELECT bot_id AS id FROM bot_assignments WHERE user_id = ?
		UNION SELECT b.id FROM bots b JOIN asf_connections c ON c.id = b.connection_id WHERE c.user_id = ?`, uid, uid) {
		botIDs = append(botIDs, r.Int("id"))
	}
	ctx, cancel := adminLockCtx()
	unlock := lockBots(ctx, botIDs)
	exec(db, "UPDATE users SET is_active = ? WHERE id = ?", b2i(active), uid)
	if !active {
		exec(db, "DELETE FROM sessions WHERE user_id = ?", uid)
	}
	unlock()
	cancel()
	if !active {
		bots := stopTasksWhere("user_id = ?", []any{uid}, "user_disabled")
		for _, b := range bots {
			sched.syncBot(b)
		}
		if len(bots) > 0 {
			warnings = append(warnings, fmt.Sprintf("已停止该用户 %d 个 Bot 上的游戏时长任务，并尝试恢复其挂卡状态。", len(bots)))
		}
	}
	action := "user_disable"
	if active {
		action = "user_enable"
	}
	record(db, action, "admin", auditOpt{UserID: admin.ID, IP: admin.IP, Target: fmt.Sprintf("user:%d", uid)})
	return warnings
}

func setUserQuota(admin *CurrentUser, uid int64, quota int) {
	if quota < 0 || quota > 50 {
		fail(400, "invalid_request", "额度需在 0–50 之间。")
	}
	if affected(exec(db, "UPDATE users SET bot_quota = ? WHERE id = ? AND is_admin = 0", quota, uid)) == 0 {
		panic(notFound("用户"))
	}
	record(db, "user_quota", "admin", auditOpt{UserID: admin.ID, IP: admin.IP, Target: fmt.Sprintf("user:%d", uid), Detail: M{"bot_quota": quota}})
}

func listAssignableBots() M {
	bots := []M{}
	for _, r := range all(db, `SELECT b.id AS bot_id, b.name AS bot, b.present, c.id AS connection_id, c.name AS connection_name,
		c.enabled, o.username AS owner, a.user_id AS assigned_user_id, u.username AS assigned_username, a.assigned_at,
		(SELECT COUNT(*) FROM playtime_tasks t WHERE t.bot_id = b.id AND t.status IN ('running','queued')) AS active_tasks
		FROM bots b
		JOIN asf_connections c ON c.id = b.connection_id
		JOIN users o ON o.id = c.user_id AND o.is_admin = 1
		LEFT JOIN bot_assignments a ON a.bot_id = b.id
		LEFT JOIN users u ON u.id = a.user_id
		ORDER BY c.id, b.name`) {
		bots = append(bots, M(r))
	}
	users := []M{}
	for _, r := range all(db, "SELECT id, username, is_active FROM users WHERE is_admin = 0 ORDER BY username") {
		users = append(users, M(r))
	}
	return M{"bots": bots, "users": users}
}

func assignBot(admin *CurrentUser, botID int64, userID *int64) []string {
	row := one(db, `SELECT b.id, b.name, b.connection_id, c.user_id AS owner_id, a.user_id AS current FROM bots b
		JOIN asf_connections c ON c.id = b.connection_id JOIN users o ON o.id = c.user_id AND o.is_admin = 1
		LEFT JOIN bot_assignments a ON a.bot_id = b.id WHERE b.id = ?`, botID)
	if row == nil {
		panic(notFound("Bot"))
	}
	if userID != nil {
		target := one(db, "SELECT id, is_admin FROM users WHERE id = ?", *userID)
		if target == nil {
			panic(notFound("用户"))
		}
		if target.Bool("is_admin") {
			fail(400, "invalid_request", "只能分配给普通用户；要自己管理请选择“不分配”。")
		}
	}
	if (userID == nil && row.Null("current")) || (userID != nil && !row.Null("current") && row.Int("current") == *userID) {
		return []string{}
	}
	newUser := row.Int("owner_id")
	if userID != nil {
		newUser = *userID
	}
	warnings := []string{}
	ctx, cancel := adminLockCtx()
	defer cancel()
	unlock := lockBot(ctx, botID)
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	db.tx(func(tx Q) {
		t := now()
		n := affected(exec(tx, `UPDATE playtime_tasks SET status = 'stopped', stop_reason = 'bot_reassigned', finished_at = ?, updated_at = ?
			WHERE bot_id = ? AND user_id != ? AND status IN ('running','queued')`, t, t, botID, newUser))
		if n > 0 {
			warnings = append(warnings, fmt.Sprintf("原使用者在该 Bot 上的 %d 个游戏时长任务已停止，Bot 会恢复到任务前的挂卡状态。", n))
		}
		action := "bot_unassign"
		var to any
		if userID == nil {
			exec(tx, "DELETE FROM bot_assignments WHERE bot_id = ?", botID)
		} else {
			action, to = "bot_assign", *userID
			exec(tx, `INSERT INTO bot_assignments (bot_id, user_id, assigned_by, assigned_at) VALUES (?, ?, ?, ?)
				ON CONFLICT(bot_id) DO UPDATE SET user_id = excluded.user_id, assigned_by = excluded.assigned_by,
				assigned_at = excluded.assigned_at`, botID, *userID, admin.ID, t)
		}
		record(tx, action, "admin", auditOpt{UserID: admin.ID, IP: admin.IP, Target: fmt.Sprintf("bot:%d", botID),
			Detail: M{"bot": row.Str("name"), "to_user": to}})
	})
	unlock()
	unlock = nil
	if len(warnings) > 0 {
		sched.syncBot(botID)
	}
	invalidateFarming(row.Int("connection_id"))
	return warnings
}

func adminHealth() M {
	t := now()
	s := M(one(db, "SELECT holder, lease_until, last_tick_at, last_tick_ms, last_error, ticks FROM scheduler_state WHERE id = 1"))
	last := Row(s).Int("last_tick_at")
	s["healthy"] = last > 0 && t-last < int64(cfg.SchedulerInterval)*3
	s["interval_seconds"] = cfg.SchedulerInterval
	connStatus := M{}
	for _, r := range all(db, "SELECT CASE WHEN enabled = 0 THEN 'disabled' ELSE status END AS s, COUNT(*) AS n FROM asf_connections GROUP BY s") {
		connStatus[r.Str("s")] = M{"count": r.Int("n"), "text": statusLabel(r.Str("s"))}
	}
	tasks := M{}
	for _, r := range all(db, "SELECT status, COUNT(*) AS n FROM playtime_tasks GROUP BY status") {
		tasks[r.Str("status")] = r.Int("n")
	}
	shared := scalar(db, `SELECT COUNT(*) FROM (SELECT url_fingerprint FROM asf_connections GROUP BY url_fingerprint
		HAVING COUNT(DISTINCT user_id) > 1)`)
	day := t - 86400
	count := func(where string) int64 {
		return scalar(db, "SELECT COUNT(*) FROM audit_log WHERE created_at >= ? AND "+where, day)
	}
	conns := []M{}
	for _, r := range all(db, `SELECT c.id, c.name, u.username AS owner, CASE WHEN c.enabled = 0 THEN 'disabled' ELSE c.status END AS status,
		c.asf_version, c.last_ok_at, c.last_tested_at FROM asf_connections c JOIN users u ON u.id = c.user_id ORDER BY c.id`) {
		m := M(r)
		m["status_text"] = statusLabel(r.Str("status"))
		conns = append(conns, m)
	}
	return M{
		"scheduler": s, "connections": connStatus, "connections_list": conns, "shared_addresses": shared, "tasks": tasks,
		"bots_with_errors": scalar(db, "SELECT COUNT(*) FROM bot_play_state WHERE managed = 1 AND last_error IS NOT NULL"),
		"last_24h": M{
			"login_failed":           count("action = 'login' AND outcome != 'ok'"),
			"login_ok":               count("action = 'login' AND outcome = 'ok'"),
			"registrations":          count("action = 'register' AND outcome = 'ok'"),
			"register_failed":        count("action = 'register' AND outcome != 'ok'"),
			"connection_test_failed": count("action = 'connection_test' AND outcome != 'ok'"),
			"farming_action_failed":  count("category = 'farming' AND outcome != 'ok'"),
		},
	}
}

func adminAudit() []M {
	out := []M{}
	for _, r := range all(db, `SELECT a.created_at, a.action, a.category, a.target, a.outcome, a.ip, a.detail, u.username
		FROM audit_log a LEFT JOIN users u ON u.id = a.user_id
		WHERE a.category IN ('auth', 'admin', 'account') OR a.outcome != 'ok' ORDER BY a.id DESC LIMIT 200`) {
		out = append(out, M(r))
	}
	return out
}
