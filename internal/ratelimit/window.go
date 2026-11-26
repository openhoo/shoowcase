// Package ratelimit bounds short-lived per-process request counters.
package ratelimit

import (
	"sync"
	"time"
)

type Window struct {
	mu     sync.Mutex
	counts map[string]int
	window int64
}

func (l *Window) Allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := time.Now().Unix() / 60
	if w != l.window || l.counts == nil {
		l.counts = make(map[string]int)
		l.window = w
	}
	if _, ok := l.counts[key]; !ok && len(l.counts) >= 65536 {
		return false
	}
	if l.counts[key] >= max {
		return false
	}
	l.counts[key]++
	return true
}
