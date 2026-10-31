package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

var onlineStatusText = []string{"离线", "在线", "忙碌", "离开", "打盹", "想交易", "想玩游戏", "隐身"}

func confString(conf map[string]any, key string) string {
	s, _ := conf[key].(string)
	return s
}

func confStatus(conf map[string]any) int {
	if f, ok := num(conf["OnlineStatus"]); ok && f >= 0 && int(f) < len(onlineStatusText) {
		return int(f)
	}
	return 1
}

func confGames(conf map[string]any) []int64 {
	out := []int64{}
	list, _ := conf["GamesPlayedWhileIdle"].([]any)
	for _, g := range list {
		if f, ok := num(g); ok && f > 0 {
			out = append(out, int64(f))
		}
	}
	return out
}

// custom idle text eats one of steam's 32 slots
func idleCapacity(conf map[string]any) int {
	if strings.TrimSpace(confString(conf, "CustomGamePlayedWhileIdle")) != "" {
		return maxGamesPlayedConcurrently - 1
	}
	return maxGamesPlayedConcurrently
}

type configOps struct {
	UserID      int64          `json:"user_id"`
	Remove      []int64        `json:"remove,omitempty"`
	Add         []int64        `json:"add,omitempty"`
	Set         map[string]any `json:"set,omitempty"`
	StopTaskIDs []int64        `json:"stop_task_ids,omitempty"`
	At          int64          `json:"at"`
}

func sameValue(cur, want any) bool {
	if cs, ok := want.(string); ok || want == nil {
		c, _ := cur.(string)
		return c == cs
	}
	a, ok1 := num(cur)
	b, ok2 := num(toFloat(want))
	return ok1 && ok2 && a == b
}

func toFloat(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return v
}

func applyOps(conf map[string]any, ops configOps) map[string]any {
	cur := confGames(conf)
	rm := map[int64]bool{}
	for _, g := range ops.Remove {
		rm[g] = true
	}
	list := []int64{}
	have := map[int64]bool{}
	for _, g := range cur {
		if !rm[g] {
			list = append(list, g)
			have[g] = true
		}
	}
	for _, g := range ops.Add {
		if !have[g] {
			list = append(list, g)
			have[g] = true
		}
	}
	changes := map[string]any{}
	if !equalIDs(list, cur) {
		changes["GamesPlayedWhileIdle"] = list
	}
	for k, v := range ops.Set {
		if !sameValue(conf[k], v) {
			changes[k] = v
		}
	}
	return changes
}

func checkFinalCapacity(final map[string]any) *AppError {
	n, capacity := len(confGames(final)), idleCapacity(final)
	if n <= capacity {
		return nil
	}
	msg := fmt.Sprintf("这个 Bot 最多挂 %d 个游戏，修改后会有 %d 个。", capacity, n)
	if capacity < maxGamesPlayedConcurrently {
		msg = fmt.Sprintf("设置了挂时长显示文字时最多 %d 个游戏（文字占一个位置），修改后会有 %d 个。", capacity, n)
	}
	e := newErr(400, "too_many_games", msg)
	e.Extra = M{"count": n, "capacity": capacity}
	return e
}

func mergedConf(conf, changes map[string]any) map[string]any {
	out := make(map[string]any, len(conf)+len(changes))
	for k, v := range conf {
		out[k] = v
	}
	for k, v := range changes {
		if ids, ok := v.([]int64); ok {
			arr := make([]any, len(ids))
			for i, id := range ids {
				arr[i] = float64(id)
			}
			v = arr
		}
		out[k] = v
	}
	return out
}

func finalizeOps(botID int64, ops configOps) {
	db.tx(func(tx Q) {
		t := now()
		if len(ops.StopTaskIDs) > 0 {
			args := []any{ops.UserID, t, t, botID}
			for _, id := range ops.StopTaskIDs {
				args = append(args, id)
			}
			exec(tx, `UPDATE playtime_tasks SET status = 'stopped',
				stop_reason = CASE WHEN user_id = ? THEN 'user' ELSE 'removed_by_other' END, finished_at = ?, updated_at = ?
				WHERE bot_id = ? AND status IN ('running','queued') AND id IN (`+placeholders(len(ops.StopTaskIDs))+`)`, args...)
		}
		st := one(tx, "SELECT applied_appids FROM bot_play_state WHERE bot_id = ?", botID)
		gone := map[int64]bool{}
		for _, g := range ops.Remove {
			gone[g] = true
		}
		var left []int64
		if st != nil {
			for _, g := range parseApplied(st.Str("applied_appids")) {
				if !gone[g] {
					left = append(left, g)
				}
			}
		}
		exec(tx, "UPDATE bot_play_state SET applied_appids = ?, managed = ?, pending_ops = NULL, updated_at = ? WHERE bot_id = ?",
			joinInts(left, ","), b2i(len(left) > 0), t, botID)
	})
}

