package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"sync"
)

type PlayerStatus struct {
	IsPlaying   bool   `json:"isPlaying"`
	CurrentSong *Song  `json:"currentSong"`
	PlayerType  string `json:"playerType"`
}

type Player struct {
	mu         sync.Mutex
	status     PlayerStatus
	queue      *Queue
	hub        *Hub
	cancelFunc context.CancelFunc
}

func NewPlayer(queue *Queue, hub *Hub) *Player {
	// Check if yt-dlp is available
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		log.Printf("⚠️ yt-dlp not found! Please install: https://github.com/yt-dlp/yt-dlp")
	} else {
		log.Println("✅ yt-dlp found")
	}

	return &Player{
		status: PlayerStatus{
			IsPlaying:  false,
			PlayerType: "browser-stream",
		},
		queue: queue,
		hub:   hub,
	}
}

func (p *Player) Play(song Song) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	log.Printf("🎵 Playing: %s - %s (videoID: %s)", song.Title, song.Artist, song.VideoID)

	p.status.IsPlaying = true
	p.status.CurrentSong = &song

	return nil
}

func (p *Player) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cancelFunc != nil {
		p.cancelFunc()
	}

	p.status.IsPlaying = false
	p.status.CurrentSong = nil

	return nil
}

func (p *Player) GetStatus() PlayerStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Handle advance request from overlay (called when song ends in browser)
func (p *Player) HandleAdvance(w http.ResponseWriter, r *http.Request) {
	log.Println("⏭️ Advance requested via API")

	p.mu.Lock()
	p.status.IsPlaying = false
	p.status.CurrentSong = nil
	p.mu.Unlock()

	// Advance queue
	p.queue.Skip()
	p.hub.Broadcast(p.queue)

	// Play next song if available
	next := p.queue.Peek()
	if next != nil {
		p.mu.Lock()
		p.status.IsPlaying = true
		p.status.CurrentSong = next
		p.mu.Unlock()
	}

	w.WriteHeader(http.StatusNoContent)
}

// ServeAudioStream streams audio directly to browser (works everywhere!)
func (p *Player) ServeAudioStream(w http.ResponseWriter, r *http.Request) {
	videoID := r.URL.Query().Get("videoId")
	if videoID == "" {
		http.Error(w, "missing videoId", http.StatusBadRequest)
		return
	}

	log.Printf("🎵 Streaming audio for videoID: %s", videoID)

	url := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)

	// Create context with cancellation
	ctx, cancel := context.WithCancel(r.Context())
	p.mu.Lock()
	p.cancelFunc = cancel
	p.mu.Unlock()

	// Cleanup on finish
	defer func() {
		p.mu.Lock()
		p.cancelFunc = nil
		p.mu.Unlock()
	}()

	// Use yt-dlp to stream audio directly to browser
	cmd := exec.CommandContext(ctx, "yt-dlp",
		url,
		"-f", "bestaudio",
		"--no-playlist",
		"--no-cache-dir",
		"-o", "-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("❌ Error getting stdout: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("❌ Error getting stderr: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Start yt-dlp
	if err := cmd.Start(); err != nil {
		log.Printf("❌ Error starting yt-dlp: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set headers for streaming
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Log stderr in background (for debugging)
	go func() {
		errBuf := make([]byte, 1024)
		for {
			n, err := stderr.Read(errBuf)
			if n > 0 {
				log.Printf("yt-dlp: %s", string(errBuf[:n]))
			}
			if err != nil {
				break
			}
		}
	}()

	// Stream to response
	_, err = io.Copy(w, stdout)
	if err != nil {
		log.Printf("⚠️ Stream ended: %v", err)
	}

	// Wait for command to finish
	if err := cmd.Wait(); err != nil {
		log.Printf("⚠️ yt-dlp finished with error: %v", err)
	}

	log.Printf("✅ Stream finished for videoID: %s", videoID)
}

// Handle player controls
func (p *Player) HandlePlayerControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Action string `json:"action"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "stop":
		p.Stop()
		p.queue.Clear()
		p.hub.Broadcast(p.queue)
		json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})

	case "skip":
		p.Stop()
		p.queue.Skip()
		p.hub.Broadcast(p.queue)
		next := p.queue.Peek()
		if next != nil {
			p.mu.Lock()
			p.status.IsPlaying = true
			p.status.CurrentSong = next
			p.mu.Unlock()
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "skipped"})

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}
