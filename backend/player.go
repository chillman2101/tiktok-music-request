package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type PlayerStatus struct {
	IsPlaying   bool   `json:"isPlaying"`
	CurrentSong *Song  `json:"currentSong"`
	ProcessID   int    `json:"processId"`
	PlayerType  string `json:"playerType"`
}

type Player struct {
	mu              sync.Mutex
	status          PlayerStatus
	cmd             *exec.Cmd
	cancelFunc      context.CancelFunc
	queue           *Queue
	hub             *Hub
	streamDir       string
	playerPath      string
	useDirectStream bool
}

func NewPlayer(queue *Queue, hub *Hub) *Player {
	// Determine which player to use
	playerPath := os.Getenv("PLAYER")
	if playerPath == "" {
		// Try to find mpv first, fallback to vlc
		if _, err := exec.LookPath("mpv"); err == nil {
			playerPath = "mpv"
		} else if _, err := exec.LookPath("vlc"); err == nil {
			playerPath = "vlc"
		} else {
			log.Println("⚠️ Neither mpv nor vlc found in PATH! Will use download + default player")
			playerPath = "vlc" // will fail but at least try
		}
	}

	log.Printf("🎯 Using player: %s", playerPath)

	// Create stream directory
	streamDir := "streams"
	if err := os.MkdirAll(streamDir, 0755); err != nil {
		log.Printf("⚠️ Could not create stream dir: %v", err)
	}

	// Check if yt-dlp is available
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		log.Printf("⚠️ yt-dlp not found! Please install: https://github.com/yt-dlp/yt-dlp")
	} else {
		log.Println("✅ yt-dlp found")
	}

	return &Player{
		status: PlayerStatus{
			IsPlaying:  false,
			PlayerType: playerPath,
		},
		queue:           queue,
		hub:             hub,
		streamDir:       streamDir,
		playerPath:      playerPath,
		useDirectStream: true,
	}
}

func (p *Player) Play(song Song) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil && p.status.IsPlaying {
		// Stop current playback first
		p.stopLocked()
	}

	log.Printf("🎵 Playing: %s - %s (videoID: %s)", song.Title, song.Artist, song.VideoID)

	// Start playback in goroutine
	go p.playSong(song)

	p.status.IsPlaying = true
	p.status.CurrentSong = &song

	return nil
}

func (p *Player) playSong(song Song) {
	url := fmt.Sprintf("https://www.youtube.com/watch?v=%s", song.VideoID)

	// Try direct streaming first
	if p.useDirectStream {
		if err := p.playDirectStream(url, song); err != nil {
			log.Printf("⚠️ Direct stream failed, falling back to download: %v", err)
		}
		return
	}

	// Fallback: download then play
	p.playWithDownload(url, song)
}

func (p *Player) playDirectStream(url string, song Song) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancelFunc = cancel
	p.mu.Unlock()

	// yt-dlp -> player pipeline
	ytArgs := []string{
		url,
		"-f", "bestaudio",
		"--no-playlist",
		"--no-cache-dir",
		"-o", "-",
	}

	ytCmd := exec.CommandContext(ctx, "yt-dlp", ytArgs...)

	var playerArgs []string
	if p.playerPath == "mpv" {
		playerArgs = []string{
			"--no-video",
			"--really-quiet",
			"--loop-playlist=no",
			"--no-resume-playback",
			"--",
			"-",
		}
	} else if p.playerPath == "vlc" {
		playerArgs = []string{
			"--no-video",
			"--intf", "dummy",
			"--play-and-exit",
			"--no-loop",
			"-",
		}
	} else {
		return fmt.Errorf("unsupported player: %s", p.playerPath)
	}

	playerCmd := exec.CommandContext(ctx, p.playerPath, playerArgs...)

	p.mu.Lock()
	p.cmd = playerCmd
	p.mu.Unlock()

	// Setup pipes
	ytStdout, err := ytCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("yt-dlp stdout pipe: %w", err)
	}

	playerStdin, err := playerCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("player stdin pipe: %w", err)
	}

	// Start player first
	if err := playerCmd.Start(); err != nil {
		return fmt.Errorf("player start: %w", err)
	}

	log.Printf("✅ Player started (PID: %d)", playerCmd.Process.Pid)

	// Start yt-dlp
	if err := ytCmd.Start(); err != nil {
		playerCmd.Process.Kill()
		return fmt.Errorf("yt-dlp start: %w", err)
	}

	// Pipe yt-dlp output to player
	go func() {
		defer playerStdin.Close()
		buf := make([]byte, 65536)
		for {
			n, readErr := ytStdout.Read(buf)
			if n > 0 {
				if _, werr := playerStdin.Write(buf[:n]); werr != nil {
					break
				}
			}
			if readErr != nil {
				break
			}
		}
	}()

	// Wait for completion
	errCh := make(chan error, 2)
	go func() {
		errCh <- ytCmd.Wait()
	}()
	go func() {
		errCh <- playerCmd.Wait()
	}()

	// Wait for either to finish
	<-errCh
	<-errCh

	p.onSongComplete()
	return nil
}

