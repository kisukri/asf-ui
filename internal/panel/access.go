package panel

const sharedName = "管理员提供的 ASF"

const visibleSQL = `
SELECT b.id AS bot_id, b.name AS bot_name, b.connection_id, c.user_id AS owner_id
FROM bots b
JOIN asf_connections c ON c.id = b.connection_id
JOIN users o ON o.id = c.user_id
LEFT JOIN bot_assignments a ON a.bot_id = b.id
WHERE b.present = 1 AND (
    (c.user_id = ? AND (a.user_id IS NULL OR a.user_id = ?))
    OR (a.user_id = ? AND c.user_id != ? AND o.is_admin = 1 AND o.is_active = 1)
)`

func visibleArgs(uid int64) []any { return []any{uid, uid, uid, uid} }

func displayName(crow Row, uid int64) string {
	if crow.Int("user_id") == uid {
		return crow.Str("name")
	}
	return sharedName
}

func visibleBotNames(uid, connectionID int64) []string {
	out := []string{}
	for _, r := range all(db, visibleSQL+" AND b.connection_id = ? ORDER BY b.name", append(visibleArgs(uid), connectionID)...) {
		out = append(out, r.Str("bot_name"))
	}
	return out
}

func accessibleConnections(uid int64) []Row {
	return all(db, `SELECT * FROM asf_connections WHERE user_id = ? OR id IN (
		SELECT b.connection_id FROM bot_assignments a JOIN bots b ON b.id = a.bot_id
		JOIN asf_connections c ON c.id = b.connection_id JOIN users o ON o.id = c.user_id
		WHERE a.user_id = ? AND b.present = 1 AND o.is_admin = 1 AND o.is_active = 1)
		ORDER BY (user_id = ?) DESC, id`, uid, uid, uid)
}

func getAccessibleConnection(uid, connectionID int64) Row {
	for _, r := range accessibleConnections(uid) {
		if r.Int("id") == connectionID {
			return r
		}
	}
	panic(notFound("ASF 连接"))
}

func ownedBot(uid, connectionID int64, botName string) (Row, Row) {
	if !isSafeBotName(botName) {
		panic(notFound("Bot"))
	}
	r := one(db, visibleSQL+" AND b.connection_id = ? AND b.name = ?", append(visibleArgs(uid), connectionID, botName)...)
	if r == nil {
		fail(404, "not_found", "没有这个 Bot，或你无权使用它。", "如果刚在 ASF 中添加了 Bot，请让连接的拥有者重新测试连接以刷新列表。")
	}
	crow := one(db, "SELECT * FROM asf_connections WHERE id = ?", connectionID)
	if !crow.Bool("enabled") {
		fail(409, "connection_disabled", "这个 ASF 连接已断开，暂时无法操作。")
	}
	brow := one(db, "SELECT * FROM bots WHERE id = ?", r.Int("bot_id"))
	return crow, brow
}

func targets(uid int64) []M {
	out := []M{}
	for _, c := range accessibleConnections(uid) {
		status := c.Str("status")
		if !c.Bool("enabled") {
			status = "disabled"
		}
		out = append(out, M{
			"connection_id": c.Int("id"), "name": displayName(c, uid), "owned": c.Int("user_id") == uid,
			"enabled": c.Bool("enabled"), "status": status, "status_text": statusLabel(status),
			"bots": visibleBotNames(uid, c.Int("id")),
		})
	}
	return out
}
