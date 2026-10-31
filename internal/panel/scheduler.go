package panel

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const unreachableFailAfter = 24 * 3600

type Scheduler struct {
	holder string
	stop   chan struct{}
	done   chan struct{}
}

var sched = &Scheduler{}

func newHolder() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s:%d:%x", host, os.Getpid(), randBytes(4))
}

func (s *Scheduler) acquireLease() bool {
	t := now()
	res := exec(db, "UPDATE scheduler_state SET holder = ?, lease_until = ? WHERE id = 1 AND (holder = ? OR lease_until < ?)",
		s.holder, t+int64(cfg.SchedulerInterval)*3, s.holder, t)
	return affected(res) == 1
}

func botState(botID int64) Row {
	exec(db, "INSERT OR IGNORE INTO bot_play_state (bot_id, updated_at, mode) VALUES (?, ?, 'config')", botID, now())
	return one(db, "SELECT * FROM bot_play_state WHERE bot_id = ?", botID)
}

func saveState(botID int64, fields map[string]any) {
	fields["updated_at"] = now()
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sets := make([]string, len(keys))
	args := make([]any, 0, len(keys)+1)
	for i, k := range keys {
		sets[i] = k + " = ?"
		args = append(args, fields[k])
	}
	exec(db, "UPDATE bot_play_state SET "+strings.Join(sets, ", ")+" WHERE bot_id = ?", append(args, botID)...)
}