func isUncertain(e *AppError) bool {
	switch e.Code {
	case "timeout", "unreachable", "bad_response", "canceled":
		return true
	}
	return false
}

func userCanLogin(uid int64) *CurrentUser {
	r := one(db, "SELECT id, username, is_admin FROM users WHERE id = ? AND is_active = 1", uid)
	if r == nil {
		return nil
	}
	return &CurrentUser{ID: r.Int("id"), Username: r.Str("username"), IsAdmin: r.Bool("is_admin")}
}

func resolvePending(client *AsfClient, crow, brow Row) *AppError {
	st := one(db, "SELECT pending_ops FROM bot_play_state WHERE bot_id = ?", brow.Int("id"))
	if st == nil || st.Null("pending_ops") {
		return nil
	}
	var ops configOps
	if json.Unmarshal([]byte(st.Str("pending_ops")), &ops) != nil {
		exec(db, "UPDATE bot_play_state SET pending_ops = NULL WHERE bot_id = ?", brow.Int("id"))
		return nil
	}

	if owner := userCanLogin(ops.UserID); owner == nil || try(func() { loginTarget(owner, crow.Int("id"), brow.Str("name")) }) != nil {
		exec(db, "UPDATE bot_play_state SET pending_ops = NULL WHERE bot_id = ?", brow.Int("id"))
		return nil
	}
	return try(func() {
		raw, ok := client.GetBots([]string{brow.Str("name")})[brow.Str("name")]
		if !ok {
			panic(notFound("Bot"))
		}
		conf, _ := raw["BotConfig"].(map[string]any)
		if conf == nil {
			panic(newErr(502, "bad_response", "读不到这个 Bot 的 ASF 配置。"))
		}
		if changes := applyOps(conf, ops); len(changes) > 0 {

			if s := summarizeBot(brow.Str("name"), raw); !s["online"].(bool) && !s["keep_running"].(bool) {
				return
			}

			if e := checkFinalCapacity(mergedConf(conf, changes)); e != nil {
				exec(db, "UPDATE bot_play_state SET pending_ops = NULL, updated_at = ? WHERE bot_id = ?", now(), brow.Int("id"))
				record(db, "bot_settings", "playtime", auditOpt{UserID: ops.UserID, Outcome: "dropped",
					Target: fmt.Sprintf("connection:%d/%s", crow.Int("id"), brow.Str("name")),
					Detail: M{"reason": e.Code, "count": e.Extra["count"], "capacity": e.Extra["capacity"]}})
				return
			}
			if e := try(func() { client.SetConfig(brow.Str("name"), conf, changes) }); e != nil {
				if !isUncertain(e) {
					exec(db, "UPDATE bot_play_state SET pending_ops = NULL WHERE bot_id = ?", brow.Int("id"))
				}
				panic(e)
			}
		}
		finalizeOps(brow.Int("id"), ops)
		invalidateFarming(crow.Int("id"))
	})
}

func settingsView(u *CurrentUser, cid int64, bot string, raw map[string]any, lookup bool) M {
	conf, _ := raw["BotConfig"].(map[string]any)
	s := summarizeBot(bot, raw)
	brow := one(db, "SELECT id FROM bots WHERE connection_id = ? AND name = ?", cid, bot)
	tasks := map[int64]M{}
	if brow != nil {
		for _, r := range all(db, taskSelect+" WHERE t.bot_id = ? AND t.status IN ('running','queued')", brow.Int("id")) {
			tasks[r.Int("app_id")] = taskDTO(r, u.ID)
		}
	}
	ids := confGames(conf)
	budget := 0
	if lookup {
		budget = 5
	}
	names := gameNamesOpt(ids, budget)
	games := []M{}
	for _, id := range ids {
		var task any
		if tk, ok := tasks[id]; ok {
			task = M{"id": tk["id"], "unlimited": tk["unlimited"], "target_seconds": tk["target_seconds"], "elapsed_seconds": tk["elapsed_seconds"]}
		}
		games = append(games, M{"app_id": id, "name": names[id], "task": task})
	}
	pending := false
	if brow != nil {
		if st := one(db, "SELECT pending_ops FROM bot_play_state WHERE bot_id = ?", brow.Int("id")); st != nil && !st.Null("pending_ops") {
			pending = true
		}
	}
	return M{
		"connection_id": cid, "bot": bot, "online": s["online"], "keep_running": s["keep_running"], "games": games,
		"capacity": idleCapacity(conf), "online_status": confStatus(conf),
		"custom_idle": confString(conf, "CustomGamePlayedWhileIdle"), "custom_farming": confString(conf, "CustomGamePlayedWhileFarming"),
		"pending": pending,
	}
}

