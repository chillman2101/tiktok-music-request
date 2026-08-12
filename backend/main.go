package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
)

//go:embed overlay
var overlayFS embed.FS

type CommentPayload struct {
	Username string `json:"username"`
	Comment  string `json:"comment"`
}

func authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	if r.URL.Query().Get("key") == token {
		return true
	}
	auth := r.Header.Get("Authorization")
	return strings.TrimPrefix(auth, "Bearer ") == token && auth != ""
}

func main() {
	queue := NewQueue()
	hub := NewHub()
	player := NewPlayer(queue, hub)

	broadcaster := os.Getenv("BROADCASTER_USERNAME")
	if broadcaster == "" {
		broadcaster = "broadcaster"
	}

	backendSecret := os.Getenv("BACKEND_SHARED_SECRET")
	if backendSecret == "" {
		log.Println("WARNING: BACKEND_SHARED_SECRET not set — /api/comment accepts any request.")
	}

	overlayToken := os.Getenv("OVERLAY_TOKEN")
	if overlayToken == "" {
		log.Println("WARNING: OVERLAY_TOKEN not set — the overlay is publicly viewable.")
	}

	mux := http.NewServeMux()

	// POST /api/comment - receive chat commands
	mux.HandleFunc("/api/comment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authorized(r, backendSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var payload CommentPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}

		cmd := ParseCommand(payload.Comment)
		switch cmd.Name {
		case "play":
			result, err := ResolveSong(cmd.Arg)
			if err != nil {
				log.Println("resolve song failed:", err)
				http.Error(w, "could not resolve song", http.StatusBadGateway)
				return
			}

			song := queue.Add(result.Title, result.Artist, result.VideoID, payload.Username)
			log.Printf("📝 Added to queue: %s - %s (queue length: %d)", song.Title, song.Artist, queue.Len())

			hub.Broadcast(queue)

			// If nothing is playing, set as current
			if !player.GetStatus().IsPlaying {
				log.Println("🎯 No song playing, setting as current...")
				player.Play(song)
			} else {
				log.Println("⏳ Song already playing, added to queue")
			}

			json.NewEncoder(w).Encode(song)
			return

		case "skip":
			if payload.Username != broadcaster {
				http.Error(w, "only the broadcaster can skip", http.StatusForbidden)
				return
			}
			player.Stop()
			queue.Skip()
			hub.Broadcast(queue)

			next := queue.Peek()
			if next != nil {
				player.Play(*next)
			}
			w.WriteHeader(http.StatusNoContent)
			return

		case "clearqueue":
			if payload.Username != broadcaster {
				http.Error(w, "only the broadcaster can clear the queue", http.StatusForbidden)
				return
			}
			player.Stop()
			queue.Clear()
			hub.Broadcast(queue)
			w.WriteHeader(http.StatusNoContent)
			return

		default:
			w.WriteHeader(http.StatusNoContent)
			return
		}
	})

	// POST /api/advance - called by overlay when song ends
	mux.HandleFunc("/api/advance", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		player.HandleAdvance(w, r)
	})

	// GET /api/queue - snapshot
	mux.HandleFunc("/api/queue", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"queue": queue.Snapshot()})
	})

	// GET /api/status - player status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		status := player.GetStatus()
		json.NewEncoder(w).Encode(status)
	})

	// GET /api/stream - audio stream (works locally and on Railway!)
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		player.ServeAudioStream(w, r)
	})

	// POST /api/player - control player
	mux.HandleFunc("/api/player", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		player.HandlePlayerControl(w, r)
	})

	// GET /ws - WebSocket for overlay
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hub.ServeWS(w, r, queue)
	})

	// Serve overlay static files
	overlayRoot, err := fs.Sub(overlayFS, "overlay")
	if err != nil {
		log.Fatal(err)
	}
	overlayFileServer := http.StripPrefix("/overlay/", http.FileServer(http.FS(overlayRoot)))
	mux.Handle("/overlay/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized — append ?key=<OVERLAY_TOKEN> to the URL", http.StatusUnauthorized)
			return
		}
		overlayFileServer.ServeHTTP(w, r)
	}))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := ":" + port
	log.Println("🚀 Song-request backend listening on", addr)

	if os.Getenv("RAILWAY_ENVIRONMENT") != "" {
		log.Println("📱 Running on Railway!")
		log.Println("📱 Overlay URL: https://your-app.railway.app/overlay/?key=<token>")
	} else {
		log.Println("📱 Running locally!")
		log.Println("📱 Overlay URL: http://localhost:8080/overlay/?key=<token>")
	}

	log.Fatal(http.ListenAndServe(addr, mux))
}
