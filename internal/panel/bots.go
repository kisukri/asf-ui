package panel

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	inputSteamGuard = 3
	inputTwoFactor  = 5
	inputQrLogin    = 8
)

var (
	newBotNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	steamLoginRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	steamCodeRe  = regexp.MustCompile(`^[A-Za-z0-9]{5}$`)
)

func (c *AsfClient) CreateBot(name, login, password string) {
	checkBot(name)
	conf := M{"Enabled": true}
	if login != "" {
		conf["SteamLogin"] = login
	}
	if password != "" {
		conf["SteamPassword"] = password
	}
	c.request("POST", "/Api/Bot/"+url.PathEscape(name), M{"BotConfig": conf})
}

func (c *AsfClient) Input(bot string, inputType int, value string) {
	checkBot(bot)
	c.request("POST", "/Api/Bot/"+url.PathEscape(bot)+"/Input", M{"Type": inputType, "Value": value})
}

func nowPlaying(u *CurrentUser) []M {
	out := []M{}
	for _, c := range accessibleConnections(u.ID) {
		if !c.Bool("enabled") {
			continue
		}
		names := visibleBotNames(u.ID, c.Int("id"))
		if len(names) == 0 {
			continue
		}
		var live map[string]M
		liveErr := try(func() { live = liveStatus(c, false) })
		admin := adminOwns(u, c)
		assigned := map[string]string{}
		if admin {
			assigned = assignedNames(c.Int("id"))
		}
		for _, name := range names {
			brow := one(db, "SELECT id FROM bots WHERE connection_id = ? AND name = ?", c.Int("id"), name)
			tasks := []M{}
			for _, r := range all(db, taskSelect+" WHERE t.bot_id = ? AND (t.user_id = ? OR ?) AND t.status IN ('running','queued') ORDER BY t.created_at",
				brow.Int("id"), u.ID, admin) {
				tasks = append(tasks, taskDTO(r, u.ID))
			}
			item := M{"connection": M{"id": c.Int("id"), "name": displayName(c, u.ID)}, "bot": name, "playtime": tasks,
				"farming_games": []M{}, "config_games": []M{}, "state": "unknown", "state_text": "无法读取状态", "online": false,
				"assigned_to": nullable(assigned[name], true)}
			if liveErr != nil {
				item["error"] = liveErr.Message
			} else if s, ok := live[name]; ok {
				key, text := farmState(s)
				item["state"], item["state_text"], item["online"] = key, text, s["online"]
				item["needs_input"], item["required_input"] = s["needs_input"], s["required_input"]
				f := s["farming"].(M)
				switch key {
				case "farming":
					item["farming_games"] = f["current_games"]
				case "idling":
					taskApps := map[int64]bool{}
					for _, tk := range tasks {
						taskApps[tk["app_id"].(int64)] = true
					}
					var own []int64
					for _, g := range s["idle_games"].([]int64) {
						if !taskApps[g] {
							own = append(own, g)
						}
					}
					names := gameNames(own)
					cg := []M{}
					for _, g := range own {
						cg = append(cg, M{"app_id": g, "name": names[g]})
					}
					item["config_games"] = cg
				}
			}
			item["active"] = len(tasks) > 0 || len(item["farming_games"].([]M)) > 0 || len(item["config_games"].([]M)) > 0
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["active"].(bool) && !out[j]["active"].(bool) })
	return out
}

func botHosts(u *CurrentUser) []Row {
	if u.IsAdmin {
		return all(db, "SELECT * FROM asf_connections WHERE user_id = ? AND enabled = 1 ORDER BY id", u.ID)
	}
	return all(db, `SELECT c.* FROM asf_connections c JOIN users o ON o.id = c.user_id
		WHERE c.allow_user_bots = 1 AND c.enabled = 1 AND o.is_admin = 1 AND o.is_active = 1 ORDER BY c.id`)
}

func botsCreatedBy(uid int64) int64 {
	return scalar(db, "SELECT COUNT(*) FROM bots WHERE created_by = ? AND present = 1", uid)
}

func myBotsInfo(u *CurrentUser) M {
	quota := int64(-1)
	if !u.IsAdmin {
		quota = one(db, "SELECT bot_quota FROM users WHERE id = ?", u.ID).Int("bot_quota")
	}
	hosts := []M{}
	for _, c := range botHosts(u) {
		var headless any
		_ = try(func() { headless = clientFor(c).Headless() })
		hosts = append(hosts, M{"connection_id": c.Int("id"), "name": displayName(c, u.ID), "qr_supported": headless != true})
	}
	return M{"quota": quota, "used": botsCreatedBy(u.ID), "hosts": hosts}
}

