package panel

import (
	"context"
	"sort"
	"sync"
	"time"
)

var (
	botLocksMu sync.Mutex
	botLocks   = map[int64]chan struct{}{}
)

func botLockChan(id int64) chan struct{} {
	botLocksMu.Lock()
	defer botLocksMu.Unlock()
	c, ok := botLocks[id]
	if !ok {
		c = make(chan struct{}, 1)
		botLocks[id] = c
	}
	return c
}

func lockBot(ctx context.Context, id int64) func() {
	c := botLockChan(id)
	select {
	case c <- struct{}{}:
		return func() { <-c }
	case <-ctx.Done():
		panic(newErr(503, "busy", "这个 Bot 正在被其他操作修改，请稍后再试。"))
	}
}

func lockBots(ctx context.Context, ids []int64) func() {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var unlocks []func()
	release := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
	defer func() {
		if r := recover(); r != nil {
			release()
			panic(r)
		}
	}()
	seen := map[int64]bool{}
	for _, id := range sorted {
		if !seen[id] {
			seen[id] = true
			unlocks = append(unlocks, lockBot(ctx, id))
		}
	}
	return release
}

func adminLockCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

const maxConcurrentHashes = 3

var hashSlots = make(chan struct{}, maxConcurrentHashes)

var settingsSlots = make(chan struct{}, 8)

func acquireSettingsSlot(ctx context.Context) func() {
	select {
	case settingsSlots <- struct{}{}:
		return func() { <-settingsSlots }
	case <-ctx.Done():
		panic(newErr(503, "busy", "服务器繁忙，请稍后再试。"))
	case <-time.After(20 * time.Second):
		panic(newErr(503, "busy", "服务器繁忙，请稍后再试。"))
	}
}
