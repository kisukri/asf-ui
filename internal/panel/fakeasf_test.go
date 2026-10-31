package panel

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBot struct {
	Connected  bool
	Paused     bool
	Cards      int
	Idle       []int64
	Status     int
	CustomIdle any
	CustomFarm any
}

type fakeASF struct {
	mu                sync.Mutex
	bots              map[string]*fakeBot
	writes            int
	getDelay          time.Duration
	writeMode         string
	failGetAfterWrite bool
	lostEnabled       bool
	srv               *httptest.Server
}

const fakePassword = "fake-ipc-pw"

// just enough of asf's ipc to poke at the edge cases
func newFakeASF(t *testing.T) *fakeASF {
	f := &fakeASF{bots: map[string]*fakeBot{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeASF) bot(name string) *fakeBot {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := *f.bots[name]
	b.Idle = append([]int64(nil), b.Idle...)
	return &b
}

func (f *fakeASF) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func (f *fakeASF) set(fn func(f *fakeASF)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func reply(w http.ResponseWriter, code int, ok bool, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"Success": ok, "Message": "OK", "Result": result})
}

func (f *fakeASF) botJSON(name string, b *fakeBot) map[string]any {
	idle := b.Idle
	if idle == nil {
		idle = []int64{}
	}
	return map[string]any{
		"BotName": name, "IsConnectedAndLoggedOn": b.Connected, "KeepRunning": b.Connected, "IsPlayingPossible": true,
		"RequiredInput": 0,
		"BotConfig": map[string]any{"Enabled": true, "FarmingPreferences": 0, "GamesPlayedWhileIdle": idle,
			"OnlineStatus": b.Status, "CustomGamePlayedWhileIdle": b.CustomIdle, "CustomGamePlayedWhileFarming": b.CustomFarm},
		"CardsFarmer": map[string]any{"NowFarming": !b.Paused && b.Cards > 0, "Paused": b.Paused,
			"CurrentGamesFarming": []any{}, "GamesToFarm": []any{}, "TimeRemaining": "00:00:00"},
	}
}

func (f *fakeASF) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authentication") != fakePassword {
		reply(w, 401, false, nil)
		return
	}
	path := r.URL.EscapedPath()
	switch {
	case r.Method == "GET" && path == "/Api/ASF":
		reply(w, 200, true, map[string]any{"Version": "6.1.0.0", "GlobalConfig": map[string]any{"Headless": false}})
	case r.Method == "GET" && strings.HasPrefix(path, "/Api/Bot/"):
		f.mu.Lock()
		delay, fail := f.getDelay, f.failGetAfterWrite && f.writes > 0
		if fail {
			f.failGetAfterWrite = false
		}
		f.mu.Unlock()
		time.Sleep(delay)
		if fail {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, "boom")
			return
		}
		f.mu.Lock()
		out := map[string]any{}
		for _, raw := range strings.Split(strings.TrimPrefix(path, "/Api/Bot/"), ",") {
			name, _ := url.PathUnescape(raw)
			if name == "ASF" {
				for n, b := range f.bots {
					out[n] = f.botJSON(n, b)
				}
			} else if b, ok := f.bots[name]; ok {
				out[name] = f.botJSON(name, b)
			}
		}
		f.mu.Unlock()
		reply(w, 200, true, out)
	case r.Method == "POST" && strings.Count(path, "/") == 3:
		name, _ := url.PathUnescape(strings.TrimPrefix(path, "/Api/Bot/"))
		var body struct {
			BotConfig map[string]any `json:"BotConfig"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		mode := f.writeMode
		b := f.bots[name]
		if mode == "reject" || b == nil {
			f.mu.Unlock()
			reply(w, 400, false, nil)
			return
		}
		if mode == "hang_before_apply" {
			f.mu.Unlock()
			time.Sleep(3 * time.Second)
			return
		}
		b.Idle = nil
		if list, ok := body.BotConfig["GamesPlayedWhileIdle"].([]any); ok {
			for _, g := range list {
				b.Idle = append(b.Idle, int64(g.(float64)))
			}
		}
		if st, ok := body.BotConfig["OnlineStatus"].(float64); ok {
			b.Status = int(st)
		}
		b.CustomIdle = body.BotConfig["CustomGamePlayedWhileIdle"]
		b.CustomFarm = body.BotConfig["CustomGamePlayedWhileFarming"]
		if body.BotConfig["Enabled"] != true {
			f.lostEnabled = true
		}
		f.writes++
		f.mu.Unlock()
		if mode == "hang_after_apply" {
			time.Sleep(3 * time.Second)
		}
		reply(w, 200, true, map[string]bool{name: true})
	case r.Method == "POST":
		reply(w, 200, true, nil)
	default:
		reply(w, 404, false, nil)
	}
}

type testEnv struct {
	admin, friend *CurrentUser
}

func setupEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg = &Settings{
		DatabasePath: filepath.Join(dir, "t.db"), SessionIdleMinutes: 60, SessionAbsoluteHours: 24,
		AllowedPorts: [][2]int{{1, 65535}}, DeniedPorts: map[int]bool{}, RequireHTTPS: true, TimeoutSeconds: 2,
		MaxResponseBytes: 1 << 20, MaxConnectionsPerUser: 10, SchedulerInterval: 10, SchedulerReassert: 300,
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	var err error
	if box, err = newCredentialBox("k1:" + base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	db = openDB(cfg.DatabasePath)
	t.Cleanup(func() { db.Close() })
	migrate(db, os.DirFS("../.."))
	sched = &Scheduler{holder: "test"}
	farmCacheMu.Lock()
	farmCache = map[int64]farmCacheEntry{}
	farmCacheMu.Unlock()
	fetchNameFn = func(int64) string { return "" }
	t1 := now()
	exec(db, "INSERT INTO users (id, username, password_hash, is_admin, is_active, created_at, password_changed_at) VALUES (1, 'admin', 'x', 1, 1, ?, ?)", t1, t1)
	exec(db, "INSERT INTO users (id, username, password_hash, is_admin, is_active, created_at, password_changed_at) VALUES (2, 'friend', 'x', 0, 1, ?, ?)", t1, t1)
	return &testEnv{
		admin:  &CurrentUser{ID: 1, Username: "admin", IsAdmin: true, IP: "127.0.0.1"},
		friend: &CurrentUser{ID: 2, Username: "friend", IP: "127.0.0.1"},
	}
}

func (e *testEnv) addConn(t *testing.T, f *fakeASF, name string) int64 {
	t.Helper()
	var res M
	if err := try(func() { res = createConnection(e.admin, name, f.srv.URL, fakePassword) }); err != nil {
		t.Fatalf("createConnection: %v", err)
	}
	if res["status"] != "ok" {
		t.Fatalf("connection status %v: %v", res["status"], res["status_message"])
	}
	exec(db, "DELETE FROM rate_limits")
	return res["id"].(int64)
}

func botID(t *testing.T, cid int64, name string) int64 {
	r := one(db, "SELECT id FROM bots WHERE connection_id = ? AND name = ?", cid, name)
	if r == nil {
		t.Fatalf("bot %s not registered", name)
	}
	return r.Int("id")
}

func taskStatus(id int64) string {
	return one(db, "SELECT status FROM playtime_tasks WHERE id = ?", id).Str("status")
}

func ids(v ...int64) []int64 { return v }

func sameIDs(a, b []int64) bool {
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
