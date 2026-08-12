package main

import (
	"strconv"
	"sync"
)

// PendingSong is a song request waiting for broadcaster approval before it
// joins the real playback queue. Only used when Config.AutoApprove is false.
type PendingSong struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	VideoID     string `json:"videoId"`
	RequestedBy string `json:"requestedBy"`
}

// PendingQueue is a simple thread-safe holding area, separate from Queue so
// the overlay (which reads Queue) never shows unapproved requests.
type PendingQueue struct {
	mu    sync.Mutex
	items []PendingSong
	seq   int
}

func NewPendingQueue() *PendingQueue {
	return &PendingQueue{items: []PendingSong{}}
}

func (p *PendingQueue) Add(title, artist, videoID, requestedBy string) PendingSong {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	s := PendingSong{
		ID:          "p" + strconv.Itoa(p.seq),
		Title:       title,
		Artist:      artist,
		VideoID:     videoID,
		RequestedBy: requestedBy,
	}
	p.items = append(p.items, s)
	return s
}

// Take removes and returns the pending song with the given ID, if present.
func (p *PendingQueue) Take(id string) (PendingSong, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.items {
		if s.ID == id {
			p.items = append(p.items[:i], p.items[i+1:]...)
			return s, true
		}
	}
	return PendingSong{}, false
}

func (p *PendingQueue) Snapshot() []PendingSong {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PendingSong, len(p.items))
	copy(out, p.items)
	return out
}
