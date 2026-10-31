package panel

import "testing"

func adminViewEnv(t *testing.T) (*testEnv, *fakeASF, int64, *CurrentUser) {
	e := setupEnv(t)
	f := newFakeASF(t)
	f.bots["b1"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	f.bots["b2"] = &fakeBot{Connected: true, Paused: true, Status: 1}
	cid := e.addConn(t, f, "c1")
	assignBot(e.admin, botID(t, cid, "b1"), &e.friend.ID)
	t1 := now()
	exec(db, "INSERT INTO users (id, username, password_hash, is_admin, is_active, created_at, password_changed_at) VALUES (3, 'other', 'x', 0, 1, ?, ?)", t1, t1)
	return e, f, cid, &CurrentUser{ID: 3, Username: "other", IP: "127.0.0.1"}
}

func taskByID(list []M, id int64) M {
	for _, x := range list {
		if x["id"] == id {
			return x
		}
	}
	return nil
}

func TestAdminSeesAndControlsAssignedBot(t *testing.T) {
	e, f, cid, other := adminViewEnv(t)
	if !sameStrings(visibleBotNames(e.admin.ID, cid), []string{"b1", "b2"}) {
		t.Fatalf("admin should see every bot: %v", visibleBotNames(e.admin.ID, cid))
	}
	if !sameStrings(visibleBotNames(e.friend.ID, cid), []string{"b1"}) {
		t.Fatalf("friend sees only their bot: %v", visibleBotNames(e.friend.ID, cid))
	}
	if try(func() { ownedBot(other.ID, cid, "b1") }) == nil || len(visibleBotNames(other.ID, cid)) != 0 {
		t.Fatal("other users must not see the bot")
	}

	ftid := newTask(t, e.friend, cid, "b1", "440", "fr-task-000001")
	atid := newTask(t, e.admin, cid, "b1", "570", "ad-task-000001")
	if got := f.bot("b1").Idle; !sameIDs(got, ids(440, 570)) {
		t.Fatalf("both tasks should idle: %v", got)
	}

	at := listTasks(e.admin.ID, "", 50)
	if x := taskByID(at, ftid); x == nil || x["owner"] != "friend" {
		t.Fatalf("admin should list the friend's task with its owner: %v", x)
	}
	if x := taskByID(at, atid); x == nil || x["owner"] != nil {
		t.Fatalf("admin's own task has no owner label: %v", x)
	}
	if taskCounts(e.admin.ID)["running"] != int64(2) {
		t.Fatalf("admin counts all tasks: %v", taskCounts(e.admin.ID))
	}
	if ft := listTasks(e.friend.ID, "", 50); taskByID(ft, atid) != nil || taskByID(ft, ftid) == nil {
		t.Fatal("friend lists only their own tasks")
	}
	if len(listTasks(other.ID, "", 50)) != 0 {
		t.Fatal("other users see no tasks")
	}

	if r := stopTasks(e.friend, []int64{atid}); r[0]["ok"] != false || taskStatus(atid) != "running" {
		t.Fatal("friend must not stop the admin's task")
	}
	if r := stopTasks(other, []int64{ftid}); r[0]["ok"] != false || taskStatus(ftid) != "running" {
		t.Fatal("other users must not stop the friend's task")
	}
	stopTasks(e.admin, []int64{ftid})
	if taskStatus(ftid) != "stopped" || one(db, "SELECT stop_reason FROM playtime_tasks WHERE id = ?", ftid).Str("stop_reason") != "admin" {
		t.Fatal("admin stops the friend's task, marked as stopped by admin")
	}
	if got := f.bot("b1").Idle; !sameIDs(got, ids(570)) {
		t.Fatalf("stopped game should leave the config: %v", got)
	}

	var bot M
	for _, b := range nowPlaying(e.admin) {
		if b["bot"] == "b1" {
			bot = b
		}
	}
	if bot == nil || bot["assigned_to"] != "friend" {
		t.Fatalf("admin's now-playing shows who the bot is assigned to: %v", bot)
	}
	for _, b := range nowPlaying(e.friend) {
		if b["assigned_to"] != nil {
			t.Fatal("friends don't get assignment labels")
		}
	}
}

func TestAdminSettingsRemovalMarksOtherOwner(t *testing.T) {
	e, _, cid, _ := adminViewEnv(t)
	ftid := newTask(t, e.friend, cid, "b1", "440", "fr-task-000002")
	mustSave(t, e.admin, botSettingsIn{ConnectionID: cid, Bot: "b1", RemoveGames: ids(440), ConfirmStop: ids(ftid)})
	if one(db, "SELECT stop_reason FROM playtime_tasks WHERE id = ?", ftid).Str("stop_reason") != "removed_by_other" {
		t.Fatal("friend's task stopped through settings should say someone else removed it")
	}
}

func sameStrings(a, b []string) bool {
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
