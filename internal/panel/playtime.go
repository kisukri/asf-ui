package panel

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const maxTargetHours = 99999

var taskStatusText = map[string]string{
	"queued": "排队中", "running": "运行中", "completed": "已完成", "stopped": "已停止", "failed": "失败",
}

var stopReasonText = map[string]string{
	"user":                 "你手动停止了任务",
	"target_reached":       "已达到目标时长，自动停止",
	"connection_disabled":  "所在 ASF 连接被断开",
	"connection_deleted":   "所在 ASF 连接被删除",
	"user_disabled":        "账户被管理员停用",
	"bot_missing":          "ASF 中已找不到这个 Bot",
	"unreachable_too_long": "长时间无法连接 ASF",
	"bot_reassigned":       "管理员收回或改分配了这个 Bot",
	"bot_deleted":          "Bot 已被删除",
}

var requestKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

const taskSelect = `
SELECT t.*, b.name AS bot_name, c.name AS connection_name, c.user_id AS connection_owner,
       s.managed, s.confirmed, s.last_online, s.last_playing_possible, s.last_error AS state_error, s.wait_reason
FROM playtime_tasks t
JOIN bots b ON b.id = t.bot_id
JOIN asf_connections c ON c.id = t.connection_id
LEFT JOIN bot_play_state s ON s.bot_id = t.bot_id`

func taskTiming(r Row) (string, string) {
	switch {
	case r.Str("status") == "queued":
		return "queued", "等待空位：这个 Bot 挂时长的游戏已满"
	case r.Str("status") != "running":
		return "done", ""
	case r.Bool("confirmed"):
		return "accruing", "计时中"
	case !r.Null("wait_reason"):
		return "paused", r.Str("wait_reason")
	}
	return "pending", "等待写入 ASF 配置"
}

func nullable(s string, ok bool) any {
	if !ok || s == "" {
		return nil
	}
	return s
}

func taskDTO(r Row, viewer int64) M {
	target := r.Float("target_seconds")
	unlimited := target == 0
	elapsed := r.Float("elapsed_seconds")
	if !unlimited {
		elapsed = math.Min(elapsed, target)
	}
	key, text := taskTiming(r)
	errMsg := r["last_error"]
	if errMsg == nil && r.Str("status") == "running" {
		errMsg = r["state_error"]
	}
	connName := r.Str("connection_name")
	if r.Int("connection_owner") != viewer {
		connName = sharedName
	}
	var progress, remaining any
	if !unlimited {
		progress = math.Round(elapsed/target*10000) / 10000
		remaining = int64(math.Max(0, target-elapsed))
	}
	reason, hasReason := stopReasonText[r.Str("stop_reason")]
	return M{
		"id": r.Int("id"), "type": "playtime",
		"connection": M{"id": r.Int("connection_id"), "name": connName},
		"bot":        r.Str("bot_name"), "app_id": r.Int("app_id"), "app_name": r["app_name"],
		"target_seconds": int64(target), "unlimited": unlimited, "elapsed_seconds": int64(elapsed),
		"remaining_seconds": remaining, "progress": progress,
		"status": r.Str("status"), "status_text": taskStatusText[r.Str("status")],
		"timing": key, "timing_text": text, "note": r["note"],
		"stop_reason": r["stop_reason"], "stop_reason_text": nullable(reason, hasReason),
		"last_error": errMsg, "created_at": r["created_at"], "updated_at": r["updated_at"],
		"started_at": r["started_at"], "finished_at": r["finished_at"],
	}
}

func getTask(uid, id int64) M {
	r := one(db, taskSelect+" WHERE t.id = ? AND t.user_id = ?", id, uid)
	if r == nil {
		panic(notFound("任务"))
	}
	return taskDTO(r, uid)
}

func listTasks(uid int64, status string, limit int) []M {
	if status != "" {
		if _, ok := taskStatusText[status]; !ok {
			fail(400, "invalid_status", "未知的任务状态筛选。")
		}
	}
	q := taskSelect + " WHERE t.user_id = ?"
	args := []any{uid}
	if status != "" {
		q += " AND t.status = ?"
		args = append(args, status)
	}
	q += " ORDER BY CASE t.status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END, t.updated_at DESC LIMIT ?"
	args = append(args, limit)
	out := []M{}
	for _, r := range all(db, q, args...) {
		out = append(out, taskDTO(r, uid))
	}
	return out
}