func createBot(u *CurrentUser, cid int64, name, method, login, password string, assignTo *int64) M {
	var host Row
	for _, c := range botHosts(u) {
		if c.Int("id") == cid {
			host = c
		}
	}
	if host == nil {
		panic(notFound("ASF 连接"))
	}
	name, login = strings.TrimSpace(name), strings.TrimSpace(login)
	if !newBotNameRe.MatchString(name) || strings.EqualFold(name, "ASF") {
		fail(400, "invalid_bot", "Bot 名称只能用字母、数字、下划线和短横线（1–32 位），且不能叫 ASF。")
	}
	switch method {
	case "password":
		if !steamLoginRe.MatchString(login) {
			fail(400, "invalid_login", "Steam 账号名只能包含字母、数字和下划线。", "这里填登录用的账号名，不是昵称或邮箱。")
		}
		if password == "" || runeLen(password) > 256 || ctrlRe.MatchString(password) {
			fail(400, "invalid_password", "请填写 Steam 密码。")
		}
	case "qr":
		if login != "" && !steamLoginRe.MatchString(login) {
			fail(400, "invalid_login", "Steam 账号名只能包含字母、数字和下划线。")
		}
		password = ""
	default:
		fail(400, "invalid_request", "请选择登录方式。")
	}
	if u.IsAdmin {
		if assignTo != nil {
			t := one(db, "SELECT is_admin FROM users WHERE id = ?", *assignTo)
			if t == nil {
				panic(notFound("用户"))
			}
			if t.Bool("is_admin") {
				fail(400, "invalid_request", "只能分配给普通用户。")
			}
		}
	} else {
		quota := one(db, "SELECT bot_quota FROM users WHERE id = ?", u.ID).Int("bot_quota")
		if botsCreatedBy(u.ID) >= quota {
			fail(403, "quota_exceeded", fmt.Sprintf("你最多可以新建 %d 个 Bot，已经用完。", quota), "可以先删除不用的 Bot，或请管理员提高额度。")
		}
		assignTo = &u.ID
	}
	if !rateHit(db, "bot_create", strconv.FormatInt(u.ID, 10), 10, 3600) {
		fail(429, "rate_limited", "新建 Bot 过于频繁，请稍后再试。")
	}
	client := clientFor(host)
	if method == "qr" && client.Headless() == true {
		fail(409, "qr_unavailable", "这个 ASF 开启了 Headless（无头模式），不支持扫码登录。",
			"请改用密码登录，或请管理员在 ASF.json 中把 Headless 改为 false 后重启 ASF。")
	}
	for existing := range client.ListBots() {
		if strings.EqualFold(existing, name) {
			fail(409, "bot_exists", "ASF 中已经有同名的 Bot，请换一个名称。")
		}
	}
	client.CreateBot(name, login, password)

	var summary M
	for i := 0; i < 10 && summary == nil; i++ {
		time.Sleep(1500 * time.Millisecond)
		if b, ok := client.GetBots([]string{name})[name]; ok {
			summary = summarizeBot(name, b)
		}
	}
	t := now()
	exec(db, `INSERT INTO bots (connection_id, name, present, last_seen_at, created_by) VALUES (?, ?, 1, ?, ?)
		ON CONFLICT(connection_id, name) DO UPDATE SET present = 1, last_seen_at = excluded.last_seen_at, created_by = excluded.created_by`,
		cid, name, t, u.ID)
	botID := one(db, "SELECT id FROM bots WHERE connection_id = ? AND name = ?", cid, name).Int("id")
	warnings := []string{}
	if assignTo != nil {
		if u.IsAdmin {
			warnings = append(warnings, assignBot(u, botID, assignTo)...)
		} else {
			exec(db, "INSERT OR REPLACE INTO bot_assignments (bot_id, user_id, assigned_by, assigned_at) VALUES (?, ?, ?, ?)", botID, u.ID, u.ID, t)
		}
	}
	if method == "qr" && summary != nil {
		startQR(client, name)
	}
	record(db, "bot_create", "farming", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d/%s", cid, name),
		Detail: M{"bot": name, "method": method, "loaded": summary != nil}})
	invalidateFarming(cid)
	res := M{"bot": name, "bot_id": botID, "connection_id": cid, "method": method, "loaded": summary != nil, "warnings": warnings}
	if summary != nil {
		res["online"], res["needs_input"], res["required_input"] = summary["online"], summary["needs_input"], summary["required_input"]
	}
	return res
}