func botSettings(ctx context.Context, u *CurrentUser, cid int64, bot string) M {
	crow := loginTarget(u, cid, bot)
	client := clientFor(crow)
	client.Ctx = ctx
	raw, ok := client.GetBots([]string{bot})[bot]
	if !ok {
		panic(notFound("Bot"))
	}
	return settingsView(u, cid, bot, raw, true)
}

type statusChange struct {
	Value int `json:"value"`
	Base  int `json:"base"`
}

type textChange struct {
	Value string `json:"value"`
	Base  string `json:"base"`
}

type botSettingsIn struct {
	ConnectionID  int64         `json:"connection_id"`
	Bot           string        `json:"bot"`
	AddGames      []string      `json:"add_games"`
	RemoveGames   []int64       `json:"remove_games"`
	OnlineStatus  *statusChange `json:"online_status"`
	CustomIdle    *textChange   `json:"custom_idle"`
	CustomFarming *textChange   `json:"custom_farming"`
	ConfirmStop   []int64       `json:"confirm_stop_task_ids"`
}

func cleanCustom(s string, field string) string {
	s = strings.TrimSpace(s)
	if runeLen(s) > 64 || ctrlRe.MatchString(s) {
		panic(invalid(field))
	}
	return s
}

func recheckAccess(u *CurrentUser, cid int64, bot string) Row {
	if userCanLogin(u.ID) == nil {
		fail(403, "account_disabled", "该账户已被停用。")
	}
	if u.SessionID != 0 && one(db, "SELECT 1 FROM sessions WHERE id = ?", u.SessionID) == nil {
		fail(401, "unauthenticated", "登录已过期，请重新登录。")
	}
	return loginTarget(u, cid, bot)
}

