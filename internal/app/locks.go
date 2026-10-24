package app

import (
	"context"
	"sync"
)

// All operations on an account share its immutable openid lock. Reference
// counting prevents expired scans and deleted accounts accumulating entries.
type accountLocks struct {
	mu      sync.Mutex
	entries map[string]*lockEntry
}

type lockEntry struct {
	token chan struct{}
	refs  int
}

func (locks *accountLocks) acquire(ctx context.Context, key string) (func(), error) {
	locks.mu.Lock()
	if locks.entries == nil {
		locks.entries = make(map[string]*lockEntry)
	}
	entry := locks.entries[key]
	if entry == nil {
		entry = &lockEntry{token: make(chan struct{}, 1)}
		locks.entries[key] = entry
	}
	entry.refs++
	locks.mu.Unlock()

	drop := func() {
		locks.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(locks.entries, key)
		}
		locks.mu.Unlock()
	}
	select {
	case entry.token <- struct{}{}:
		return func() {
			<-entry.token
			drop()
		}, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
