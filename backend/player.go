package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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

func (p *Player) ServeAudioStream(w http.ResponseWriter, r *http.Request) {
	videoID := r.URL.Query().Get("videoId")
	if videoID == "" {
		http.Error(w, "missing videoId", http.StatusBadRequest)
		return
	}

	log.Printf("🎵 Streaming audio for videoID: %s", videoID)

	ytDlpPath := "/usr/local/bin/yt-dlp"
	if _, err := os.Stat(ytDlpPath); os.IsNotExist(err) {
		ytDlpPath = "yt-dlp"
	}

	url := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)

	ctx, cancel := context.WithCancel(r.Context())
	p.mu.Lock()
	p.cancelFunc = cancel
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.cancelFunc = nil
		p.mu.Unlock()
	}()

	// === FIX: Extractor args dengan visitor_data ===
	// Ganti VISITOR_DATA_1_xxx dengan data dari browser Anda
	cmd := exec.CommandContext(ctx, ytDlpPath,
		url,
		"-f", "bestaudio[ext=m4a]/bestaudio[ext=mp4]/bestaudio",
		"--no-playlist",
		"--no-cache-dir",
		"--extractor-args", "youtube:player_client=android,web;player_skip=webpage,configs;visitor_data=VISITOR_DATA_1_111111111111111111111",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"-o", "-",
	)

	// ... (sisa kode sama seperti sebelumnya)
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
