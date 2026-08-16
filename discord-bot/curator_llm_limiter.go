package main

import (
	"sync"
	"time"
)

// curatorLLMLimiter is the SHARED per-user/bot-wide cooldown gate on
// actual LLM provider calls -- CGPT-051-A: the original implementation
// only rate-limited the natural-chat MessageCreate path; /curator went
// straight to the provider pool with no equivalent limit, so a player
// could exhaust a free provider's daily/request quota by spamming
// /curator alone. This type is the single choke point both entry points
// (curator_chat.go's askCurator and curator_natural_trigger.go) share,
// so CURATOR-LLM-RATE-2 ("share the same bot-wide LLM request ceiling")
// holds regardless of which surface a message came through.
//
// Deliberately separate from curatorNaturalTrigger's own cooldown, which
// paces NATURAL-CHAT REPLIES in general (including canned ones, for UX
// reasons -- "don't feel like a keyword-spam bot") and is unrelated to
// LLM cost. This limiter only gates the LLM call itself, applied AFTER
// canned/deterministic routing has already decided an LLM call is
// actually needed (CURATOR-LLM-RATE-3 -- canned replies never consume
// LLM budget).
type curatorLLMLimiter struct {
	mu             sync.Mutex
	userCooldown   time.Duration
	globalCooldown time.Duration
	lastGlobal     time.Time
	lastUser       map[string]time.Time
}

func newCuratorLLMLimiter(userCooldown, globalCooldown time.Duration) *curatorLLMLimiter {
	return &curatorLLMLimiter{
		userCooldown:   userCooldown,
		globalCooldown: globalCooldown,
		lastUser:       make(map[string]time.Time),
	}
}

// allow reports whether an LLM call is currently permitted for userID,
// and if so atomically records it (a caller that gets true has, by
// definition, just consumed the budget -- a second concurrent caller
// checking immediately after must see the updated state).
func (l *curatorLLMLimiter) allow(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGlobal) < l.globalCooldown {
		return false
	}
	if last, ok := l.lastUser[userID]; ok && now.Sub(last) < l.userCooldown {
		return false
	}
	l.lastGlobal = now
	l.lastUser[userID] = now
	return true
}
