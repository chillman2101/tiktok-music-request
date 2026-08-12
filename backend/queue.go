package main

import (
	"strconv"
	"sync"
)

// Song represents one song request in the queue.
type Song struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	VideoID     string `json:"videoId"`
	RequestedBy string `json:"requestedBy"`
}

// Queue is a simple thread-safe FIFO queue.
// In production you'd back this with Redis (list + pub/sub) so state
// survives restarts and multiple backend instances can share it.
// For local testing, in-memory is enough.
type Queue struct {
	mu    sync.Mutex
	items []Song
	seq   int
}

func NewQueue() *Queue {
	return &Queue{items: []Song{}}
}

// Add appends a song to the end of the queue and returns it (with an assigned ID).
func (q *Queue) Add(title, artist, videoID, requestedBy string) Song {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	s := Song{
		ID:          strconv.Itoa(q.seq),
		Title:       title,
		Artist:      artist,
		VideoID:     videoID,
		RequestedBy: requestedBy,
	}
	q.items = append(q.items, s)
	return s
}

// Skip removes the currently playing (first) song and returns the new state.
func (q *Queue) Skip() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) > 0 {
		q.items = q.items[1:]
	}
}

// Clear empties the whole queue.
func (q *Queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = []Song{}
}

// Remove deletes the song with the given ID from anywhere in the queue
// (not just the front, unlike Skip) and reports whether it was found.
func (q *Queue) Remove(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, s := range q.items {
		if s.ID == id {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return true
		}
	}
	return false
}

// ReorderUpcoming reorders every song after the currently playing one
// (items[0]) to match the given ID order — used for drag-to-reorder in
// the admin CMS, which never lets you drag the currently playing song.
// ids must contain exactly the same set of IDs already in items[1:], in
// any order; otherwise the queue is left unchanged and false is returned.
func (q *Queue) ReorderUpcoming(ids []string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return len(ids) == 0
	}

	upcoming := q.items[1:]
	if len(ids) != len(upcoming) {
		return false
	}

	byID := make(map[string]Song, len(upcoming))
	for _, s := range upcoming {
		byID[s.ID] = s
	}

	reordered := make([]Song, 0, len(ids))
	for _, id := range ids {
		s, ok := byID[id]
		if !ok {
			return false
		}
		reordered = append(reordered, s)
	}

	q.items = append(q.items[:1], reordered...)
	return true
}

// Snapshot returns a copy of the current queue state.
func (q *Queue) Snapshot() []Song {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Song, len(q.items))
	copy(out, q.items)
	return out
}

// Len returns the number of songs in the queue.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Peek returns the first song without removing it.
func (q *Queue) Peek() *Song {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	return &q.items[0]
}
