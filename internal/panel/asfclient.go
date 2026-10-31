package panel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	maxGamesPlayedConcurrently = 32
	maxPlayGames               = maxGamesPlayedConcurrently - 1
	maxAppID                   = 4294967295
)

var botNameRe = regexp.MustCompile(`^[^\s,<>\x00-\x1f\x7f]{1,64}$`)

func isSafeBotName(name string) bool {
	if !botNameRe.MatchString(name) {
		return false
	}
	return !strings.EqualFold(name, "ASF") && !strings.Contains(name, "..") && !strings.HasPrefix(strings.ToLower(name), "r!")
}

var statusText = map[string]string{
	"ok": "连接正常", "untested": "尚未测试", "auth_failed": "IPC 密码错误或缺失", "forbidden": "被 ASF 拒绝访问",
	"unreachable": "无法连接", "timeout": "连接超时", "tls": "HTTPS 证书校验失败", "bad_response": "对方不像是 ASF IPC",
	"blocked_address": "地址不被允许", "https_required": "需要使用 HTTPS", "dns_failed": "域名无法解析",
	"asf_error": "ASF 返回错误", "disabled": "已断开",
}

func statusLabel(s string) string {
	if t, ok := statusText[s]; ok {
		return t
	}
	return "未知状态"
}

func asfErr(code, msg string, hint ...string) *AppError { return newErr(502, code, msg, hint...) }

type AsfClient struct {
	Target   Target
	Password string
	Ctx      context.Context
}

func (c *AsfClient) request(method, path string, body any) any {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	parent := c.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout*2)
	defer cancel()
	ip := resolveTarget(ctx, c.Target)
	var payload io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		payload = bytes.NewReader(b)
	}
	u := c.Target.Scheme + "://" + net.JoinHostPort(c.Target.Host, strconv.Itoa(c.Target.Port)) + c.Target.Path + path
	req, err := http.NewRequestWithContext(ctx, method, u, payload)
	if err != nil {
		panic(asfErr("bad_response", "ASF 地址格式无法使用。"))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "asf-panel/2.0")
	if c.Password != "" {
		req.Header.Set("Authentication", c.Password)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := pinnedClient(c.Target, ip, timeout).Do(req)
	if err != nil {
		panic(mapNetErr(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, cfg.MaxResponseBytes+1))
	if err != nil {
		panic(mapNetErr(err))
	}
	if int64(len(raw)) > cfg.MaxResponseBytes {
		panic(asfErr("bad_response", "ASF 返回的数据过大，已中止。"))
	}
	switch resp.StatusCode {
	case 401:
		panic(asfErr("auth_failed", "IPC 密码错误或缺失。", "请在连接设置中填写与 ASF 的 IPCPassword 一致的密码。"))
	case 403:
		panic(asfErr("forbidden", "ASF 拒绝了访问。",
			"可能原因：ASF 未设置 IPCPassword 时只允许本机访问；或多次密码错误后被 ASF 临时封禁，请稍后再试。"))
	case 301, 302, 307, 308:
		panic(asfErr("bad_response", "ASF 地址发生了跳转，本网站不跟随跳转。", "请填写最终的 ASF 地址（包括 https 和端口）。"))
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		panic(asfErr("bad_response", "对方返回的不是 ASF IPC 数据。", "请确认地址指向 ASF 的 IPC 端口。"))
	}
	success, hasSuccess := data["Success"]
	if !hasSuccess {
		panic(asfErr("bad_response", "对方返回的不是 ASF IPC 数据。", "请确认地址指向 ASF 的 IPC 端口。"))
	}
	if ok, _ := success.(bool); resp.StatusCode >= 400 || !ok {
		msg, _ := data["Message"].(string)
		msg = truncate(strings.TrimSpace(stripCtrl(msg, " ")), 200)
		if msg != "" {
			panic(asfErr("asf_error", fmt.Sprintf("ASF 返回错误：%s", msg)))
		}
		panic(asfErr("asf_error", "ASF 返回了错误。"))
	}
	return data["Result"]
}

func mapNetErr(err error) *AppError {
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var recErr tls.RecordHeaderError
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return newErr(503, "canceled", "请求已取消。")
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr), errors.As(err, &invalid):
		return asfErr("tls", "ASF 的 HTTPS 证书无法通过校验。", "请使用受信任 CA 签发的证书（例如 Let's Encrypt）。")
	case errors.As(err, &recErr):
		return asfErr("tls", "与 ASF 建立 HTTPS 连接失败。", "请确认该端口确实提供 HTTPS。")
	case errors.Is(err, syscall.ECONNREFUSED):
		return asfErr("unreachable", "ASF 拒绝了连接。", "请确认 ASF 正在运行、IPC 已开启且端口正确。")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return asfErr("timeout", "连接 ASF 超时。", "请确认 ASF 正在运行、地址和端口可从公网访问。")
	}
	if strings.Contains(strings.ToLower(err.Error()), "refused") {
		return asfErr("unreachable", "ASF 拒绝了连接。", "请确认 ASF 正在运行、IPC 已开启且端口正确。")
	}
	return asfErr("unreachable", "无法连接到 ASF。", "请检查地址、端口和防火墙设置。")
}