func saveBotSettings(ctx context.Context, u *CurrentUser, in botSettingsIn) M {
	loginTarget(u, in.ConnectionID, in.Bot)
	brow := one(db, "SELECT id FROM bots WHERE connection_id = ? AND name = ?", in.ConnectionID, in.Bot)
	if brow == nil {
		panic(notFound("Bot"))
	}
	if !rateHit(db, "bot_settings", fmt.Sprint(u.ID), 30, 600) {
		fail(429, "rate_limited", "保存过于频繁，请稍后再试。")
	}
	releaseSlot := acquireSettingsSlot(ctx)
	defer releaseSlot()
	unlock := lockBot(ctx, brow.Int("id"))
	defer unlock()

	crow := recheckAccess(u, in.ConnectionID, in.Bot)
	botState(brow.Int("id"))
	client := clientFor(crow)
	client.Ctx = ctx
	if e := resolvePending(client, crow, brow); e != nil {
		fail(503, "pending", "上一次保存还没有确认完成，ASF 暂时连不上。请稍后再试。")
	}
	raw, ok := client.GetBots([]string{in.Bot})[in.Bot]
	if !ok {
		panic(notFound("Bot"))
	}
	s := summarizeBot(in.Bot, raw)
	if !s["online"].(bool) && !s["keep_running"].(bool) {
		fail(409, "bot_stopped", "这个 Bot 已停止，请先启动它再修改。", "修改配置会让 ASF 重新加载 Bot；停止状态下修改可能把它重新启动。")
	}
	conf, _ := raw["BotConfig"].(map[string]any)
	if conf == nil {
		fail(502, "asf_error", "读不到这个 Bot 的 ASF 配置。")
	}

	ops := configOps{UserID: u.ID, Set: map[string]any{}, At: now()}
	var conflicts []string
	if c := in.OnlineStatus; c != nil {
		if c.Value < 0 || c.Value >= len(onlineStatusText) {
			panic(invalid("online_status"))
		}
		if cur := confStatus(conf); c.Value != cur {
			if c.Base != cur {
				conflicts = append(conflicts, "在线状态")
			} else {
				ops.Set["OnlineStatus"] = c.Value
			}
		}
	}
	setText := func(c *textChange, key, field, label string) {
		if c == nil {
			return
		}
		v := cleanCustom(c.Value, field)
		if cur := confString(conf, key); v != cur {
			if strings.TrimSpace(c.Base) != cur {
				conflicts = append(conflicts, label)
			} else if v == "" {
				ops.Set[key] = nil
			} else {
				ops.Set[key] = v
			}
		}
	}
	setText(in.CustomIdle, "CustomGamePlayedWhileIdle", "custom_idle", "挂时长时显示的文字")
	setText(in.CustomFarming, "CustomGamePlayedWhileFarming", "custom_farming", "挂卡时显示的文字")
	if len(conflicts) > 0 {
		e := newErr(409, "settings_conflict", fmt.Sprintf("这些设置刚被别人改过：%s。已为你显示最新内容，请确认后再保存。", strings.Join(conflicts, "、")))
		e.Extra = M{"settings": settingsView(u, in.ConnectionID, in.Bot, raw, false)}
		panic(e)
	}
	if len(in.AddGames) > 0 {
		ops.Add = parseGamesUpTo(in.AddGames, maxGamesPlayedConcurrently)
	}
	for _, g := range in.RemoveGames {
		if g <= 0 || g > maxAppID {
			panic(invalid("remove_games"))
		}
		ops.Remove = append(ops.Remove, g)
	}

	changes := applyOps(conf, ops)
	final := mergedConf(conf, changes)
	if e := checkFinalCapacity(final); e != nil {
		panic(e)
	}

	var stop []M
	if len(ops.Remove) > 0 {
		args := []any{brow.Int("id")}
		for _, g := range ops.Remove {
			args = append(args, g)
		}
		for _, r := range all(db, taskSelect+" WHERE t.bot_id = ? AND t.status IN ('running','queued') AND t.app_id IN ("+placeholders(len(ops.Remove))+") ORDER BY t.id", args...) {
			stop = append(stop, M{"id": r.Int("id"), "app_id": r.Int("app_id"), "app_name": r["app_name"]})
			ops.StopTaskIDs = append(ops.StopTaskIDs, r.Int("id"))
		}
	}
	confirmed := map[int64]bool{}
	for _, id := range in.ConfirmStop {
		confirmed[id] = true
	}
	for _, id := range ops.StopTaskIDs {
		if !confirmed[id] {
			e := newErr(409, "confirm_required", "删除这些游戏会停止下面的时长任务，请确认。")
			e.Extra = M{"tasks": stop}
			panic(e)
		}
	}
	sort.Slice(ops.StopTaskIDs, func(i, j int) bool { return ops.StopTaskIDs[i] < ops.StopTaskIDs[j] })

	if len(changes) == 0 && len(ops.StopTaskIDs) == 0 {
		return M{"saved": true, "changed": false, "message": "没有需要保存的修改。", "settings": settingsView(u, in.ConnectionID, in.Bot, raw, false)}
	}
	if len(changes) > 0 {
		blob, _ := json.Marshal(ops)
		exec(db, "UPDATE bot_play_state SET pending_ops = ?, updated_at = ? WHERE bot_id = ?", string(blob), now(), brow.Int("id"))
		if e := try(func() { client.SetConfig(in.Bot, conf, changes) }); e != nil {
			if isUncertain(e) {
				record(db, "bot_settings", "playtime", auditOpt{UserID: u.ID, IP: u.IP, Outcome: "pending",
					Target: fmt.Sprintf("connection:%d/%s", in.ConnectionID, in.Bot)})
				return M{"saved": false, "pending": true,
					"message": "ASF 没有及时响应，无法确定是否已经保存。系统会自动核对并补完，请稍后刷新查看，不用重复保存。"}
			}
			exec(db, "UPDATE bot_play_state SET pending_ops = NULL WHERE bot_id = ?", brow.Int("id"))
			panic(e)
		}
	}
	res := M{"saved": true, "changed": true}
	if e := try(func() { finalizeOps(brow.Int("id"), ops) }); e != nil {
		res["message"] = "已保存到 ASF；部分本地状态会在下一轮自动同步。"
	}
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	record(db, "bot_settings", "playtime", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d/%s", in.ConnectionID, in.Bot),
		Detail: M{"fields": keys, "removed": ops.Remove, "stopped": ops.StopTaskIDs}})
	invalidateFarming(in.ConnectionID)

	if e := try(func() {
		raw2, ok := client.GetBots([]string{in.Bot})[in.Bot]
		if !ok {
			panic(notFound("Bot"))
		}
		res["settings"] = settingsView(u, in.ConnectionID, in.Bot, raw2, false)
	}); e != nil {
		res["refresh_error"] = "已保存，但暂时读取不到最新状态，请稍后刷新。"
	}
	return res
}
