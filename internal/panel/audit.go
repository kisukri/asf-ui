package panel

import (
	"encoding/json"
	"log/slog"
	"strings"
)

func rateLimited(q Q, bucket, key string, limit, window int64) bool {
	r := one(q, "SELECT window_start, count FROM rate_limits WHERE bucket = ? AND key = ?", bucket, key)
	return r != nil && now()-r.Int("window_start") < window && r.Int("count") >= limit
}

func rateHit(q Q, bucket, key string, limit, window int64) bool {
	t := now()
	exec(q, `INSERT INTO rate_limits (bucket, key, window_start, count) VALUES (?, ?, ?, 1)
		ON CONFLICT (bucket, key) DO UPDATE SET
		  count = CASE WHEN ? - window_start >= ? THEN 1 ELSE count + 1 END,
		  window_start = CASE WHEN ? - window_start >= ? THEN ? ELSE window_start END`,
		bucket, key, t, t, window, t, window, t)
	return scalar(q, "SELECT count FROM rate_limits WHERE bucket = ? AND key = ?", bucket, key) <= limit
}

func rateUndo(q Q, bucket, key string) {
	exec(q, "UPDATE rate_limits SET count = MAX(count - 1, 0) WHERE bucket = ? AND key = ?", bucket, key)
}

func rateReset(q Q, bucket, key string) {
	exec(q, "DELETE FROM rate_limits WHERE bucket = ? AND key = ?", bucket, key)
}

var forbiddenKeyParts = []string{"password", "secret", "token", "cookie", "key", "url", "address", "credential"}

func cleanDetail(detail M) any {
	if len(detail) == 0 {
		return nil
	}
	safe := M{}
	for k, v := range detail {
		lk := strings.ToLower(k)
		skip := false
		for _, p := range forbiddenKeyParts {
			if strings.Contains(lk, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		switch x := v.(type) {
		case string:
			safe[k] = truncate(x, 200)
		case int, int64, float64, bool, nil:
			safe[k] = x
		case []int64:
			if len(x) > 50 {
				x = x[:50]
			}
			safe[k] = x
		case []string:
			if len(x) > 50 {
				x = x[:50]
			}
			safe[k] = x
		}
	}
	if len(safe) == 0 {
		return nil
	}
	b, _ := json.Marshal(safe)
	return string(b)
}

type auditOpt struct {
	UserID  int64
	IP      string
	Target  string
	Outcome string
	Detail  M
}

func record(q Q, action, category string, o auditOpt) {
	if o.Outcome == "" {
		o.Outcome = "ok"
	}
	var uid, ip, target any
	if o.UserID != 0 {
		uid = o.UserID
	}
	if o.IP != "" {
		ip = o.IP
	}
	if o.Target != "" {
		target = truncate(o.Target, 120)
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("audit write failed", "action", action)
		}
	}()
	exec(q, "INSERT INTO audit_log (user_id, ip, action, category, target, outcome, detail, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		uid, ip, action, category, target, o.Outcome, cleanDetail(o.Detail), now())
}
