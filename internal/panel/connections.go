package panel

import (
	"fmt"
	"strconv"
	"strings"
)

var testLimit = [2]int64{12, 600}

func aad(userID int64, field string) string {
	return "asf_connection:" + strconv.FormatInt(userID, 10) + ":" + field
}

func ownerIsAdmin(userID int64) bool {
	r := one(db, "SELECT is_admin, is_active FROM users WHERE id = ?", userID)
	return r != nil && r.Bool("is_admin") && r.Bool("is_active")
}

func clientFor(crow Row) *AsfClient {
	uid := crow.Int("user_id")
	u := box.Decrypt(crow.Str("url_enc"), aad(uid, "url"))
	pw := ""
	if !crow.Null("password_enc") {
		pw = box.Decrypt(crow.Str("password_enc"), aad(uid, "password"))
	}
	return &AsfClient{Target: parseTarget(u, ownerIsAdmin(uid)), Password: pw}
}

func maskTarget(t Target) string {
	h := t.Host
	var masked string
	if len(h) <= 6 {
		masked = h[:1] + "***"
	} else {
		masked = h[:3] + "***" + h[len(h)-5:]
	}
	suffix := ""
	if t.Path != "" {
		suffix = "/…"
	}
	return fmt.Sprintf("%s://%s:%d%s", t.Scheme, masked, t.Port, suffix)
}

func validateConnName(name string) string {
	name = strings.TrimSpace(stripCtrl(name, ""))
	if n := runeLen(name); n < 1 || n > 40 {
		fail(400, "invalid_name", "连接名称需为 1–40 个字符。")
	}
	return name
}

func validateIPCPassword(pw string) {
	if runeLen(pw) > 256 || ctrlRe.MatchString(pw) {
		fail(400, "invalid_password", "IPC 密码不能超过 256 个字符，且不能包含控制字符。")
	}
}

func getOwnedConnection(uid, cid int64) Row {
	r := one(db, "SELECT * FROM asf_connections WHERE id = ? AND user_id = ?", cid, uid)
	if r == nil {
		panic(notFound("ASF 连接"))
	}
	return r
}

func connectionDTO(c Row) M {
	bots := []string{}
	for _, r := range all(db, "SELECT name FROM bots WHERE connection_id = ? AND present = 1 ORDER BY name", c.Int("id")) {
		bots = append(bots, r.Str("name"))
	}
	assigned := M{}
	for _, r := range all(db, `SELECT b.name, u.username FROM bot_assignments a JOIN bots b ON b.id = a.bot_id
		JOIN users u ON u.id = a.user_id WHERE b.connection_id = ? AND a.user_id != ?`, c.Int("id"), c.Int("user_id")) {
		assigned[r.Str("name")] = r.Str("username")
	}
	active := scalar(db, "SELECT COUNT(*) FROM playtime_tasks WHERE connection_id = ? AND status IN ('running','queued')", c.Int("id"))
	status := c.Str("status")
	if !c.Bool("enabled") {
		status = "disabled"
	}
	return M{
		"id": c.Int("id"), "name": c.Str("name"), "address_hint": c.Str("url_hint"), "has_password": !c.Null("password_enc"),
		"enabled": c.Bool("enabled"), "status": status, "status_text": statusLabel(status),
		"status_message": c["status_message"], "asf_version": c["asf_version"], "last_tested_at": c["last_tested_at"],
		"last_ok_at": c["last_ok_at"], "allow_user_bots": c.Bool("allow_user_bots"), "bots": bots, "assigned": assigned, "active_tasks": active, "created_at": c["created_at"],
	}
}

func listConnections(uid int64) []M {
	out := []M{}
	for _, r := range all(db, "SELECT * FROM asf_connections WHERE user_id = ? ORDER BY id", uid) {
		out = append(out, connectionDTO(r))
	}
	return out
}

