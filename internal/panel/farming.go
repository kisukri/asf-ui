package panel

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

const farmCacheTTL = 10 * time.Second

type farmCacheEntry struct {
	at   time.Time
	data map[string]M
}

var (
	farmCache   = map[int64]farmCacheEntry{}
	farmCacheMu sync.Mutex
)

var farmActions = map[string]bool{"farm_start": true, "farm_stop": true, "farm_pause_temp": true, "bot_start": true, "bot_stop": true}

func invalidateFarming(cid int64) {
	farmCacheMu.Lock()
	delete(farmCache, cid)
	farmCacheMu.Unlock()
}

func liveStatus(crow Row, force bool) map[string]M {
	cid := crow.Int("id")
	farmCacheMu.Lock()
	if e, ok := farmCache[cid]; ok && !force && time.Since(e.at) < farmCacheTTL {
		farmCacheMu.Unlock()
		return e.data
	}
	farmCacheMu.Unlock()
	var names []string
	for _, r := range all(db, "SELECT name FROM bots WHERE connection_id = ? AND present = 1 ORDER BY name", cid) {
		names = append(names, r.Str("name"))
	}
	data := map[string]M{}
	if len(names) > 0 {
		raw := clientFor(crow).GetBots(names)
		for _, n := range names {
			if b, ok := raw[n]; ok {
				data[n] = summarizeBot(n, b)
			}
		}
	}
	farmCacheMu.Lock()
	farmCache[cid] = farmCacheEntry{at: time.Now(), data: data}
	farmCacheMu.Unlock()
	return data
}

func farmState(s M) (string, string) {
	f := s["farming"].(M)
	idle := len(s["idle_games"].([]int64)) > 0
	switch {
	case s["needs_input"].(bool):
		return "needs_input", "需要登录验证"
	case !s["online"].(bool):
		return "offline", "离线"
	case !s["playing_possible"].(bool):
		return "occupied", "账号正在其他地方游戏"
	case f["now_farming"].(bool) && idle:
		return "farming", "挂卡中（挂完后挂时长）"
	case !f["now_farming"].(bool) && idle:

		return "idling", "挂时长中"
	case f["paused"].(bool):
		return "stopped", "挂卡已停止"
	case f["now_farming"].(bool):
		return "farming", "挂卡中"
	case f["games_to_farm_count"].(int) == 0:
		return "idle", "没有需要挂卡的游戏"
	}
	return "waiting", "等待开始"
}

func farmingOverview(u *CurrentUser, force bool) []M {
	out := []M{}
	for _, c := range accessibleConnections(u.ID) {
		cid := c.Int("id")
		allowed := visibleBotNames(u.ID, cid)
		item := M{"connection": M{"id": cid, "name": displayName(c, u.ID), "enabled": c.Bool("enabled"),
			"owned": c.Int("user_id") == u.ID}, "bots": []M{}, "error": nil}
		if !c.Bool("enabled") {
			hint := "请联系管理员。"
			if c.Int("user_id") == u.ID {
				hint = "在“ASF 连接”中重新启用后可查看状态。"
			}
			item["error"] = M{"message": "连接已断开", "hint": hint}
			out = append(out, item)
			continue
		}
		var live map[string]M
		if e := try(func() { live = liveStatus(c, force) }); e != nil {
			item["error"] = M{"message": e.Message, "hint": e.Hint}
			out = append(out, item)
			continue
		}
		meta := map[string]Row{}
		for _, r := range all(db, `SELECT b.name, b.skip_count, b.skip_checked_at, b.created_by, s.managed, s.prev_farming_paused,
			(SELECT COUNT(*) FROM playtime_tasks t WHERE t.bot_id = b.id AND t.status IN ('running','queued')) AS tasks
			FROM bots b LEFT JOIN bot_play_state s ON s.bot_id = b.id WHERE b.connection_id = ? AND b.present = 1`, cid) {
			meta[r.Str("name")] = r
		}
		assigned := map[string]string{}
		if adminOwns(u, c) {
			assigned = assignedNames(cid)
		}
		bots := []M{}
		missing := []string{}
		for _, name := range allowed {
			s, ok := live[name]
			if !ok {
				missing = append(missing, name)
				continue
			}
			m := meta[name]
			key, text := farmState(s)
			bot := M{}
			for k, v := range s {
				bot[k] = v
			}
			bot["state"], bot["state_text"] = key, text
			bot["playtime"] = M{"managed": false, "playtime_tasks": m.Int("tasks")}
			bot["skip"] = M{"count": m["skip_count"], "checked_at": m["skip_checked_at"]}
			bot["deletable"] = canDeleteBot(u, c, m)
			bot["idle_games"] = withNames(s["idle_games"].([]int64))
			bot["assigned_to"] = nullable(assigned[name], true)
			bots = append(bots, bot)
		}
		item["bots"] = bots
		if len(missing) > 0 {
			item["missing_bots"] = missing
		}
		out = append(out, item)
	}
	return out
}

