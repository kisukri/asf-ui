package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

type ctx struct {
	w    http.ResponseWriter
	r    *http.Request
	user *CurrentUser
}

const (
	public = iota
	authed
	adminOnly
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func invalid(fields ...string) *AppError {
	return &AppError{Status: 400, Code: "validation_error", Message: "输入内容不符合要求，请检查后重试。", Fields: fields}
}

func need(ok bool, field string) {
	if !ok {
		panic(invalid(field))
	}
}

func strLen(s string, lo, hi int) bool { n := runeLen(s); return n >= lo && n <= hi }

func (c *ctx) bind(v any) {
	body, err := io.ReadAll(io.LimitReader(c.r.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		panic(invalid("body"))
	}
	if err := json.Unmarshal(body, v); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			panic(invalid(te.Field))
		}
		panic(invalid("body"))
	}
}

func (c *ctx) pathID(name string) int64 {
	n, err := strconv.ParseInt(c.r.PathValue(name), 10, 64)
	need(err == nil, name)
	return n
}

// windows resets the conn if the body is left unread
func drain(r *http.Request) { _, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 64*1024)) }

func handle(mux *http.ServeMux, pattern string, level int, fn func(c *ctx) any) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				drain(r)
				if e, ok := rec.(*AppError); ok {
					writeJSON(w, e.Status, localize(requestLang(r), M{"error": e.toMap()}))
					return
				}

				slog.Error("unhandled panic", "path", r.URL.Path, "error", fmt.Sprintf("%T: %v", rec, rec))
				writeJSON(w, 500, localize(requestLang(r), M{"error": M{"code": "internal_error", "message": "服务器内部错误，请稍后重试。"}}))
			}
		}()
		c := &ctx{w: w, r: r}
		if level >= authed {
			c.user = loadUser(r)
			if c.user == nil {
				fail(401, "unauthenticated", "登录已过期，请重新登录。")
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				sent := r.Header.Get("X-CSRF-Token")
				if sent == "" || !constEq(sent, c.user.CSRF) {
					fail(403, "csrf_failed", "页面已过期，请刷新后重试。")
				}
			}
			if level == adminOnly && !c.user.IsAdmin {
				fail(403, "forbidden", "你没有权限访问此页面。")
			}
		}
		res := fn(c)
		drain(r)
		writeJSON(w, 200, localize(requestLang(r), res))
	})
}

func securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAPI := strings.HasPrefix(r.URL.Path, "/api/")
		if isAPI && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeJSON(w, 415, localize(requestLang(r), M{"error": M{"code": "bad_request", "message": "请求格式不正确。"}}))
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				ok := false
				if cfg.PublicOrigin != "" {
					ok = strings.TrimRight(origin, "/") == cfg.PublicOrigin
				} else if u, err := url.Parse(origin); err == nil {
					ok = u.Host == r.Host
				}
				if !ok {
					writeJSON(w, 403, localize(requestLang(r), M{"error": M{"code": "csrf_failed", "message": "请求来源不被允许。"}}))
					return
				}
			}
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if cfg.CookieSecure {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if isAPI {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type idsIn struct {
	IDs []int64 `json:"ids"`
}

func newRouter(static fs.FS) http.Handler {
	mux := http.NewServeMux()

	handle(mux, "GET /api/auth/me", public, func(c *ctx) any {
		u := loadUser(c.r)
		if u == nil {
			return M{"user": nil}
		}
		return M{
			"user":       M{"id": u.ID, "username": u.Username, "is_admin": u.IsAdmin},
			"csrf_token": u.CSRF, "prefs": getPrefs(u.ID),
			"features": M{"user_connections": u.IsAdmin || cfg.AllowUserConnections},
		}
	})
	handle(mux, "POST /api/auth/register", public, func(c *ctx) any {
		var in struct {
			InviteCode string `json:"invite_code"`
			Username   string `json:"username"`
			Password   string `json:"password"`
		}
		c.bind(&in)
		need(strLen(in.InviteCode, 1, 64), "invite_code")
		need(strLen(in.Username, 1, 32), "username")
		need(strLen(in.Password, 1, 128), "password")
		setSessionCookie(c.w, register(c.r, in.InviteCode, in.Username, in.Password))
		return M{"ok": true}
	})
	handle(mux, "POST /api/auth/login", public, func(c *ctx) any {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		c.bind(&in)
		need(strLen(in.Username, 1, 64), "username")
		need(strLen(in.Password, 1, 128), "password")
		setSessionCookie(c.w, login(c.r, in.Username, in.Password))
		return M{"ok": true}
	})
	handle(mux, "POST /api/auth/logout", authed, func(c *ctx) any {
		logout(c.user)
		clearSessionCookie(c.w)
		return M{"ok": true}
	})

	handle(mux, "POST /api/account/password", authed, func(c *ctx) any {
		var in struct {
			Current string `json:"current_password"`
			New     string `json:"new_password"`
		}
		c.bind(&in)
		need(strLen(in.Current, 1, 128), "current_password")
		need(strLen(in.New, 1, 128), "new_password")
		changePassword(c.user, in.Current, in.New)
		return M{"ok": true, "message": "密码已修改，其他设备上的登录已退出。"}
	})
	handle(mux, "GET /api/account/sessions", authed, func(c *ctx) any { return M{"sessions": listSessions(c.user)} })
	handle(mux, "POST /api/account/sessions/revoke-others", authed, func(c *ctx) any {
		return M{"revoked": revokeOtherSessions(c.user)}
	})
	handle(mux, "PUT /api/account/prefs", authed, func(c *ctx) any {
		var in struct {
			DefaultConnectionID *int64 `json:"default_connection_id"`
			DefaultMinutes      int64  `json:"default_minutes"`
		}
		c.bind(&in)
		need(in.DefaultMinutes >= 0 && in.DefaultMinutes <= maxTargetHours*60, "default_minutes")
		return M{"prefs": setPrefs(c.user, in.DefaultConnectionID, in.DefaultMinutes)}
	})
	handle(mux, "GET /api/account/activity", authed, func(c *ctx) any { return M{"items": activity(c.user)} })

	handle(mux, "GET /api/overview", authed, func(c *ctx) any {
		return M{"targets": targets(c.user.ID), "task_counts": taskCounts(c.user.ID),
			"tasks": listTasks(c.user.ID, "", 50), "farming": farmingOverview(c.user, false)}
	})
	handle(mux, "GET /api/targets", authed, func(c *ctx) any { return M{"targets": targets(c.user.ID)} })
	handle(mux, "GET /api/connections", authed, func(c *ctx) any { return M{"connections": listConnections(c.user.ID)} })
	handle(mux, "POST /api/connections", authed, func(c *ctx) any {
		var in struct {
			Name     string  `json:"name"`
			Address  string  `json:"address"`
			Password *string `json:"ipc_password"`
		}
		c.bind(&in)
		need(strLen(in.Name, 1, 40), "name")
		need(strLen(in.Address, 1, 300), "address")
		pw := ""
		if in.Password != nil {
			pw = *in.Password
		}
		return M{"connection": createConnection(c.user, in.Name, in.Address, pw)}
	})
	handle(mux, "PATCH /api/connections/{id}", authed, func(c *ctx) any {
		var in struct {
			Name          *string `json:"name"`
			Address       *string `json:"address"`
			Password      *string `json:"ipc_password"`
			ClearPassword bool    `json:"clear_password"`
		}
		c.bind(&in)
		need(in.Name == nil || runeLen(*in.Name) <= 40, "name")
		need(in.Address == nil || runeLen(*in.Address) <= 300, "address")
		return M{"connection": updateConnection(c.user, c.pathID("id"), in.Name, in.Address, in.Password, in.ClearPassword)}
	})
	handle(mux, "POST /api/connections/{id}/test", authed, func(c *ctx) any {
		return M{"connection": testConnection(c.user, getOwnedConnection(c.user.ID, c.pathID("id")))}
	})
	handle(mux, "POST /api/connections/{id}/disable", authed, func(c *ctx) any {
		return setConnectionEnabled(c.user, c.pathID("id"), false)
	})
	handle(mux, "POST /api/connections/{id}/enable", authed, func(c *ctx) any {
		return setConnectionEnabled(c.user, c.pathID("id"), true)
	})
	handle(mux, "DELETE /api/connections/{id}", authed, func(c *ctx) any {
		return M{"ok": true, "warnings": deleteConnection(c.user, c.pathID("id"))}
	})

	handle(mux, "GET /api/farming", authed, func(c *ctx) any {
		v := strings.ToLower(c.r.URL.Query().Get("refresh"))
		return M{"connections": farmingOverview(c.user, v == "1" || v == "true")}
	})
	handle(mux, "POST /api/farming/actions", authed, func(c *ctx) any {
		var in struct {
			Action  string   `json:"action"`
			Items   []botRef `json:"items"`
			Minutes *int     `json:"minutes"`
		}
		c.bind(&in)
		need(len(in.Action) <= 32, "action")
		need(len(in.Items) >= 1 && len(in.Items) <= 50, "items")
		for _, it := range in.Items {
			need(strLen(it.Bot, 1, 64), "items.bot")
		}
		return M{"results": farmingPerform(c.user, in.Items, in.Action, in.Minutes)}
	})
	handle(mux, "GET /api/farming/skip", authed, func(c *ctx) any {
		q := c.r.URL.Query()
		cid, err := strconv.ParseInt(q.Get("connection_id"), 10, 64)
		need(err == nil, "connection_id")
		bot := q.Get("bot")
		need(runeLen(bot) <= 64, "bot")
		return skipGet(c.user, cid, bot)
	})
	handle(mux, "POST /api/farming/skip", authed, func(c *ctx) any {
		var in struct {
			ConnectionID int64    `json:"connection_id"`
			Bot          *string  `json:"bot"`
			Op           string   `json:"op"`
			Games        []string `json:"games"`
		}
		c.bind(&in)
		bot := ""
		if in.Bot != nil {
			bot = *in.Bot
		}
		need(runeLen(bot) <= 64, "bot")
		need(len(in.Op) <= 10, "op")
		need(len(in.Games) >= 1 && len(in.Games) <= 50, "games")
		return skipModify(c.user, in.ConnectionID, bot, in.Op, in.Games)
	})

	type botIn struct {
		ConnectionID int64  `json:"connection_id"`
		Bot          string `json:"bot"`
	}
	handle(mux, "GET /api/bots/settings", authed, func(c *ctx) any {
		q := c.r.URL.Query()
		cid, err := strconv.ParseInt(q.Get("connection_id"), 10, 64)
		need(err == nil, "connection_id")
		return botSettings(c.r.Context(), c.user, cid, q.Get("bot"))
	})
	handle(mux, "POST /api/bots/settings", authed, func(c *ctx) any {
		var in botSettingsIn
		c.bind(&in)
		need(strLen(in.Bot, 1, 64), "bot")
		need(len(in.AddGames) <= 64 && len(in.RemoveGames) <= 64 && len(in.ConfirmStop) <= 64, "games")
		return saveBotSettings(c.r.Context(), c.user, in)
	})
	handle(mux, "GET /api/bots/mine", authed, func(c *ctx) any { return myBotsInfo(c.user) })
	handle(mux, "POST /api/bots", authed, func(c *ctx) any {
		var in struct {
			ConnectionID int64  `json:"connection_id"`
			Name         string `json:"name"`
			Method       string `json:"method"`
			SteamLogin   string `json:"steam_login"`
			Password     string `json:"steam_password"`
			AssignTo     *int64 `json:"assign_to"`
		}
		c.bind(&in)
		need(strLen(in.Name, 1, 32), "name")
		need(len(in.Method) <= 10, "method")
		need(runeLen(in.SteamLogin) <= 64, "steam_login")
		need(runeLen(in.Password) <= 256, "steam_password")
		return createBot(c.user, in.ConnectionID, in.Name, in.Method, in.SteamLogin, in.Password, in.AssignTo)
	})
	handle(mux, "POST /api/bots/delete", authed, func(c *ctx) any {
		var in botIn
		c.bind(&in)
		deleteBot(c.user, in.ConnectionID, in.Bot)
		return M{"ok": true}
	})
	handle(mux, "POST /api/bots/qr-start", authed, func(c *ctx) any {
		var in botIn
		c.bind(&in)
		return M{"ok": true, "message": qrStart(c.user, in.ConnectionID, in.Bot)}
	})
	handle(mux, "GET /api/bots/qr", authed, func(c *ctx) any {
		q := c.r.URL.Query()
		cid, err := strconv.ParseInt(q.Get("connection_id"), 10, 64)
		need(err == nil, "connection_id")
		return qrStatus(c.user, cid, q.Get("bot"))
	})
	handle(mux, "POST /api/connections/{id}/user-bots", authed, func(c *ctx) any {
		var in struct {
			Allow bool `json:"allow"`
		}
		c.bind(&in)
		return M{"connection": setAllowUserBots(c.user, c.pathID("id"), in.Allow)}
	})

	handle(mux, "GET /api/now-playing", authed, func(c *ctx) any { return M{"bots": nowPlaying(c.user)} })
	handle(mux, "POST /api/bots/code", authed, func(c *ctx) any {
		var in struct {
			ConnectionID int64  `json:"connection_id"`
			Bot          string `json:"bot"`
			Kind         string `json:"kind"`
			Code         string `json:"code"`
		}
		c.bind(&in)
		need(strLen(in.Bot, 1, 64), "bot")
		need(len(in.Kind) <= 20, "kind")
		need(strLen(in.Code, 1, 16), "code")
		return M{"ok": true, "message": submitCode(c.user, in.ConnectionID, in.Bot, in.Kind, in.Code)}
	})

	type taskIn struct {
		ConnectionID int64    `json:"connection_id"`
		Bot          string   `json:"bot"`
		Game         string   `json:"game"`
		Games        []string `json:"games"`
		Hours        int      `json:"hours"`
		Minutes      int      `json:"minutes"`
		Note         *string  `json:"note"`
		RequestKey   string   `json:"request_key"`
		PauseFarming bool     `json:"pause_farming"`
	}
	checkTask := func(in *taskIn) {
		need(strLen(in.Bot, 1, 64), "bot")
		if len(in.Games) == 0 && in.Game != "" {
			in.Games = []string{in.Game}
		}
		need(len(in.Games) >= 1 && len(in.Games) <= 64, "games")
		for _, g := range in.Games {
			need(strLen(g, 1, 4000), "games")
		}
		need(in.Hours >= 0 && in.Hours <= maxTargetHours, "hours")
		need(in.Minutes >= 0 && in.Minutes <= 59, "minutes")
		need(in.Note == nil || runeLen(*in.Note) <= 200, "note")
	}
	handle(mux, "GET /api/playtime/tasks", authed, func(c *ctx) any {
		return M{"tasks": listTasks(c.user.ID, c.r.URL.Query().Get("status"), 200), "counts": taskCounts(c.user.ID),
			"max_per_bot": maxPlayGames}
	})
	handle(mux, "POST /api/playtime/preview", authed, func(c *ctx) any {
		var in taskIn
		c.bind(&in)
		checkTask(&in)
		return previewTask(c.user, in.ConnectionID, in.Bot, in.Games, in.Hours, in.Minutes)
	})
	handle(mux, "POST /api/playtime/tasks", authed, func(c *ctx) any {
		var in taskIn
		c.bind(&in)
		checkTask(&in)
		need(strLen(in.RequestKey, 8, 64), "request_key")
		res := createTasks(c.r.Context(), c.user, in.ConnectionID, in.Bot, in.Games, in.Hours, in.Minutes, in.Note, in.RequestKey, in.PauseFarming)
		res["task"] = res["tasks"].([]M)[0]
		return res
	})
	handle(mux, "POST /api/playtime/tasks/stop", authed, func(c *ctx) any {
		var in idsIn
		c.bind(&in)
		need(len(in.IDs) >= 1 && len(in.IDs) <= 100, "ids")
		return M{"results": stopTasks(c.user, in.IDs)}
	})
	handle(mux, "GET /api/apps/resolve", authed, func(c *ctx) any {
		q := c.r.URL.Query().Get("q")
		need(strLen(q, 1, 300), "q")
		appID := parseAppID(q)
		if !rateHit(db, "app_lookup", strconv.FormatInt(c.user.ID, 10), 60, 600) {
			var name any
			if n, ok := cachedNames(db, []int64{appID})[appID]; ok {
				name = n
			}
			return M{"app_id": appID, "name": name}
		}
		return M{"app_id": appID, "name": lookupName(db, appID)}
	})

	handle(mux, "GET /api/admin/invites", adminOnly, func(c *ctx) any { return M{"invites": listInvites()} })
	handle(mux, "POST /api/admin/invites", adminOnly, func(c *ctx) any {
		var in struct {
			MaxUses int     `json:"max_uses"`
			Hours   int     `json:"expires_in_hours"`
			Note    *string `json:"note"`
		}
		c.bind(&in)
		need(in.MaxUses >= 1 && in.MaxUses <= 100, "max_uses")
		need(in.Hours >= 1 && in.Hours <= 24*90, "expires_in_hours")
		need(in.Note == nil || runeLen(*in.Note) <= 100, "note")
		return createInvite(c.user, in.MaxUses, in.Hours, in.Note)
	})
	handle(mux, "POST /api/admin/invites/{id}/revoke", adminOnly, func(c *ctx) any {
		revokeInvite(c.user, c.pathID("id"))
		return M{"ok": true}
	})
	handle(mux, "GET /api/admin/users", adminOnly, func(c *ctx) any { return M{"users": listUsers()} })
	handle(mux, "POST /api/admin/users/{id}/disable", adminOnly, func(c *ctx) any {
		return M{"ok": true, "warnings": setUserActive(c.user, c.pathID("id"), false)}
	})
	handle(mux, "POST /api/admin/users/{id}/enable", adminOnly, func(c *ctx) any {
		return M{"ok": true, "warnings": setUserActive(c.user, c.pathID("id"), true)}
	})
	handle(mux, "GET /api/admin/bots", adminOnly, func(c *ctx) any { return listAssignableBots() })
	handle(mux, "POST /api/admin/bots/{id}/assign", adminOnly, func(c *ctx) any {
		var in struct {
			UserID *int64 `json:"user_id"`
		}
		c.bind(&in)
		return M{"ok": true, "warnings": assignBot(c.user, c.pathID("id"), in.UserID)}
	})
	handle(mux, "POST /api/admin/users/{id}/quota", adminOnly, func(c *ctx) any {
		var in struct {
			Quota int `json:"bot_quota"`
		}
		c.bind(&in)
		setUserQuota(c.user, c.pathID("id"), in.Quota)
		return M{"ok": true}
	})
	handle(mux, "GET /api/admin/health", adminOnly, func(c *ctx) any { return adminHealth() })
	handle(mux, "GET /api/admin/audit", adminOnly, func(c *ctx) any { return M{"items": adminAudit()} })
	handle(mux, "/api/", public, func(c *ctx) any {
		panic(newErr(404, "not_found", "接口不存在。"))
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, M{"ok": true}) })
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	index, err := fs.ReadFile(static, "index.html")
	check(err)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			writeJSON(w, 405, localize(requestLang(r), M{"error": M{"code": "method_not_allowed", "message": "请求方式不正确。"}}))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
	return securityMiddleware(mux)
}
