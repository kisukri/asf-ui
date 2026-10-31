package panel

import (
	"context"
	"testing"
)

func pendingOf(bid int64) Row {
	return one(db, "SELECT pending_ops FROM bot_play_state WHERE bot_id = ?", bid)
}

func TestSchedulerUncertainWriteKeepsOwnership(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	f.set(func(f *fakeASF) { f.writeMode = "hang_after_apply" })
	tid := newTask(t, e.admin, cid, "b1", "440", "own-key-000001")
	f.set(func(f *fakeASF) { f.writeMode = "" })
	if got := f.bot("b1").Idle; !sameIDs(got, ids(730, 440)) {
		t.Fatalf("write should have taken effect remotely: %v", got)
	}
	if err := try(func() { stopTasks(e.admin, []int64{tid}) }); err != nil {
		t.Fatalf("stopTasks: %v", err)
	}
	if got := f.bot("b1").Idle; !sameIDs(got, ids(730)) {
		t.Fatalf("panel game must be cleaned up, user game kept; got %v", got)
	}
}

func TestSchedulerUncertainRemovalRetried(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	tid := newTask(t, e.admin, cid, "b1", "440", "own-key-000002")
	f.set(func(f *fakeASF) { f.writeMode = "hang_before_apply" })
	_ = try(func() { stopTasks(e.admin, []int64{tid}) })
	if got := f.bot("b1").Idle; !sameIDs(got, ids(730, 440)) {
		t.Fatalf("removal should not have taken effect yet: %v", got)
	}
	f.set(func(f *fakeASF) { f.writeMode = "" })
	sched.syncBot(botID(t, cid, "b1"))
	if got := f.bot("b1").Idle; !sameIDs(got, ids(730)) {
		t.Fatalf("removal must be retried: %v", got)
	}
}

func TestCreateTaskRevokedDuringNameLookup(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := botID(t, cid, "b1")
	assignBot(e.admin, bid, &e.friend.ID)
	cfg.SteamNameLookup = true
	started, release := make(chan struct{}), make(chan struct{})
	fetchNameFn = func(int64) string { close(started); <-release; return "" }
	writes := f.writeCount()
	done := make(chan *AppError, 1)
	go func() {
		done <- try(func() {
			createTasks(context.Background(), e.friend, cid, "b1", []string{"440"}, 0, 0, nil, "revoke-key-0001", false)
		})
	}()
	<-started
	assignBot(e.admin, bid, nil)
	close(release)
	if err := <-done; err == nil {
		t.Fatal("task creation must be refused after the bot was taken back")
	}
	if n := scalar(db, "SELECT COUNT(*) FROM playtime_tasks WHERE user_id = ? AND status IN ('running','queued')", e.friend.ID); n != 0 {
		t.Fatalf("no active task may exist for the old owner, got %d", n)
	}
	if f.writeCount() != writes || !sameIDs(f.bot("b1").Idle, ids(730)) {
		t.Fatal("no config write may happen")
	}
}

func TestPendingRecoveryWaitsWhileBotStopped(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := botID(t, cid, "b1")
	f.set(func(f *fakeASF) { f.writeMode = "hang_before_apply" })
	if res := mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", AddGames: []string{"20"}}); res["pending"] != true {
		t.Fatalf("expected pending, got %v", res)
	}
	f.set(func(f *fakeASF) { f.writeMode = ""; f.bots["b1"].Connected = false })
	writes := f.writeCount()
	sched.syncBot(bid)
	if f.writeCount() != writes {
		t.Fatal("must not write config while the bot is stopped")
	}
	if pendingOf(bid).Null("pending_ops") {
		t.Fatal("pending change must be kept until it can be applied")
	}
	f.set(func(f *fakeASF) { f.bots["b1"].Connected = true })
	sched.syncBot(bid)
	if got := f.bot("b1").Idle; !sameIDs(got, ids(730, 20)) || !pendingOf(bid).Null("pending_ops") {
		t.Fatalf("pending change should be applied after start: idle=%v", got)
	}
}

func TestPendingAlreadyAppliedFinalizesWhileStopped(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: ids(730), Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := botID(t, cid, "b1")
	tid := newTask(t, e.admin, cid, "b1", "440", "applied-key-001")
	f.set(func(f *fakeASF) { f.writeMode = "hang_after_apply" })
	mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", RemoveGames: ids(440), ConfirmStop: ids(tid)})
	f.set(func(f *fakeASF) { f.writeMode = ""; f.bots["b1"].Connected = false })
	writes := f.writeCount()
	sched.syncBot(bid)
	if f.writeCount() != writes {
		t.Fatal("already-applied change must not be written again")
	}
	if !pendingOf(bid).Null("pending_ops") || taskStatus(tid) != "stopped" {
		t.Fatalf("local follow-up should finish: pending cleared? %v task=%s", pendingOf(bid).Null("pending_ops"), taskStatus(tid))
	}
}