type botRef struct {
	ConnectionID int64  `json:"connection_id"`
	Bot          string `json:"bot"`
}

func farmingPerform(u *CurrentUser, items []botRef, action string, minutes *int) []M {
	if !farmActions[action] {
		fail(400, "invalid_action", "不支持的操作。")
	}
	if len(items) == 0 || len(items) > 50 {
		fail(400, "invalid_request", "请选择 1–50 个 Bot。")
	}
	seconds := 0
	if action == "farm_pause_temp" {
		if minutes == nil || *minutes < 1 || *minutes > 1092 {
			fail(400, "invalid_duration", "暂停时长需在 1–1092 分钟之间。")
		}
		seconds = *minutes * 60
	}
	results := []M{}
	touched := map[int64]bool{}
	seen := map[botRef]bool{}
	for _, it := range items {
		if seen[it] {
			continue
		}
		seen[it] = true
		label := it.Bot
		target := fmt.Sprintf("connection:%d/%s", it.ConnectionID, it.Bot)
		var msg string
		e := try(func() {
			crow, brow := ownedBot(u.ID, it.ConnectionID, it.Bot)
			label = displayName(crow, u.ID) + " / " + it.Bot
			client := clientFor(crow)
			managed := false
			live, ok := liveStatus(crow, true)[it.Bot]
			if !ok {
				fail(404, "bot_missing", "ASF 中找不到这个 Bot。", "请在“ASF 连接”中重新测试以刷新 Bot 列表。")
			}
			msg = doFarmAction(client, brow, action, managed, live, seconds)
		})
		if e != nil {
			results = append(results, M{"connection_id": it.ConnectionID, "bot": it.Bot, "label": label, "ok": false,
				"message": e.Message, "hint": e.Hint})
			record(db, action, "farming", auditOpt{UserID: u.ID, IP: u.IP, Target: target, Outcome: e.Code})
			continue
		}
		results = append(results, M{"connection_id": it.ConnectionID, "bot": it.Bot, "label": label, "ok": true, "message": msg})
		var detail M
		if minutes != nil {
			detail = M{"minutes": *minutes}
		}
		record(db, action, "farming", auditOpt{UserID: u.ID, IP: u.IP, Target: target, Detail: detail})
		touched[it.ConnectionID] = true
	}
	for cid := range touched {
		invalidateFarming(cid)
	}
	return results
}

func doFarmAction(client *AsfClient, brow Row, action string, managed bool, live M, seconds int) string {
	name := brow.Str("name")
	paused := live["farming"].(M)["paused"].(bool)
	online, keepRunning := live["online"].(bool), live["keep_running"].(bool)
	switch action {
	case "farm_start":
		if managed {
			exec(db, "UPDATE bot_play_state SET prev_farming_paused = 0 WHERE bot_id = ?", brow.Int("id"))
			return "该 Bot 正在执行游戏时长任务；已记录：任务全部结束后自动恢复挂卡。"
		}
		if !online {
			fail(409, "bot_offline", "Bot 离线，无法启动挂卡。", "请先启动 Bot。")
		}
		if !paused {
			return "挂卡本来就在运行中，无需操作。"
		}
		client.ResumeFarming(name)
		return "已发送启动挂卡指令。"
	case "farm_stop":
		if managed {
			exec(db, "UPDATE bot_play_state SET prev_farming_paused = 1 WHERE bot_id = ?", brow.Int("id"))
			return "该 Bot 正在执行游戏时长任务（挂卡已暂停）；已记录：任务结束后保持停止挂卡。"
		}
		if paused {
			return "挂卡本来就是停止状态，无需操作。"
		}
		client.PauseFarming(name, true, 0)
		return "已停止挂卡。ASF 重启后会按 Bot 自身配置重新开始挂卡。"
	case "farm_pause_temp":
		if managed {
			fail(409, "playtime_active", "该 Bot 正在执行游戏时长任务，不能临时暂停。")
		}
		if paused {
			fail(409, "already_paused", "挂卡已处于停止/暂停状态。")
		}
		client.PauseFarming(name, false, seconds)
		return fmt.Sprintf("已暂停挂卡，约 %d 分钟后由 ASF 自动继续。", seconds/60)
	case "bot_start":
		if online || keepRunning {
			return "Bot 已在运行或正在连接中。"
		}
		client.StartBot(name)
		return "已发送启动 Bot 指令。若需要 Steam 令牌或验证码，请在 ASF 中自行完成，本网站不收集这些信息。"
	case "bot_stop":
		if managed {
			fail(409, "playtime_active", "该 Bot 有进行中的游戏时长任务，请先停止这些任务。")
		}
		if !keepRunning && !online {
			return "Bot 已经是停止状态。"
		}
		client.StopBot(name)
		return "已停止 Bot（Steam 账号会从 ASF 登出）。"
	}
	fail(400, "invalid_action", "不支持的操作。")
	return ""
}

