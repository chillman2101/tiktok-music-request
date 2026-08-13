package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	// Atau kalo mau lebih fleksibel, coba beberapa path
	cookiePaths := []string{
		"/app/backend/cookies.txt", // Railway (Docker)
		"./backend/cookies.txt",    // Local run
		"./cookies.txt",            // Fallback
	}

	var cookieFilePath string
	for _, path := range cookiePaths {
		if _, err := os.Stat(path); err == nil {
			cookieFilePath = path
			log.Printf("✅ Cookies found at %s", path)
			break
		}
	}

	if cookieFilePath == "" {
		log.Printf("⚠️ No cookie file found. Trying without cookies.")
	}

	// Di local untuk testing, sesuaikan path-nya
	// cookieFilePath := "./cookies.txt"

	// Cek apakah file cookies ada
	if _, err := os.Stat(cookieFilePath); os.IsNotExist(err) {
		log.Printf("⚠️ Cookie file not found at %s. Trying without cookies.", cookieFilePath)
		cookieFilePath = "" // Set kosong agar tidak dipakai
	} else {
		log.Printf("✅ Cookies found at %s", cookieFilePath)
	}

	url := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)

	// ... (context setup, defer cancel) ...

	// Bangun perintah yt-dlp dengan cookies
	ytDlpPath := "/usr/local/bin/yt-dlp"
	if _, err := os.Stat(ytDlpPath); os.IsNotExist(err) {
		ytDlpPath = "yt-dlp"
	}

	// Argumen dasar
	args := []string{
		url,
		"-f", "bestaudio",
		"--no-playlist",
		"--no-cache-dir",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"-o", "-",
	}

	// Tambahkan argumen cookies jika file tersedia
	if cookieFilePath != "" {
		// Sisipkan argumen cookies sebelum URL atau di awal
		args = append([]string{"--cookies", cookieFilePath}, args...)
	}

	log.Printf("🚀 Executing yt-dlp with cookies: %v", args)

	cmd := exec.CommandContext(ctx, ytDlpPath, args...)

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

	if err := cmd.Start(); err != nil {
		log.Printf("❌ Error starting yt-dlp: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Log stderr
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

	_, err = io.Copy(w, stdout)
	if err != nil {
		log.Printf("⚠️ Stream ended: %v", err)
	}

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