func createConnection(u *CurrentUser, name, address, password string) M {
	if !u.IsAdmin && !cfg.AllowUserConnections {
		fail(403, "forbidden", "本站未开放自行添加 ASF 连接。", "请联系管理员为你分配 Bot。")
	}
	name = validateConnName(name)
	validateIPCPassword(password)
	t := parseTarget(address, u.IsAdmin)
	if scalar(db, "SELECT COUNT(*) FROM asf_connections WHERE user_id = ?", u.ID) >= int64(cfg.MaxConnectionsPerUser) {
		fail(409, "too_many", fmt.Sprintf("每个账户最多添加 %d 个 ASF 连接。", cfg.MaxConnectionsPerUser))
	}
	if one(db, "SELECT 1 FROM asf_connections WHERE user_id = ? AND name = ?", u.ID, name) != nil {
		fail(409, "name_taken", "你已经有同名的连接了，请换一个名称。")
	}
	var pwEnc any
	if password != "" {
		pwEnc = box.Encrypt(password, aad(u.ID, "password"))
	}
	var cid int64
	db.tx(func(tx Q) {
		ts := now()
		cid = lastID(exec(tx, `INSERT INTO asf_connections (user_id, name, url_enc, url_hint, url_fingerprint, password_enc,
			enabled, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 1, 'untested', ?, ?)`,
			u.ID, name, box.Encrypt(t.Display(), aad(u.ID, "url")), maskTarget(t),
			box.Fingerprint(fmt.Sprintf("%s:%d", t.Host, t.Port)), pwEnc, ts, ts))
		record(tx, "connection_create", "connection", auditOpt{UserID: u.ID, IP: u.IP,
			Target: fmt.Sprintf("connection:%d", cid), Detail: M{"name": name}})
	})
	return testConnection(u, getOwnedConnection(u.ID, cid))
}

func updateConnection(u *CurrentUser, cid int64, name, address, password *string, clearPassword bool) M {
	row := getOwnedConnection(u.ID, cid)
	sets := []string{}
	args := []any{}
	fields := []string{}
	changedTarget := false
	if name != nil && strings.TrimSpace(*name) != "" && strings.TrimSpace(*name) != row.Str("name") {
		n := validateConnName(*name)
		if one(db, "SELECT 1 FROM asf_connections WHERE user_id = ? AND name = ? AND id != ?", u.ID, n, cid) != nil {
			fail(409, "name_taken", "你已经有同名的连接了，请换一个名称。")
		}
		sets, args, fields = append(sets, "name = ?"), append(args, n), append(fields, "name")
	}
	if address != nil && strings.TrimSpace(*address) != "" {
		t := parseTarget(*address, u.IsAdmin)
		sets = append(sets, "url_enc = ?", "url_hint = ?", "url_fingerprint = ?")
		args = append(args, box.Encrypt(t.Display(), aad(u.ID, "url")), maskTarget(t), box.Fingerprint(fmt.Sprintf("%s:%d", t.Host, t.Port)))
		fields = append(fields, "url_hint")
		changedTarget = true
	}
	if clearPassword {
		sets, args = append(sets, "password_enc = ?"), append(args, nil)
		changedTarget = true
	} else if password != nil && *password != "" {
		validateIPCPassword(*password)
		sets, args = append(sets, "password_enc = ?"), append(args, box.Encrypt(*password, aad(u.ID, "password")))
		changedTarget = true
	}
	if len(sets) == 0 {
		return connectionDTO(row)
	}
	sets, args = append(sets, "updated_at = ?"), append(args, now())
	if changedTarget {
		sets = append(sets, "status = 'untested'", "status_message = NULL")
		fields = append(fields, "credentials")
	}
	exec(db, "UPDATE asf_connections SET "+strings.Join(sets, ", ")+" WHERE id = ? AND user_id = ?", append(args, cid, u.ID)...)
	record(db, "connection_update", "connection", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d", cid),
		Detail: M{"fields": fields}})
	row = getOwnedConnection(u.ID, cid)
	if changedTarget && row.Bool("enabled") {
		return testConnection(u, row)
	}
	return connectionDTO(row)
}