func parseApplied(s string) []int64 {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// gone from asf -> forget who it belonged to, a same-named bot later is a new bot
func forgetBotTx(tx Q, botID int64) {
	t := now()
	exec(tx, `UPDATE playtime_tasks SET status = 'failed', stop_reason = 'bot_missing', last_error = ?, finished_at = ?, updated_at = ?
		WHERE bot_id = ? AND status IN ('running','queued')`, "ASF 中已找不到这个 Bot", t, t, botID)
	if a := one(tx, "SELECT user_id FROM bot_assignments WHERE bot_id = ?", botID); a != nil {
		record(tx, "bot_unassign", "admin", auditOpt{Target: fmt.Sprintf("bot:%d", botID),
			Detail: M{"reason": "bot_missing", "from_user": a.Int("user_id")}})
	}
	exec(tx, "DELETE FROM bot_assignments WHERE bot_id = ?", botID)
	exec(tx, "UPDATE bots SET present = 0, created_by = NULL WHERE id = ?", botID)
	exec(tx, `UPDATE bot_play_state SET managed = 0, confirmed = 0, applied_appids = '', last_error = NULL, wait_reason = NULL,
		mode = 'config', pending_ops = NULL, updated_at = ? WHERE bot_id = ?`, t, botID)
}

func finishTasks(botID int64, status, reason string, errMsg any) {
	t := now()
	exec(db, `UPDATE playtime_tasks SET status = ?, stop_reason = ?, last_error = ?, finished_at = ?, updated_at = ?
		WHERE bot_id = ? AND status IN ('running','queued')`, status, reason, errMsg, t, t, botID)
}

func balanceTasks(botID int64, room int, free map[int64]bool) {
	t := now()
	used := 0
	for _, r := range all(db, "SELECT id, app_id FROM playtime_tasks WHERE bot_id = ? AND status = 'running' ORDER BY started_at, id", botID) {
		if free[r.Int("app_id")] {
			continue
		}
		used++
		if used > room {
			exec(db, "UPDATE playtime_tasks SET status = 'queued', updated_at = ? WHERE id = ? AND status = 'running'", t, r.Int("id"))
		}
	}
	slots := room - used
	for _, r := range all(db, "SELECT id, app_id FROM playtime_tasks WHERE bot_id = ? AND status = 'queued' ORDER BY created_at, id", botID) {
		if !free[r.Int("app_id")] {
			if slots <= 0 {
				continue
			}
			slots--
		}
		exec(db, "UPDATE playtime_tasks SET status = 'running', started_at = COALESCE(started_at, ?), updated_at = ? WHERE id = ? AND status = 'queued'",
			t, t, r.Int("id"))
	}
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func onUnreachable(bot Row, e *AppError) {
	exec(db, "UPDATE bot_play_state SET wait_reason = ? WHERE bot_id = ?", "无法连接 ASF，计时暂停", bot.Int("id"))
	t := now()
	st := botState(bot.Int("id"))
	since := st.Int("failing_since")
	if since == 0 {
		since = t
	}
	msg := truncate(fmt.Sprintf("无法连接 ASF：%s", e.Message), 300)
	saveState(bot.Int("id"), map[string]any{"confirmed": 0, "last_online": 0, "failing_since": since, "last_error": msg})

	exec(db, "UPDATE playtime_tasks SET last_accrued_at = ?, updated_at = ? WHERE bot_id = ? AND status = 'running'", t, t, bot.Int("id"))
	if t-since > unreachableFailAfter {
		finishTasks(bot.Int("id"), "failed", "unreachable_too_long", msg)
	}
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func processBot(client *AsfClient, bot Row, raw map[string]any) {
	t := now()
	id := bot.Int("id")
	st := botState(id)
	if raw == nil {
		db.tx(func(tx Q) { forgetBotTx(tx, id) })
		return
	}
	s := summarizeBot(bot.Str("name"), raw)
	online, possible := s["online"].(bool), s["playing_possible"].(bool)
	f := s["farming"].(M)
	paused, nowFarming := f["paused"].(bool), f["now_farming"].(bool)
	base := func(m map[string]any) map[string]any {
		m["last_online"], m["last_playing_possible"] = b2i(online), b2i(possible)
		return m
	}

	if st.Str("mode") != "config" {
		if st.Bool("managed") {
			if !online {
				return
			}
			_ = try(func() {
				if !st.Bool("prev_farming_paused") {
					if paused {
						client.ResumeFarming(bot.Str("name"))
					}
				} else {
					client.Reset(bot.Str("name"))
				}
			})
		}
		saveState(id, map[string]any{"mode": "config", "managed": 0, "confirmed": 0, "applied_appids": "", "last_error": nil})
		st = botState(id)
	}

	conf, _ := raw["BotConfig"].(map[string]any)
	if conf == nil {
		saveState(id, base(map[string]any{"last_error": "读不到这个 Bot 的 ASF 配置"}))
		return
	}
	cfgGames := confGames(conf)
	inCfg := map[int64]bool{}
	for _, g := range cfgGames {
		inCfg[g] = true
	}
	added := map[int64]bool{}
	for _, g := range parseApplied(st.Str("applied_appids")) {
		added[g] = true
	}
	capacity := idleCapacity(conf)
	userOwned := map[int64]bool{}
	for _, g := range cfgGames {
		if !added[g] {
			userOwned[g] = true
		}
	}

	canAccrue := online && possible && !nowFarming
	capSecs := int64(cfg.SchedulerInterval) * 3
	for _, task := range all(db, "SELECT id, app_id, target_seconds, elapsed_seconds, last_accrued_at FROM playtime_tasks WHERE bot_id = ? AND status = 'running'", id) {
		var delta int64
		if canAccrue && !task.Null("last_accrued_at") && inCfg[task.Int("app_id")] {
			delta = max(0, min(t-task.Int("last_accrued_at"), capSecs))
		}
		target := task.Float("target_seconds")
		elapsed := task.Float("elapsed_seconds") + float64(delta)
		if target > 0 && elapsed >= target {
			exec(db, `UPDATE playtime_tasks SET elapsed_seconds = ?, status = 'completed', stop_reason = 'target_reached',
				finished_at = ?, updated_at = ?, last_accrued_at = ?, last_error = NULL WHERE id = ? AND status = 'running'`,
				target, t, t, t, task.Int("id"))
		} else {
			exec(db, "UPDATE playtime_tasks SET elapsed_seconds = ?, last_accrued_at = ?, updated_at = ? WHERE id = ?", elapsed, t, t, task.Int("id"))
		}
	}

	balanceTasks(id, capacity-len(userOwned), userOwned)
	var desired []int64
	want := map[int64]bool{}
	for _, r := range all(db, "SELECT DISTINCT app_id FROM playtime_tasks WHERE bot_id = ? AND status = 'running' ORDER BY app_id", id) {
		desired = append(desired, r.Int("app_id"))
		want[r.Int("app_id")] = true
	}

	var newList, newAdded, appended []int64
	changed, overflow := false, false
	for _, g := range cfgGames {
		if added[g] && !want[g] {
			changed = true
			continue
		}
		newList = append(newList, g)
		if added[g] {
			newAdded = append(newAdded, g)
		}
	}
	for _, g := range desired {
		if inCfg[g] {
			continue
		}
		if len(newList) >= capacity {
			overflow = true
			continue
		}
		newList = append(newList, g)
		newAdded = append(newAdded, g)
		appended = append(appended, g)
		changed = true
	}

	var reason any
	switch {
	case !online && s["needs_input"].(bool):
		reason = "Bot 需要登录验证，计时暂停"
	case !online:
		reason = "Bot 离线，计时暂停"
	case !possible:
		reason = "Steam 账号正在其他地方游戏，计时暂停"
	case nowFarming:
		reason = "正在挂卡，挂卡结束后自动开始（也可以在“卡牌挂机”里停止挂卡）"
	case overflow:
		reason = fmt.Sprintf("这个 Bot 最多挂 %d 个游戏，多出的任务在排队", capacity)
	}
	allIn := true
	for _, g := range desired {
		if !inCfg[g] {
			allIn = false
		}
	}
	state := base(map[string]any{"managed": b2i(len(newAdded) > 0), "applied_appids": joinInts(newAdded, ","),
		"confirmed": b2i(canAccrue && allIn && !changed), "wait_reason": reason})

	if !changed || !online {
		if changed {
			state["wait_reason"] = "Bot 离线，上线后再写入 ASF 配置"
			state["applied_appids"], state["managed"] = st.Str("applied_appids"), st.Int("managed")
		}
		state["last_error"] = nil
		saveState(id, state)
		return
	}

	if len(appended) > 0 {
		// mark them as ours before writing, a timeout shouldn't orphan them
		claim := map[int64]bool{}
		for _, g := range parseApplied(st.Str("applied_appids")) {
			claim[g] = true
		}
		for _, g := range appended {
			claim[g] = true
		}
		var owned []int64
		for g := range claim {
			owned = append(owned, g)
		}
		sort.Slice(owned, func(i, j int) bool { return owned[i] < owned[j] })
		saveState(id, map[string]any{"applied_appids": joinInts(owned, ","), "managed": 1})
	}
	if e := try(func() { client.SetIdleGames(bot.Str("name"), conf, newList) }); e != nil {
		saveState(id, base(map[string]any{"confirmed": 0, "last_error": truncate(fmt.Sprintf("写入 ASF 配置失败：%s", e.Message), 300)}))
		return
	}
	state["applied_at"], state["last_error"] = t, nil
	if reason == nil {
		state["wait_reason"] = "已写入 ASF 配置，Bot 重连后开始计时"
	}
	saveState(id, state)
	invalidateFarming(bot.Int("connection_id"))
}

func processConnection(crow Row, bots []Row, includeDisabled bool) {
	var client *AsfClient
	var connErr *AppError
	if e := try(func() { client = clientFor(crow) }); e != nil {
		connErr = e
	}
	for _, b := range bots {
		if connErr != nil {
			onUnreachable(b, connErr)
			continue
		}
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			var unlock func()
			if try(func() { unlock = lockBot(ctx, b.Int("id")) }) != nil {
				return
			}
			defer unlock()
			if c := one(db, "SELECT enabled FROM asf_connections WHERE id = ?", crow.Int("id")); c == nil || (!c.Bool("enabled") && !includeDisabled) {
				return
			}
			if e := resolvePending(client, crow, b); e != nil && isUncertain(e) {
				connErr = e
				onUnreachable(b, e)
				return
			}
			var raw map[string]any
			if e := try(func() { raw = client.GetBots([]string{b.Str("name")})[b.Str("name")] }); e != nil {
				connErr = e
				onUnreachable(b, e)
				return
			}
			if e := try(func() { processBot(client, b, raw) }); e != nil {
				slog.Warn("bot sync failed", "bot_id", b.Int("id"), "code", e.Code)
			}
		}()
	}
	if connErr != nil {
		code := connErr.Code
		if _, ok := statusText[code]; !ok {
			code = "asf_error"
		}
		exec(db, "UPDATE asf_connections SET status = ?, status_message = ? WHERE id = ? AND enabled = 1",
			code, truncate(errHint(connErr), 300), crow.Int("id"))
		return
	}
	exec(db, "UPDATE asf_connections SET status = 'ok', last_ok_at = ? WHERE id = ? AND enabled = 1 AND status != 'ok'", now(), crow.Int("id"))
}

func botsNeedingWork(botIDs []int64) map[int64][]Row {
	q := `SELECT b.* FROM bots b WHERE b.id IN (
		SELECT bot_id FROM playtime_tasks WHERE status IN ('running','queued')
		UNION SELECT bot_id FROM bot_play_state WHERE managed = 1 OR pending_ops IS NOT NULL)`
	var args []any
	if botIDs != nil {
		q += " AND b.id IN (" + placeholders(len(botIDs)) + ")"
		for _, b := range botIDs {
			args = append(args, b)
		}
	}
	grouped := map[int64][]Row{}
	for _, r := range all(db, q, args...) {
		grouped[r.Int("connection_id")] = append(grouped[r.Int("connection_id")], r)
	}
	return grouped
}

const maxParallelConnections = 4

func (s *Scheduler) run(botIDs []int64, includeDisabled bool) {
	if botIDs != nil && len(botIDs) == 0 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallelConnections)
	for cid, bots := range botsNeedingWork(botIDs) {
		crow := one(db, "SELECT * FROM asf_connections WHERE id = ?", cid)
		if crow == nil {
			continue
		}
		if !crow.Bool("enabled") && !includeDisabled {

			t := now()
			exec(db, `UPDATE playtime_tasks SET status = 'stopped', stop_reason = 'connection_disabled', finished_at = ?,
				updated_at = ? WHERE connection_id = ? AND status IN ('running','queued')`, t, t, cid)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(crow Row, bots []Row) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("connection sync failed", "connection_id", crow.Int("id"), "error", fmt.Sprint(r))
				}
			}()
			processConnection(crow, bots, includeDisabled)
		}(crow, bots)
	}
	wg.Wait()
}