func (p *Player) playWithDownload(url string, song Song) {
	log.Printf("📥 Downloading audio...")

	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancelFunc = cancel
	p.mu.Unlock()

	// Download audio
	outputPath := filepath.Join(p.streamDir, fmt.Sprintf("%s.mp3", song.VideoID))

	ytCmd := exec.CommandContext(ctx, "yt-dlp",
		url,
		"-f", "bestaudio",
		"--extract-audio",
		"--audio-format", "mp3",
		"--audio-quality", "0",
		"--no-playlist",
		"-o", outputPath,
	)

	// Show download progress
	ytCmd.Stderr = os.Stderr

	if err := ytCmd.Run(); err != nil {
		log.Printf("❌ Download failed: %v", err)
		p.onSongComplete()
		return
	}

	log.Printf("✅ Download complete: %s", filepath.Base(outputPath))

	// Play with player
	var playerArgs []string
	if p.playerPath == "mpv" {
		playerArgs = []string{
			"--no-video",
			"--really-quiet",
			"--loop-playlist=no",
			"--no-resume-playback",
			outputPath,
		}
	} else if p.playerPath == "vlc" {
		playerArgs = []string{
			"--no-video",
			"--play-and-exit",
			"--no-loop",
			outputPath,
		}
	} else {
		playerArgs = []string{outputPath}
	}

	playerCmd := exec.CommandContext(ctx, p.playerPath, playerArgs...)

	p.mu.Lock()
	p.cmd = playerCmd
	p.mu.Unlock()

	playerCmd.Stdout = os.Stdout
	playerCmd.Stderr = os.Stderr

	if err := playerCmd.Run(); err != nil {
		log.Printf("❌ Player error: %v", err)
	}

	// Cleanup
	os.Remove(outputPath)
	p.onSongComplete()
}

func (p *Player) onSongComplete() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.status.IsPlaying = false
	p.status.CurrentSong = nil
	p.cmd = nil

	log.Println("⏭️ Song complete, advancing queue...")

	// Advance queue
	p.queue.Skip()
	p.hub.Broadcast(p.queue)

	// Play next song if available
	next := p.queue.Peek()
	if next != nil {
		go p.Play(*next)
	}
}

func (p *Player) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopLocked()
}

func (p *Player) stopLocked() error {
	if p.cmd != nil && p.cmd.Process != nil {
		log.Printf("🛑 Stopping player (PID: %d)", p.cmd.Process.Pid)
		if err := p.cmd.Process.Kill(); err != nil {
			log.Printf("⚠️ Error killing process: %v", err)
			return err
		}
		p.cmd = nil
	}

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

// Handle advance request from overlay
func (p *Player) HandleAdvance(w http.ResponseWriter, r *http.Request) {
	log.Println("⏭️ Advance requested via API")
	p.onSongComplete()
	w.WriteHeader(http.StatusNoContent)
}

// Handle player controls
func (p *Player) HandlePlayerControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Action  string `json:"action"`
		VideoID string `json:"videoId"`
		Title   string `json:"title"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "play":
		if req.VideoID != "" {
			song := Song{
				VideoID:     req.VideoID,
				Title:       req.Title,
				Artist:      "Streaming",
				RequestedBy: "system",
			}
			go p.Play(song)
			json.NewEncoder(w).Encode(map[string]string{"status": "playing", "videoId": req.VideoID})
		} else {
			http.Error(w, "missing videoId", http.StatusBadRequest)
		}

	case "stop":
		p.Stop()
		json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})

	case "skip":
		p.Stop()
		p.queue.Skip()
		p.hub.Broadcast(p.queue)
		next := p.queue.Peek()
		if next != nil {
			go p.Play(*next)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "skipped"})

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}