func testConnection(u *CurrentUser, row Row) M {
	if !rateHit(db, "conn_test", strconv.FormatInt(u.ID, 10), testLimit[0], testLimit[1]) {
		fail(429, "rate_limited", "测试过于频繁，请几分钟后再试。")
	}
	if !row.Bool("enabled") {
		fail(409, "connection_disabled", "这个连接已断开，请先重新启用。")
	}
	cid := row.Int("id")
	t := now()
	var version string
	var bots map[string]map[string]any
	if e := try(func() {
		c := clientFor(row)
		version = c.Version()
		bots = c.ListBots()
	}); e != nil {
		status := e.Code
		if _, ok := statusText[status]; !ok {
			status = "asf_error"
		}
		exec(db, "UPDATE asf_connections SET status = ?, status_message = ?, last_tested_at = ? WHERE id = ?",
			status, truncate(errHint(e), 300), t, cid)
		record(db, "connection_test", "connection", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d", cid), Outcome: status})
		return connectionDTO(getOwnedConnection(u.ID, cid))
	}
	var safe []string
	for n := range bots {
		if isSafeBotName(n) {
			safe = append(safe, n)
		}
	}
	var note any
	if len(bots) == 0 {
		note = "连接成功，但 ASF 中还没有 Bot。"
	} else if skipped := len(bots) - len(safe); skipped > 0 {
		note = fmt.Sprintf("有 %d 个 Bot 的名称包含空格、逗号等字符，本网站无法安全地操作它们，已忽略。", skipped)
	}
	if len(safe) > 200 {
		safe = safe[:200]
	}
	var ver any
	if version != "" {
		ver = version
	}

	var ids []int64
	for _, r := range all(db, "SELECT id FROM bots WHERE connection_id = ? AND present = 1", cid) {
		ids = append(ids, r.Int("id"))
	}
	ctx, cancel := adminLockCtx()
	defer cancel()
	unlock := lockBots(ctx, ids)
	defer unlock()
	db.tx(func(tx Q) {
		seen := map[string]bool{}
		for _, n := range safe {
			seen[n] = true
		}
		for _, r := range all(tx, "SELECT id, name FROM bots WHERE connection_id = ? AND present = 1", cid) {
			if !seen[r.Str("name")] {
				forgetBotTx(tx, r.Int("id"))
			}
		}
		for _, n := range safe {
			exec(tx, `INSERT INTO bots (connection_id, name, present, last_seen_at) VALUES (?, ?, 1, ?)
				ON CONFLICT(connection_id, name) DO UPDATE SET present = 1, last_seen_at = excluded.last_seen_at`, cid, n, t)
		}
		exec(tx, `UPDATE asf_connections SET status = 'ok', status_message = ?, asf_version = ?, last_tested_at = ?,
			last_ok_at = ? WHERE id = ?`, note, ver, t, t, cid)
		record(tx, "connection_test", "connection", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d", cid),
			Detail: M{"bots": len(safe), "version": version}})
	})
	invalidateFarming(cid)
	return connectionDTO(getOwnedConnection(u.ID, cid))
}

func setAllowUserBots(u *CurrentUser, cid int64, allow bool) M {
	if !u.IsAdmin {
		fail(403, "forbidden", "只有管理员可以修改这个设置。")
	}
	getOwnedConnection(u.ID, cid)
	exec(db, "UPDATE asf_connections SET allow_user_bots = ? WHERE id = ? AND user_id = ?", b2i(allow), cid, u.ID)
	record(db, "connection_user_bots", "admin", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d", cid), Detail: M{"allow": allow}})
	return connectionDTO(getOwnedConnection(u.ID, cid))
}

func disableUnderLocks(cid int64) {
	var ids []int64
	for _, r := range all(db, "SELECT id FROM bots WHERE connection_id = ?", cid) {
		ids = append(ids, r.Int("id"))
	}
	ctx, cancel := adminLockCtx()
	defer cancel()
	unlock := lockBots(ctx, ids)
	defer unlock()
	exec(db, "UPDATE asf_connections SET enabled = 0, status = 'disabled', updated_at = ? WHERE id = ?", now(), cid)
}

func releaseConnection(cid int64, reason string) []string {
	stopTasksWhere("connection_id = ?", []any{cid}, reason)
	return sched.releaseConnection(cid)
}

func setConnectionEnabled(u *CurrentUser, cid int64, enabled bool) M {
	row := getOwnedConnection(u.ID, cid)
	warnings := []string{}
	if enabled && !row.Bool("enabled") {
		exec(db, "UPDATE asf_connections SET enabled = 1, status = 'untested', updated_at = ? WHERE id = ?", now(), cid)
		record(db, "connection_enable", "connection", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d", cid)})
		return M{"connection": testConnection(u, getOwnedConnection(u.ID, cid)), "warnings": warnings}
	}
	if !enabled && row.Bool("enabled") {
		disableUnderLocks(cid)
		if failed := releaseConnection(cid, "connection_disabled"); len(failed) > 0 {
			warnings = append(warnings, fmt.Sprintf("未能从这些 Bot 的 ASF 配置中删掉面板加的游戏：%s。可以稍后重新启用连接，或在 ASF 中自行删除。", strings.Join(failed, ", ")))
		}
		record(db, "connection_disable", "connection", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d", cid)})
	}
	return M{"connection": connectionDTO(getOwnedConnection(u.ID, cid)), "warnings": warnings}
}

func deleteConnection(u *CurrentUser, cid int64) []string {
	row := getOwnedConnection(u.ID, cid)
	warnings := []string{}
	if row.Bool("enabled") {
		disableUnderLocks(cid)
		if failed := releaseConnection(cid, "connection_deleted"); len(failed) > 0 {
			warnings = append(warnings, fmt.Sprintf("连接已删除，但未能恢复这些 Bot 的挂卡状态：%s。请在 ASF 中自行处理。", strings.Join(failed, ", ")))
		}
	}
	exec(db, "DELETE FROM asf_connections WHERE id = ? AND user_id = ?", cid, u.ID)
	record(db, "connection_delete", "connection", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d", cid),
		Detail: M{"name": row.Str("name")}})
	invalidateFarming(cid)
	return warnings
}