func (s *Scheduler) tick() {
	started := time.Now()
	if !s.acquireLease() {
		return
	}
	var errText any
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("tick failed", "error", fmt.Sprint(r))
				errText = truncate(fmt.Sprint(r), 200)
			}
		}()
		s.run(nil, false)
	}()
	exec(db, "DELETE FROM rate_limits WHERE window_start < ?", now()-86400)
	exec(db, "DELETE FROM sessions WHERE expires_at < ?", now())
	exec(db, "UPDATE scheduler_state SET last_tick_at = ?, last_tick_ms = ?, last_error = ?, ticks = ticks + 1 WHERE id = 1",
		now(), time.Since(started).Milliseconds(), errText)
}

func (s *Scheduler) syncBot(botID int64) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("sync now failed", "bot_id", botID, "error", fmt.Sprint(r))
		}
	}()
	if !s.acquireLease() {
		return
	}
	s.run([]int64{botID}, false)
}

func (s *Scheduler) releaseConnection(cid int64) []string {
	var ids []int64
	for _, r := range all(db, `SELECT s.bot_id FROM bot_play_state s JOIN bots b ON b.id = s.bot_id
		WHERE b.connection_id = ? AND s.managed = 1`, cid) {
		ids = append(ids, r.Int("bot_id"))
	}
	if len(ids) == 0 {
		return nil
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("release connection failed", "connection_id", cid, "error", fmt.Sprint(r))
			}
		}()
		s.run(ids, true)
	}()
	args := make([]any, len(ids))
	for i, v := range ids {
		args[i] = v
	}
	var failed []string
	for _, r := range all(db, `SELECT b.name FROM bot_play_state s JOIN bots b ON b.id = s.bot_id
		WHERE s.managed = 1 AND s.bot_id IN (`+placeholders(len(ids))+`)`, args...) {
		failed = append(failed, r.Str("name"))
	}
	return failed
}

func (s *Scheduler) Start() {
	s.holder = newHolder()
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Duration(cfg.SchedulerInterval) * time.Second)
		defer ticker.Stop()
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("scheduler panic", "error", fmt.Sprint(r))
					}
				}()
				s.tick()
			}()
			select {
			case <-s.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	slog.Info("scheduler started", "holder", s.holder)
}

func (s *Scheduler) Stop() {
	if s.stop == nil {
		return
	}
	close(s.stop)
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
	}
	exec(db, "UPDATE scheduler_state SET lease_until = 0 WHERE id = 1 AND holder = ?", s.holder)
}