func (c *AsfClient) Headless() any {
	res, _ := c.request("GET", "/Api/ASF", nil).(map[string]any)
	g, _ := res["GlobalConfig"].(map[string]any)
	if h, ok := g["Headless"].(bool); ok {
		return h
	}
	return nil
}

func (c *AsfClient) DeleteBot(bot string) {
	checkBot(bot)
	c.request("DELETE", "/Api/Bot/"+url.PathEscape(bot), nil)
}

func (c *AsfClient) Version() string {
	res, _ := c.request("GET", "/Api/ASF", nil).(map[string]any)
	if v, ok := res["Version"]; ok && v != nil {
		return truncate(fmt.Sprint(v), 40)
	}
	return ""
}

func botsMap(res any) map[string]map[string]any {
	m, ok := res.(map[string]any)
	if !ok {
		panic(asfErr("bad_response", "ASF 返回的 Bot 数据格式无法识别。"))
	}
	out := map[string]map[string]any{}
	for k, v := range m {
		if b, ok := v.(map[string]any); ok {
			out[k] = b
		}
	}
	return out
}

func (c *AsfClient) ListBots() map[string]map[string]any {
	return botsMap(c.request("GET", "/Api/Bot/ASF", nil))
}

func (c *AsfClient) GetBots(names []string) map[string]map[string]any {
	var parts []string
	for _, n := range names {
		if isSafeBotName(n) {
			parts = append(parts, url.PathEscape(n))
		}
	}
	if len(parts) == 0 {
		return map[string]map[string]any{}
	}
	return botsMap(c.request("GET", "/Api/Bot/"+strings.Join(parts, ","), nil))
}

func checkBot(bot string) {
	if !isSafeBotName(bot) {
		panic(newErr(400, "invalid_bot", "Bot 名称不受支持。"))
	}
}

func (c *AsfClient) PauseFarming(bot string, permanent bool, resumeInSeconds int) {
	checkBot(bot)
	resumeInSeconds = max(0, min(resumeInSeconds, 65535))
	c.request("POST", "/Api/Bot/"+url.PathEscape(bot)+"/Pause", M{"Permanent": permanent, "ResumeInSeconds": resumeInSeconds})
}

func (c *AsfClient) botAction(bot, action string) {
	checkBot(bot)
	c.request("POST", "/Api/Bot/"+url.PathEscape(bot)+"/"+action, nil)
}

func (c *AsfClient) ResumeFarming(bot string) { c.botAction(bot, "Resume") }
func (c *AsfClient) StartBot(bot string)      { c.botAction(bot, "Start") }
func (c *AsfClient) StopBot(bot string)       { c.botAction(bot, "Stop") }

func (c *AsfClient) command(cmd string) string {
	res, _ := c.request("POST", "/Api/Command", M{"Command": cmd}).(string)
	return res
}

func idList(ids []int64) string {
	set := map[int64]bool{}
	var clean []int64
	for _, a := range ids {
		if a > 0 && a <= maxAppID && !set[a] {
			set[a] = true
			clean = append(clean, a)
		}
	}
	if len(clean) == 0 {
		panic(newErr(400, "invalid_request", "没有有效的 App ID。"))
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i] < clean[j] })
	return joinInts(clean, ",")
}

func (c *AsfClient) Blacklist(bot string) []int64 {
	checkBot(bot)
	return parseIDList(c.command("fb " + bot))
}

func (c *AsfClient) BlacklistAdd(bot string, ids []int64) {
	checkBot(bot)
	c.command("fbadd " + bot + " " + idList(ids))
}

func (c *AsfClient) BlacklistRemove(bot string, ids []int64) {
	checkBot(bot)
	c.command("fbrm " + bot + " " + idList(ids))
}

func (c *AsfClient) SetConfig(bot string, conf map[string]any, changes map[string]any) {
	checkBot(bot)
	next := make(map[string]any, len(conf)+len(changes))
	for k, v := range conf {
		next[k] = v
	}
	for k, v := range changes {
		next[k] = v
	}
	c.request("POST", "/Api/Bot/"+url.PathEscape(bot), M{"BotConfig": next})
}

func (c *AsfClient) SetIdleGames(bot string, conf map[string]any, games []int64) {
	if len(games) > maxGamesPlayedConcurrently {
		panic(newErr(400, "invalid_request", "配置里的游戏不能超过 32 个。"))
	}
	if games == nil {
		games = []int64{}
	}
	c.SetConfig(bot, conf, map[string]any{"GamesPlayedWhileIdle": games})
}

func (c *AsfClient) Reset(bot string) {
	checkBot(bot)
	c.command("reset " + bot)
}

var (
	fbLineRe  = regexp.MustCompile(`^\s*<[^>]*>\s*(.*)$`)
	idsOnlyRe = regexp.MustCompile(`^[\d,\s]+$`)
	digitsRe  = regexp.MustCompile(`\d+`)
	spanRe    = regexp.MustCompile(`^(?:(\d+)\.)?(\d{1,2}):(\d{2}):(\d{2})(?:\.\d+)?$`)
)