func skipScope(u *CurrentUser, cid int64, bot string) (Row, []Row) {
	if bot != "" {
		crow, brow := ownedBot(u.ID, cid, bot)
		return crow, []Row{brow}
	}
	crow := getAccessibleConnection(u.ID, cid)
	if !crow.Bool("enabled") {
		fail(409, "connection_disabled", "这个连接已断开，暂时无法操作。")
	}
	names := visibleBotNames(u.ID, cid)
	if len(names) > 30 {
		names = names[:30]
	}
	if len(names) == 0 {
		fail(404, "no_bots", "这个连接下没有你可以操作的 Bot。")
	}
	args := []any{cid}
	for _, n := range names {
		args = append(args, n)
	}
	return crow, all(db, "SELECT * FROM bots WHERE connection_id = ? AND name IN ("+placeholders(len(names))+") ORDER BY name", args...)
}

func rememberSkip(botID int64, ids []int64) {
	exec(db, "UPDATE bots SET skip_count = ?, skip_checked_at = ? WHERE id = ?", len(ids), now(), botID)
}

func withNames(ids []int64) []M {
	names := cachedNames(db, ids)
	out := []M{}
	for _, id := range ids {
		var n any
		if v, ok := names[id]; ok {
			n = v
		}
		out = append(out, M{"app_id": id, "name": n})
	}
	return out
}

func scopeName(bot string) string {
	if bot != "" {
		return "bot"
	}
	return "connection"
}

func skipGet(u *CurrentUser, cid int64, bot string) M {
	crow, bots := skipScope(u, cid, bot)
	client := clientFor(crow)
	out := []M{}
	for _, b := range bots {
		var ids []int64
		if e := try(func() { ids = client.Blacklist(b.Str("name")) }); e != nil {
			out = append(out, M{"bot": b.Str("name"), "apps": []M{}, "error": e.Message})
			continue
		}
		rememberSkip(b.Int("id"), ids)
		out = append(out, M{"bot": b.Str("name"), "apps": withNames(ids), "error": nil})
	}
	return M{"connection": M{"id": crow.Int("id"), "name": displayName(crow, u.ID)}, "scope": scopeName(bot), "bots": out}
}

func skipModify(u *CurrentUser, cid int64, bot, op string, games []string) M {
	if op != "add" && op != "remove" {
		fail(400, "invalid_action", "不支持的操作。")
	}
	if len(games) == 0 || len(games) > 50 {
		fail(400, "invalid_request", "每次可添加或移除 1–50 个游戏。")
	}
	set := map[int64]bool{}
	for _, g := range games {
		set[parseAppID(g)] = true
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	crow, bots := skipScope(u, cid, bot)
	client := clientFor(crow)
	if op == "add" {
		for i, a := range ids {
			if i >= 5 {
				break
			}
			lookupName(db, a)
		}
	}
	results := []M{}
	for _, b := range bots {
		var current []int64
		if e := try(func() {
			if op == "add" {
				client.BlacklistAdd(b.Str("name"), ids)
			} else {
				client.BlacklistRemove(b.Str("name"), ids)
			}
			current = client.Blacklist(b.Str("name"))
		}); e != nil {
			results = append(results, M{"bot": b.Str("name"), "ok": false, "message": e.Message})
			continue
		}
		rememberSkip(b.Int("id"), current)
		results = append(results, M{"bot": b.Str("name"), "ok": true, "apps": withNames(current)})
	}
	target := fmt.Sprintf("connection:%d/%s", cid, bot)
	if bot == "" {
		target = fmt.Sprintf("connection:%d/*", cid)
	}
	record(db, "skip_"+op, "farming", auditOpt{UserID: u.ID, IP: u.IP, Target: target, Detail: M{"app_ids": ids}})
	invalidateFarming(cid)
	return M{"scope": scopeName(bot), "app_ids": ids, "results": results}
}
