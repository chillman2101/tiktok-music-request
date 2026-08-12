package main

import "sync"

// SeenSet is a small thread-safe bounded set used to dedup incoming
// comments by TikTok's own message ID. Centralizing this in the backend
// (rather than in tiktok-connector's in-memory Set) matters because
// platforms like Railway briefly run the old and new container together
// during a rolling deploy — for a few seconds, two separate connector
// processes are both connected to the same TikTok room and both forward
// the same live comments. Each connector has its own independent dedup
// Set, so it can't catch a duplicate from the other instance. The backend
// is a single instance receiving from both, so it can.
type SeenSet struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
	max   int
}

func NewSeenSet(max int) *SeenSet {
	return &SeenSet{seen: make(map[string]struct{}), max: max}
}

// CheckAndAdd returns true if id was already seen (caller should skip
// reprocessing it), and records it if not. An empty id is never
// considered a duplicate (some callers may not have a real message ID).
func (s *SeenSet) CheckAndAdd(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.seen[id]; ok {
		return true
	}

	s.seen[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > s.max {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.seen, oldest)
	}
	return false
}