func parseIDList(text string) []int64 {
	set := map[int64]bool{}
	for _, line := range strings.Split(text, "\n") {
		rest := strings.TrimSpace(line)
		if m := fbLineRe.FindStringSubmatch(line); m != nil {
			rest = strings.TrimSpace(m[1])
		}
		if rest != "" && idsOnlyRe.MatchString(rest) {
			for _, d := range digitsRe.FindAllString(rest, -1) {
				if n, err := strconv.ParseInt(d, 10, 64); err == nil && n > 0 && n <= maxAppID {
					set[n] = true
				}
			}
		}
	}
	out := make([]int64, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func timespanSeconds(v any) any {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	m := spanRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil
	}
	n := func(x string) int64 { v, _ := strconv.ParseInt(x, 10, 64); return v }
	return n(m[1])*86400 + n(m[2])*3600 + n(m[3])*60 + n(m[4])
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func gameInfo(g map[string]any) M {
	var appID any
	if f, ok := num(g["AppID"]); ok {
		appID = int64(f)
	}
	var name any
	if s, ok := g["GameName"].(string); ok && s != "" {
		name = truncate(s, 120)
	}
	var cards, hours any
	if f, ok := num(g["CardsRemaining"]); ok && f == float64(int64(f)) {
		cards = int64(f)
	}
	if f, ok := num(g["HoursPlayed"]); ok {
		hours = float64(int64(f*10+0.5)) / 10
	}
	return M{"app_id": appID, "name": name, "cards_remaining": cards, "hours_played": hours}
}

func asBool(v any) bool { b, _ := v.(bool); return b }

func requiredInputKey(v any) any {
	names := map[int]string{1: "login", 2: "password", 3: "steam_guard", 4: "parental_code", 5: "two_factor",
		6: "cryptkey", 7: "device_confirmation", 8: "qr_code"}
	byName := map[string]string{"login": "login", "password": "password", "steamguard": "steam_guard",
		"steamparentalcode": "parental_code", "twofactorauthentication": "two_factor", "cryptkey": "cryptkey",
		"deviceconfirmation": "device_confirmation", "qrcodelogin": "qr_code"}
	switch x := v.(type) {
	case float64:
		if k, ok := names[int(x)]; ok {
			return k
		}
		if x != 0 {
			return "other"
		}
	case string:
		if k, ok := byName[strings.ToLower(x)]; ok {
			return k
		}
		if !strings.EqualFold(x, "none") && x != "" {
			return "other"
		}
	}
	return nil
}

func summarizeBot(name string, bot map[string]any) M {
	cf, _ := bot["CardsFarmer"].(map[string]any)
	current := []M{}
	if list, ok := cf["CurrentGamesFarming"].([]any); ok {
		for _, g := range list {
			if gm, ok := g.(map[string]any); ok {
				current = append(current, gameInfo(gm))
			}
		}
	}
	queue, _ := cf["GamesToFarm"].([]any)
	var cardsTotal int64
	queueCount := 0
	for _, g := range queue {
		if gm, ok := g.(map[string]any); ok {
			queueCount++
			if f, ok := num(gm["CardsRemaining"]); ok {
				cardsTotal += int64(f)
			}
		}
	}
	requiredInput := requiredInputKey(bot["RequiredInput"])
	needsInput := requiredInput != nil
	var nickname any
	if s, ok := bot["Nickname"].(string); ok && s != "" {
		nickname = truncate(s, 64)
	}
	idle := []int64{}
	if bc, ok := bot["BotConfig"].(map[string]any); ok {
		if list, ok := bc["GamesPlayedWhileIdle"].([]any); ok {
			for _, g := range list {
				if f, ok := num(g); ok && f > 0 && len(idle) < maxGamesPlayedConcurrently {
					idle = append(idle, int64(f))
				}
			}
		}
	}
	qrURL, _ := bot["QrChallengeURL"].(string)
	var cardsRemaining, timeRemaining any
	if queueCount > 0 {
		cardsRemaining = cardsTotal
		timeRemaining = timespanSeconds(cf["TimeRemaining"])
	}
	return M{
		"name":             name,
		"nickname":         nickname,
		"online":           asBool(bot["IsConnectedAndLoggedOn"]),
		"keep_running":     asBool(bot["KeepRunning"]),
		"playing_possible": asBool(bot["IsPlayingPossible"]),
		"needs_input":      needsInput,
		"required_input":   requiredInput,
		"idle_games":       idle,
		"qr_pending":       qrURL != "",
		"farming": M{
			"now_farming":            asBool(cf["NowFarming"]),
			"paused":                 asBool(cf["Paused"]),
			"current_games":          current,
			"games_to_farm_count":    queueCount,
			"cards_remaining_total":  cardsRemaining,
			"time_remaining_seconds": timeRemaining,
		},
	}
}
