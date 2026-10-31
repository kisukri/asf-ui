package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var appLinkRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)store\.steampowered\.com/(?:agecheck/)?app/(\d+)`),
	regexp.MustCompile(`(?i)steamcommunity\.com/app/(\d+)`),
	regexp.MustCompile(`(?i)steamdb\.info/app/(\d+)`),
	regexp.MustCompile(`(?i)s\.team/a/(\d+)`),
	regexp.MustCompile(`(?i)steam://(?:run|rungameid|store)/(\d+)`),
}

var digitsOnlyRe = regexp.MustCompile(`^\d{1,10}$`)

const (
	namePositiveTTL = 30 * 86400
	nameNegativeTTL = 86400
)

func parseAppID(text string) int64 {
	text = strings.TrimSpace(text)
	var s string
	if digitsOnlyRe.MatchString(text) {
		s = text
	} else {
		for _, re := range appLinkRes {
			if m := re.FindStringSubmatch(text); m != nil {
				s = m[1]
				break
			}
		}
	}
	if s == "" {
		fail(400, "invalid_app_id", "无法识别 App ID。请输入数字 App ID，或粘贴 Steam 商店链接。")
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 || v > maxAppID {
		fail(400, "invalid_app_id", "App ID 超出有效范围。")
	}
	return v
}

func gameNamesOpt(ids []int64, budget int) map[int64]any {
	out := map[int64]any{}
	rows := map[int64]Row{}
	if len(ids) > 0 {
		args := make([]any, len(ids))
		for i, v := range ids {
			args[i] = v
		}
		for _, r := range all(db, "SELECT app_id, name, fetched_at FROM app_names WHERE app_id IN ("+placeholders(len(ids))+")", args...) {
			rows[r.Int("app_id")] = r
		}
	}
	for _, id := range ids {
		r := rows[id]
		if r != nil && nameFresh(r) {
			out[id] = r["name"]
		} else if budget > 0 && cfg.SteamNameLookup {
			budget--
			out[id] = lookupName(db, id)
		} else if r != nil {
			out[id] = r["name"]
		} else {
			out[id] = nil
		}
	}
	return out
}

func nameFresh(r Row) bool {
	ttl := int64(nameNegativeTTL)
	if !r.Null("name") {
		ttl = namePositiveTTL
	}
	return now()-r.Int("fetched_at") < ttl
}

func cachedNames(q Q, ids []int64) map[int64]string {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out
	}
	args := make([]any, len(ids))
	for i, v := range ids {
		args[i] = v
	}
	for _, r := range all(q, "SELECT app_id, name FROM app_names WHERE app_id IN ("+placeholders(len(ids))+") AND name IS NOT NULL", args...) {
		out[r.Int("app_id")] = r.Str("name")
	}
	return out
}

func lookupName(q Q, appID int64) any {
	r := one(q, "SELECT name, fetched_at FROM app_names WHERE app_id = ?", appID)
	if r != nil {
		if nameFresh(r) || !cfg.SteamNameLookup {
			return r["name"]
		}
	} else if !cfg.SteamNameLookup {
		return nil
	}
	name := fetchNameFn(appID)
	var stored any
	if name != "" {
		stored = name
	}
	exec(q, `INSERT INTO app_names (app_id, name, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT(app_id) DO UPDATE SET name = COALESCE(excluded.name, app_names.name), fetched_at = excluded.fetched_at`,
		appID, stored, now())
	if name != "" {
		return name
	}
	if r != nil {
		return r["name"]
	}
	return nil
}

var steamHTTP = &http.Client{Timeout: 4 * time.Second}

var fetchNameFn = fetchName

func fetchName(appID int64) string {
	u := fmt.Sprintf("https://store.steampowered.com/api/appdetails?appids=%d&filters=basic&l=schinese", appID)
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "asf-panel/2.0")
	resp, err := steamHTTP.Do(req)
	if err != nil {
		slog.Info("app name lookup failed", "app_id", appID)
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	var data map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return ""
	}
	e := data[strconv.FormatInt(appID, 10)]
	if !e.Success {
		return ""
	}
	return truncate(strings.TrimSpace(stripCtrl(e.Data.Name, "")), 120)
}
