package panel

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func mustSave(t *testing.T, u *CurrentUser, in botSettingsIn) M {
	t.Helper()
	var res M
	if err := try(func() { res = saveBotSettings(context.Background(), u, in) }); err != nil {
		t.Fatalf("save failed: %s %s", err.Code, err.Message)
	}
	return res
}

func saveErr(u *CurrentUser, in botSettingsIn) *AppError {
	return try(func() { saveBotSettings(context.Background(), u, in) })
}

func newTask(t *testing.T, u *CurrentUser, cid int64, bot, game, key string) int64 {
	t.Helper()
	var res M
	if err := try(func() { res = createTasks(context.Background(), u, cid, bot, []string{game}, 0, 0, nil, key, false) }); err != nil {
		t.Fatalf("createTasks: %v", err)
	}
	return res["tasks"].([]M)[0]["id"].(int64)
}

func TestStaleDialogKeepsNewGames(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	tid := newTask(t, e.admin, cid, "b1", "440", "stale-key-0001")
	if got := f.bot("b1").Idle; !sameIDs(got, ids(730, 440)) {
		t.Fatalf("task game not written: %v", got)
	}
	mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", OnlineStatus: &statusChange{Value: 7, Base: 1}})
	if b := f.bot("b1"); !sameIDs(b.Idle, ids(730, 440)) || b.Status != 7 {
		t.Fatalf("stale save changed games: idle=%v status=%d", b.Idle, b.Status)
	}
	if s := taskStatus(tid); s != "running" {
		t.Fatalf("task should keep running, got %s", s)
	}
}

func TestRemoveRequiresConfirmOfActualTasks(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	tid := newTask(t, e.admin, cid, "b1", "440", "confirm-key-01")
	writes := f.writeCount()
	err := saveErr(e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", RemoveGames: ids(440), ConfirmStop: ids(9999)})
	if err == nil || err.Code != "confirm_required" {
		t.Fatalf("expected confirm_required, got %v", err)
	}
	if tasks := err.Extra["tasks"].([]M); len(tasks) != 1 || tasks[0]["id"].(int64) != tid {
		t.Fatalf("confirm list should name the real task: %v", tasks)
	}
	if f.writeCount() != writes || taskStatus(tid) != "running" {
		t.Fatal("nothing should change before confirmation")
	}
	mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", RemoveGames: ids(440), ConfirmStop: ids(tid)})
	if got := f.bot("b1").Idle; !sameIDs(got, ids(730)) || taskStatus(tid) != "stopped" {
		t.Fatalf("after confirm: idle=%v task=%s", got, taskStatus(tid))
	}
}

func TestFieldLevelConflict(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cid := e.addConn(t, f, "c1")
	mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", OnlineStatus: &statusChange{Value: 3, Base: 1}})
	err := saveErr(e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", OnlineStatus: &statusChange{Value: 5, Base: 1}})
	if err == nil || err.Code != "settings_conflict" || err.Extra["settings"] == nil {
		t.Fatalf("expected conflict with latest settings, got %v", err)
	}
	if f.bot("b1").Status != 3 {
		t.Fatal("conflicting save must not overwrite")
	}
	mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", CustomIdle: &textChange{Value: "挂机中", Base: ""}})
	if b := f.bot("b1"); b.Status != 3 || b.CustomIdle != "挂机中" {
		t.Fatalf("other field save should keep status: %+v", b)
	}
	if f.lostEnabled {
		t.Fatal("config write dropped Enabled")
	}
}

func TestNoChangeNoWrite(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 2}
	cid := e.addConn(t, f, "c1")
	writes := f.writeCount()
	res := mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", OnlineStatus: &statusChange{Value: 2, Base: 2},
		AddGames: []string{"730"}, CustomIdle: &textChange{Value: "", Base: ""}})
	if res["changed"] != false || f.writeCount() != writes {
		t.Fatalf("no-op save wrote config: changed=%v writes %d→%d", res["changed"], writes, f.writeCount())
	}
}