func taskCounts(uid int64) M {
	out := M{}
	for k := range taskStatusText {
		out[k] = int64(0)
	}
	for _, r := range all(db, "SELECT status, COUNT(*) AS n FROM playtime_tasks WHERE user_id = ? GROUP BY status", uid) {
		out[r.Str("status")] = r.Int("n")
	}
	return out
}

func targetSeconds(hours, minutes int) int64 {
	if hours < 0 || minutes < 0 || minutes > 59 || hours > maxTargetHours {
		fail(400, "invalid_duration", "时长格式不正确：分钟应在 0–59 之间。")
	}
	return int64(hours*60+minutes) * 60
}

func parseGames(games []string) []int64 { return parseGamesUpTo(games, maxPlayGames) }

func parseGamesUpTo(games []string, limit int) []int64 {
	var ids []int64
	seen := map[int64]bool{}
	sep := func(r rune) bool { return strings.ContainsRune(",，;； \n\r\t", r) }
	for _, g := range games {
		for _, part := range strings.FieldsFunc(g, sep) {
			id := parseAppID(part)
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		fail(400, "invalid_app_id", "请至少填写一个 App ID 或商店链接。")
	}
	if len(ids) > limit {
		fail(400, "too_many_games", fmt.Sprintf("一次最多 %d 个游戏。", limit))
	}
	return ids
}

func gameNames(ids []int64) map[int64]any { return gameNamesOpt(ids, 5) }

func previewTask(u *CurrentUser, cid int64, bot string, games []string, hours, minutes int) M {
	crow, brow := ownedBot(u.ID, cid, bot)
	ids := parseGames(games)
	target := targetSeconds(hours, minutes)
	running := scalar(db, "SELECT COUNT(*) FROM playtime_tasks WHERE bot_id = ? AND status = 'running'", brow.Int("id"))
	var cardsPending bool
	_ = try(func() {
		if s, ok := liveStatus(crow, false)[brow.Str("name")]; ok {
			f := s["farming"].(M)
			cardsPending = !f["paused"].(bool) && f["games_to_farm_count"].(int) > 0
		}
	})
	names := gameNames(ids)
	items := []M{}
	fresh := int64(0)
	for _, id := range ids {
		dup := one(db, "SELECT 1 FROM playtime_tasks WHERE bot_id = ? AND app_id = ? AND status IN ('running','queued')", brow.Int("id"), id) != nil
		if !dup {
			fresh++
		}
		items = append(items, M{"app_id": id, "app_name": names[id], "duplicate": dup})
	}
	return M{
		"connection": M{"id": crow.Int("id"), "name": displayName(crow, u.ID)}, "bot": brow.Str("name"),
		"items": items, "new_count": fresh, "target_seconds": target,
		"queued_count": max(0, running+fresh-maxPlayGames), "cards_pending": cardsPending,
		"other_running": running,
	}
}

func cleanNote(note *string) any {
	if note == nil {
		return nil
	}
	n := strings.TrimSpace(stripCtrl(*note, " "))
	if runeLen(n) > 200 {
		fail(400, "invalid_note", "备注不能超过 200 个字符。")
	}
	if n == "" {
		return nil
	}
	return n
}

func recheckTaskAccess(u *CurrentUser, cid int64, bot string, botID int64) {
	if userCanLogin(u.ID) == nil {
		fail(403, "account_disabled", "该账户已被停用。")
	}
	if u.SessionID != 0 && one(db, "SELECT 1 FROM sessions WHERE id = ?", u.SessionID) == nil {
		fail(401, "unauthenticated", "登录已过期，请重新登录。")
	}
	if _, brow := ownedBot(u.ID, cid, bot); brow.Int("id") != botID {
		panic(notFound("Bot"))
	}
}

func createTasks(ctx context.Context, u *CurrentUser, cid int64, bot string, games []string, hours, minutes int, note *string, requestKey string, pauseFarming bool) M {
	if !requestKeyRe.MatchString(requestKey) {
		fail(400, "invalid_request", "请求标识无效，请刷新页面后重试。")
	}
	prefix := requestKey + "-"
	if ex := all(db, "SELECT id FROM playtime_tasks WHERE user_id = ? AND request_key >= ? AND request_key < ? ORDER BY id",
		u.ID, prefix, requestKey+"."); len(ex) > 0 {
		tasks := []M{}
		for _, r := range ex {
			tasks = append(tasks, getTask(u.ID, r.Int("id")))
		}
		return M{"tasks": tasks, "skipped": []int64{}}
	}
	crow, brow := ownedBot(u.ID, cid, bot)
	ids := parseGames(games)
	target := targetSeconds(hours, minutes)
	n := cleanNote(note)
	names := gameNames(ids)
	var created []int64
	skipped := []int64{}
	if ctx == nil {
		ctx = context.Background()
	}

	unlock := lockBot(ctx, brow.Int("id"))
	func() {
		defer unlock()
		recheckTaskAccess(u, cid, bot, brow.Int("id"))
		db.tx(func(tx Q) {
			running := scalar(tx, "SELECT COUNT(*) FROM playtime_tasks WHERE bot_id = ? AND status = 'running'", brow.Int("id"))
			t := now()
			for _, id := range ids {
				if one(tx, "SELECT 1 FROM playtime_tasks WHERE bot_id = ? AND app_id = ? AND status IN ('running','queued')", brow.Int("id"), id) != nil {
					skipped = append(skipped, id)
					continue
				}
				status := "running"
				var started any = t
				if running >= maxPlayGames {
					status, started = "queued", nil
				} else {
					running++
				}
				taskID := lastID(exec(tx, `INSERT INTO playtime_tasks (user_id, connection_id, bot_id, app_id, app_name, target_seconds, status,
				note, request_key, created_at, updated_at, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					u.ID, crow.Int("id"), brow.Int("id"), id, names[id], target, status, n, prefix+strconv.FormatInt(id, 10), t, t, started))
				created = append(created, taskID)
				record(tx, "playtime_create", "playtime", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("task:%d", taskID),
					Detail: M{"bot": brow.Str("name"), "app_id": id, "target_seconds": target, "status": status}})
			}
		})
	}()
	if len(created) == 0 {
		fail(409, "duplicate_task", "这些游戏在这个 Bot 上都已经有进行中的任务了。")
	}
	if pauseFarming {
		_ = try(func() {
			if live, ok := liveStatus(crow, true)[brow.Str("name")]; ok && !live["farming"].(M)["paused"].(bool) {
				clientFor(crow).PauseFarming(brow.Str("name"), true, 0)
			}
		})
		record(db, "farm_stop", "farming", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d/%s", cid, brow.Str("name"))})
		invalidateFarming(cid)
	}
	sched.syncBot(brow.Int("id"))
	tasks := []M{}
	for _, id := range created {
		tasks = append(tasks, getTask(u.ID, id))
	}
	return M{"tasks": tasks, "skipped": skipped}
}

func stopTasks(u *CurrentUser, ids []int64) []M {
	if len(ids) == 0 || len(ids) > 100 {
		fail(400, "invalid_request", "请选择 1–100 个任务。")
	}
	botIDs := map[int64]bool{}
	seen := map[int64]bool{}
	results := []M{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r := one(db, "SELECT id, bot_id, status FROM playtime_tasks WHERE id = ? AND user_id = ?", id, u.ID)
		if r == nil {
			results = append(results, M{"id": id, "ok": false, "message": "任务不存在或你无权访问"})
			continue
		}
		t := now()
		res := exec(db, `UPDATE playtime_tasks SET status = 'stopped', stop_reason = 'user', finished_at = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND status IN ('running','queued')`, t, t, id, u.ID)
		if affected(res) > 0 {
			botIDs[r.Int("bot_id")] = true
			record(db, "playtime_stop", "playtime", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("task:%d", id)})
			results = append(results, M{"id": id, "ok": true, "message": "已停止"})
		} else {
			results = append(results, M{"id": id, "ok": true, "message": fmt.Sprintf("任务已是“%s”状态", taskStatusText[r.Str("status")])})
		}
	}
	for b := range botIDs {
		sched.syncBot(b)
	}
	return results
}

func stopTasksWhere(where string, args []any, reason string) []int64 {
	t := now()
	var bots []int64
	for _, r := range all(db, "SELECT DISTINCT bot_id FROM playtime_tasks WHERE status IN ('running','queued') AND "+where, args...) {
		bots = append(bots, r.Int("bot_id"))
	}
	exec(db, "UPDATE playtime_tasks SET status = 'stopped', stop_reason = ?, finished_at = ?, updated_at = ? WHERE status IN ('running','queued') AND "+where,
		append([]any{reason, t, t}, args...)...)
	return bots
}