func loginTarget(u *CurrentUser, cid int64, bot string) Row {
	var crow Row
	if e := try(func() { crow, _ = ownedBot(u.ID, cid, bot) }); e != nil {
		if !u.IsAdmin {
			panic(e)
		}
		crow = getOwnedConnection(u.ID, cid)
		if !crow.Bool("enabled") || !isSafeBotName(bot) ||
			one(db, "SELECT 1 FROM bots WHERE connection_id = ? AND name = ? AND present = 1", cid, bot) == nil {
			panic(e)
		}
	}
	return crow
}

func submitCode(u *CurrentUser, cid int64, bot, kind, code string) string {
	crow := loginTarget(u, cid, bot)
	typ := map[string]int{"steam_guard": inputSteamGuard, "two_factor": inputTwoFactor}[kind]
	if typ == 0 {
		fail(400, "invalid_request", "请选择验证码类型。")
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if !steamCodeRe.MatchString(code) {
		fail(400, "invalid_code", "验证码应为 5 位字母或数字。")
	}
	if !rateHit(db, "bot_input", strconv.FormatInt(u.ID, 10), 10, 600) {
		fail(429, "rate_limited", "提交过于频繁，请稍后再试。")
	}
	client := clientFor(crow)
	client.Input(bot, typ, code)
	_ = try(func() { client.StartBot(bot) })
	record(db, "bot_input", "farming", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d/%s", cid, bot),
		Detail: M{"kind": kind}})
	invalidateFarming(cid)
	return "已提交验证码，ASF 正在重新登录，几秒后刷新即可看到结果。"
}

func startQR(client *AsfClient, bot string) {
	for i := 0; i < 6; i++ {
		var stopped bool
		_ = try(func() {
			if b, ok := client.GetBots([]string{bot})[bot]; ok {
				s := summarizeBot(bot, b)
				stopped = !s["keep_running"].(bool) || s["needs_input"].(bool)
			}
		})
		if stopped {
			break
		}
		time.Sleep(1500 * time.Millisecond)
	}
	client.Input(bot, inputQrLogin, "Y")
	_ = try(func() { client.StartBot(bot) })
}

func qrStart(u *CurrentUser, cid int64, bot string) string {
	crow := loginTarget(u, cid, bot)
	if !rateHit(db, "bot_input", strconv.FormatInt(u.ID, 10), 10, 600) {
		fail(429, "rate_limited", "操作过于频繁，请稍后再试。")
	}
	client := clientFor(crow)
	if client.Headless() == true {
		fail(409, "qr_unavailable", "这个 ASF 开启了 Headless（无头模式），不支持扫码登录。",
			"请改用验证码，或请管理员在 ASF.json 中把 Headless 改为 false 后重启 ASF。")
	}
	_ = try(func() { client.StopBot(bot) })
	startQR(client, bot)
	record(db, "bot_qr_start", "farming", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d/%s", cid, bot)})
	invalidateFarming(cid)
	return "正在生成二维码…"
}

func qrStatus(u *CurrentUser, cid int64, bot string) M {
	crow := loginTarget(u, cid, bot)
	raw, ok := clientFor(crow).GetBots([]string{bot})[bot]
	if !ok {
		panic(notFound("Bot"))
	}
	s := summarizeBot(bot, raw)
	res := M{"online": s["online"], "needs_input": s["needs_input"], "required_input": s["required_input"], "qr": nil}
	if link, _ := raw["QrChallengeURL"].(string); link != "" {
		if png, err := qrcode.Encode(link, qrcode.Medium, 280); err == nil {
			res["qr"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}
	return res
}

func canDeleteBot(u *CurrentUser, crow, meta Row) bool {
	return crow.Int("user_id") == u.ID || (meta != nil && !meta.Null("created_by") && meta.Int("created_by") == u.ID)
}

func deleteBot(u *CurrentUser, cid int64, bot string) {
	var crow, brow Row
	if e := try(func() { crow, brow = ownedBot(u.ID, cid, bot) }); e != nil {
		crow = loginTarget(u, cid, bot)
		brow = one(db, "SELECT * FROM bots WHERE connection_id = ? AND name = ?", cid, bot)
	}
	if !canDeleteBot(u, crow, brow) {
		fail(403, "forbidden", "只有创建这个 Bot 的人或管理员可以删除它。")
	}
	stopTasksWhere("bot_id = ?", []any{brow.Int("id")}, "bot_deleted")
	clientFor(crow).DeleteBot(bot)
	exec(db, "DELETE FROM bots WHERE id = ?", brow.Int("id"))
	record(db, "bot_delete", "farming", auditOpt{UserID: u.ID, IP: u.IP, Target: fmt.Sprintf("connection:%d/%s", cid, bot)})
	invalidateFarming(cid)
}