func TestCapacityRules(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	var full []int64
	for i := int64(1); i <= 32; i++ {
		full = append(full, i)
	}
	f.bots["full"] = &fakeBot{Connected: true, Paused: true, Idle: full, Status: 1}
	var user31 []int64
	for i := int64(101); i <= 131; i++ {
		user31 = append(user31, i)
	}
	f.bots["c31"] = &fakeBot{Connected: true, Paused: true, Idle: user31, Status: 1, CustomIdle: "挂机中"}
	cid := e.addConn(t, f, "c1")
	writes := f.writeCount()
	err := saveErr(e.admin, botSettingsIn{ConnectionID: cid, Bot: "full", CustomIdle: &textChange{Value: "文字", Base: ""}})
	if err == nil || err.Code != "too_many_games" || f.writeCount() != writes {
		t.Fatalf("custom text with 32 games must be rejected without writing, got %v", err)
	}
	tid := newTask(t, e.admin, cid, "c31", "999", "capacity-key-1")
	if s := taskStatus(tid); s != "queued" {
		t.Fatalf("task should wait for a slot, got %s", s)
	}
	for _, g := range f.bot("c31").Idle {
		if g == 999 {
			t.Fatal("queued task must not be written into config")
		}
	}
	if one(db, "SELECT elapsed_seconds FROM playtime_tasks WHERE id = ?", tid).Float("elapsed_seconds") != 0 {
		t.Fatal("queued task must not accrue time")
	}
}

func TestUncertainWriteRecovers(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	tid := newTask(t, e.admin, cid, "b1", "440", "uncertain-key1")
	bid := botID(t, cid, "b1")
	f.set(func(f *fakeASF) { f.writeMode = "hang_after_apply" })
	before := f.writeCount()
	res := mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", RemoveGames: ids(440), ConfirmStop: ids(tid)})
	if res["pending"] != true || res["saved"] != false {
		t.Fatalf("expected pending result, got %v", res)
	}
	if one(db, "SELECT pending_ops FROM bot_play_state WHERE bot_id = ?", bid).Null("pending_ops") {
		t.Fatal("pending record should be kept")
	}
	if taskStatus(tid) != "running" {
		t.Fatal("task must not be stopped before the write is confirmed")
	}
	f.set(func(f *fakeASF) { f.writeMode = "" })
	sched.syncBot(bid)
	if !one(db, "SELECT pending_ops FROM bot_play_state WHERE bot_id = ?", bid).Null("pending_ops") {
		t.Fatal("pending record should be resolved")
	}
	if taskStatus(tid) != "stopped" || !sameIDs(f.bot("b1").Idle, ids(730)) {
		t.Fatalf("after recovery: task=%s idle=%v", taskStatus(tid), f.bot("b1").Idle)
	}
	if f.writeCount() != before+1 {
		t.Fatalf("recovery must not write again: %d writes", f.writeCount()-before)
	}
}

func TestRejectedWriteLeavesNothingPending(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	f.set(func(f *fakeASF) { f.writeMode = "reject" })
	if err := saveErr(e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", AddGames: []string{"20"}}); err == nil {
		t.Fatal("rejected write should report an error")
	}
	if !one(db, "SELECT pending_ops FROM bot_play_state WHERE bot_id = ?", botID(t, cid, "b1")).Null("pending_ops") {
		t.Fatal("rejected write must not stay pending")
	}
}

func TestSavedEvenIfRefreshFails(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cid := e.addConn(t, f, "c1")
	f.set(func(f *fakeASF) { f.writes = 0; f.failGetAfterWrite = true })
	res := mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", OnlineStatus: &statusChange{Value: 7, Base: 1}})
	if res["saved"] != true || res["refresh_error"] == nil {
		t.Fatalf("expected saved with refresh warning, got %v", res)
	}
	if f.bot("b1").Status != 7 {
		t.Fatal("write should have happened")
	}
}

