package panel

import (
	"fmt"
	"testing"
)

func refreshConn(e *testEnv, cid int64) {
	exec(db, "DELETE FROM rate_limits")
	testConnection(e.admin, getOwnedConnection(e.admin.ID, cid))
}

func friendCanUse(e *testEnv, cid int64, bot string) bool {
	return try(func() { ownedBot(e.friend.ID, cid, bot) }) == nil
}

func assertForgotten(t *testing.T, bid int64) {
	t.Helper()
	if n := scalar(db, "SELECT COUNT(*) FROM bot_assignments WHERE bot_id = ?", bid); n != 0 {
		t.Fatalf("assignment must be removed, got %d", n)
	}
	if !one(db, "SELECT created_by FROM bots WHERE id = ?", bid).Null("created_by") {
		t.Fatal("creator must be cleared")
	}
	if scalar(db, "SELECT COUNT(*) FROM audit_log WHERE action = 'bot_unassign' AND target = ?", fmt.Sprintf("bot:%d", bid)) == 0 {
		t.Fatal("automatic unassignment must be audited")
	}
}

func TestVanishedBotOnRefreshLosesAssignment(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	f.bots["keep"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := botID(t, cid, "b1")
	assignBot(e.admin, bid, &e.friend.ID)
	exec(db, "UPDATE bots SET created_by = ? WHERE id = ?", e.friend.ID, bid)
	tid := newTask(t, e.friend, cid, "b1", "440", "vanish-key-0001")

	f.set(func(f *fakeASF) { delete(f.bots, "b1") })
	refreshConn(e, cid)
	assertForgotten(t, bid)
	if taskStatus(tid) != "failed" {
		t.Fatalf("task on the vanished bot must end, got %s", taskStatus(tid))
	}
	if n := botsCreatedBy(e.friend.ID); n != 0 {
		t.Fatalf("vanished bot must not count toward quota, got %d", n)
	}

	f.set(func(f *fakeASF) { f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1} })
	refreshConn(e, cid)
	if friendCanUse(e, cid, "b1") {
		t.Fatal("old assignee must not control a new bot with the same name")
	}
	if try(func() { deleteBot(e.friend, cid, "b1") }) == nil {
		t.Fatal("old creator must not delete the new bot")
	}
	if try(func() { ownedBot(e.admin.ID, cid, "b1") }) != nil {
		t.Fatal("the new bot should belong to the connection owner")
	}
}

func TestVanishedBotOnSchedulerLosesAssignment(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := botID(t, cid, "b1")
	assignBot(e.admin, bid, &e.friend.ID)
	tid := newTask(t, e.friend, cid, "b1", "440", "vanish-key-0002")
	exec(db, "UPDATE bot_play_state SET pending_ops = ? WHERE bot_id = ?", `{"user_id":2,"add":[20],"at":1}`, bid)

	f.set(func(f *fakeASF) { delete(f.bots, "b1") })
	sched.syncBot(bid)
	assertForgotten(t, bid)
	if taskStatus(tid) != "failed" || !pendingOf(bid).Null("pending_ops") {
		t.Fatal("tasks and pending changes of the vanished bot must be cleared")
	}

	f.set(func(f *fakeASF) { f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1} })
	refreshConn(e, cid)
	if friendCanUse(e, cid, "b1") {
		t.Fatal("old assignee must not control a new bot with the same name")
	}
	sched.syncBot(bid)
	if got := f.bot("b1").Idle; len(got) != 0 {
		t.Fatalf("old pending change must not be applied to the new bot: %v", got)
	}
}

func TestRefreshKeepsAssignmentOfPresentBot(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := botID(t, cid, "b1")
	assignBot(e.admin, bid, &e.friend.ID)
	refreshConn(e, cid)
	if !friendCanUse(e, cid, "b1") {
		t.Fatal("assignment of a present bot must be kept")
	}
}
