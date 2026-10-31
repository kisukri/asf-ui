package panel

import "testing"

func seqIDs(from, n int64) []int64 {
	out := make([]int64, 0, n)
	for i := int64(0); i < n; i++ {
		out = append(out, from+i)
	}
	return out
}

func droppedAudits() int64 {
	return scalar(db, "SELECT COUNT(*) FROM audit_log WHERE action = 'bot_settings' AND outcome = 'dropped'")
}

func pendingSave(t *testing.T, e *testEnv, f *fakeASF, cid int64, in botSettingsIn) int64 {
	t.Helper()
	f.set(func(f *fakeASF) { f.writeMode = "hang_before_apply" })
	if res := mustSave(t, e.admin, in); res["pending"] != true {
		t.Fatalf("expected pending, got %v", res)
	}
	f.set(func(f *fakeASF) { f.writeMode = "" })
	return botID(t, cid, in.Bot)
}

func expectDropped(t *testing.T, f *fakeASF, bid int64, want []int64, writes int, audits int64) {
	t.Helper()
	sched.syncBot(bid)
	if f.writeCount() != writes {
		t.Fatal("over-capacity pending change must not be written")
	}
	if got := f.bot("b1").Idle; !sameIDs(got, want) {
		t.Fatalf("config must stay as ASF has it: %v", got)
	}
	if !pendingOf(bid).Null("pending_ops") {
		t.Fatal("pending change that cannot be applied must be dropped")
	}
	if droppedAudits() != audits+1 {
		t.Fatal("dropped pending change must be audited")
	}
}

func TestPendingRecoveryRespectsCapacity(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: seqIDs(1000, 31), Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := pendingSave(t, e, f, cid, botSettingsIn{ConnectionID: cid, Bot: "b1", AddGames: []string{"20"}})
	ext := append(seqIDs(1000, 31), 5000)
	f.set(func(f *fakeASF) { f.bots["b1"].Idle = append([]int64(nil), ext...) })
	expectDropped(t, f, bid, ext, f.writeCount(), droppedAudits())
}

func TestPendingRecoveryCountsRemoteCustomText(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: seqIDs(1000, 30), Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := pendingSave(t, e, f, cid, botSettingsIn{ConnectionID: cid, Bot: "b1", AddGames: []string{"20"}})
	ext := append(seqIDs(1000, 30), 5000)
	f.set(func(f *fakeASF) { f.bots["b1"].Idle = append([]int64(nil), ext...); f.bots["b1"].CustomIdle = "hi" })
	expectDropped(t, f, bid, ext, f.writeCount(), droppedAudits())
}

func TestPendingRecoveryCountsPendingCustomText(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: seqIDs(1000, 31), Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := pendingSave(t, e, f, cid, botSettingsIn{ConnectionID: cid, Bot: "b1", CustomIdle: &textChange{Value: "hi", Base: ""}})
	ext := append(seqIDs(1000, 31), 5000)
	f.set(func(f *fakeASF) { f.bots["b1"].Idle = append([]int64(nil), ext...) })
	expectDropped(t, f, bid, ext, f.writeCount(), droppedAudits())
	if f.bot("b1").CustomIdle != nil {
		t.Fatal("custom text from the dropped change must not be written")
	}
}

func TestPendingDroppedKeepsTasks(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: seqIDs(1000, 30), Status: 1}
	cid := e.addConn(t, f, "c1")
	tid := newTask(t, e.admin, cid, "b1", "440", "dropped-key-0001")
	bid := pendingSave(t, e, f, cid, botSettingsIn{ConnectionID: cid, Bot: "b1",
		RemoveGames: ids(440), ConfirmStop: ids(tid), CustomIdle: &textChange{Value: "hi", Base: ""}, AddGames: []string{"20"}})
	ext := append(seqIDs(1000, 30), 440, 5000)
	f.set(func(f *fakeASF) { f.bots["b1"].Idle = append([]int64(nil), ext...) })
	expectDropped(t, f, bid, ext, f.writeCount(), droppedAudits())
	if taskStatus(tid) == "stopped" {
		t.Fatal("task must not be stopped by a dropped change")
	}
}

func TestPendingRecoveryWithinCapacityStillApplies(t *testing.T) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Idle: seqIDs(1000, 30), Status: 1}
	cid := e.addConn(t, f, "c1")
	bid := pendingSave(t, e, f, cid, botSettingsIn{ConnectionID: cid, Bot: "b1", AddGames: []string{"20"}})
	ext := append(seqIDs(1000, 30), 5000)
	f.set(func(f *fakeASF) { f.bots["b1"].Idle = append([]int64(nil), ext...) })
	sched.syncBot(bid)
	if got := f.bot("b1").Idle; !sameIDs(got, append(append([]int64(nil), ext...), 20)) || !pendingOf(bid).Null("pending_ops") {
		t.Fatalf("pending change within capacity should be applied: %v", got)
	}
}