func TestRevokedWhileWaitingForLock(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := botID(t, cid, "b1")
	assignBot(e.admin, bid, &e.friend.ID)
	writes := f.writeCount()
	unlock := lockBot(context.Background(), bid)
	done := make(chan *AppError, 1)
	go func() {
		done <- saveErr(e.friend, botSettingsIn{ConnectionID: cid, Bot: "b1", OnlineStatus: &statusChange{Value: 7, Base: 1}})
	}()
	time.Sleep(200 * time.Millisecond)
	exec(db, "DELETE FROM bot_assignments WHERE bot_id = ?", bid)
	unlock()
	if err := <-done; err == nil {
		t.Fatal("save should be refused after the bot was taken back")
	}
	if f.writeCount() != writes || f.bot("b1").Status != 1 {
		t.Fatal("no config write may happen after revocation")
	}
}

func TestCancelledRequestStopsWaiting(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cid := e.addConn(t, f, "c1")
	unlock := lockBot(context.Background(), botID(t, cid, "b1"))
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := try(func() {
		saveBotSettings(ctx, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", OnlineStatus: &statusChange{Value: 7, Base: 1}})
	})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("expected quick refusal, got %v after %v", err, time.Since(start))
	}
}

func TestSlowConnectionDoesNotBlockOthers(t *testing.T) {
	e := setupEnv(t)
	slow, fast := newFakeASF(t), newFakeASF(t)
	slow.bots["s1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	fast.bots["f1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cs, cf := e.addConn(t, slow, "slow"), e.addConn(t, fast, "fast")
	newTask(t, e.admin, cs, "s1", "10", "slow-task-key1")
	newTask(t, e.admin, cf, "f1", "20", "fast-task-key1")

	slow.set(func(f *fakeASF) { f.bots["s1"].Idle = nil; f.getDelay = 3 * time.Second })
	fast.set(func(f *fakeASF) { f.bots["f1"].Idle = nil })
	exec(db, "UPDATE scheduler_state SET lease_until = 0")
	fastWrites := fast.writeCount()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); sched.tick() }()
	deadline := time.Now().Add(1500 * time.Millisecond)
	for fast.writeCount() == fastWrites && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if fast.writeCount() == fastWrites {
		t.Fatal("fast connection was blocked by the slow one")
	}
	wg.Wait()
}

func TestLoginReservesAttemptsBeforeHashing(t *testing.T) {
	setupEnv(t)
	h := hashPassword("correct horse battery")
	exec(db, "INSERT INTO users (username, password_hash, is_admin, is_active, created_at, password_changed_at) VALUES ('u1', ?, 0, 1, 0, 0)", h)
	before := hashCalls.Load()
	hashPeak.Store(0)
	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/auth/login", nil)
			req.RemoteAddr = "192.0.2.10:1234"
			_ = try(func() { login(req, "u1", "wrong password!") })
		}()
	}
	wg.Wait()
	if n := hashCalls.Load() - before; n > loginUserLimit[0] {
		t.Fatalf("expected at most %d hashes, got %d", loginUserLimit[0], n)
	}
	if p := hashPeak.Load(); p > maxConcurrentHashes {
		t.Fatalf("concurrent hashes %d exceed cap %d", p, maxConcurrentHashes)
	}
}

func TestStaleGameNamesRefresh(t *testing.T) {
	setupEnv(t)
	cfg.SteamNameLookup = true
	calls := 0
	fetchNameFn = func(id int64) string { calls++; return fmt.Sprintf("New %d", id) }
	exec(db, "INSERT INTO app_names (app_id, name, fetched_at) VALUES (1, 'Old', ?), (2, 'Fresh', ?)", now()-40*86400, now())
	names := gameNamesOpt(ids(1, 2), 5)
	if names[1] != "New 1" || names[2] != "Fresh" || calls != 1 {
		t.Fatalf("names=%v calls=%d", names, calls)
	}
	exec(db, "UPDATE app_names SET fetched_at = ? WHERE app_id = 1", now()-40*86400)
	if gameNamesOpt(ids(1), 0)[1] != "New 1" || calls != 1 {
		t.Fatal("without budget, stale name is shown and no lookup is made")
	}
}
