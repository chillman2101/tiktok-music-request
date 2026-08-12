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

//go:embed admin
var adminFS embed.FS

type CommentPayload struct {
	Username string `json:"username"`
	Comment  string `json:"comment"`
	// MsgID is TikTok's own per-message identifier, forwarded by
	// tiktok-connector. Optional — manual curl testing can omit it and
	// every request is treated as unique. See dedup.go for why this
	// check lives here rather than in the connector.
	MsgID string `json:"msgId"`
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

// authorizedAny checks against multiple acceptable tokens — used for
// /api/config, which both the admin CMS (adminToken) and the
// tiktok-connector (backendSecret, to poll for username changes) need
// to read. Unlike authorized(), an empty individual token is never
// treated as "open" here — only if every configured token is empty is
// the check skipped (consistent with authorized()'s local-dev behavior).
func authorizedAny(r *http.Request, tokens ...string) bool {
	allEmpty := true
	for _, t := range tokens {
		if t == "" {
			continue
		}
		allEmpty = false
		if r.URL.Query().Get("key") == t {
			return true
		}
		auth := r.Header.Get("Authorization")
		if auth != "" && strings.TrimPrefix(auth, "Bearer ") == t {
			return true
		}
	}
	return allEmpty
}

func main() {
	queue := NewQueue()
	pending := NewPendingQueue()
	hub := NewHub()
	player := NewPlayer(queue, hub)
	seenComments := NewSeenSet(1000)

	defaultBroadcaster := os.Getenv("BROADCASTER_USERNAME")
	if defaultBroadcaster == "" {
		defaultBroadcaster = "broadcaster"
	}
	cfg := NewConfig("config.json", defaultBroadcaster, os.Getenv("TIKTOK_USERNAME"))

	backendSecret := os.Getenv("BACKEND_SHARED_SECRET")
	if backendSecret == "" {
		log.Println("WARNING: BACKEND_SHARED_SECRET not set — /api/comment accepts any request.")
	}

	overlayToken := os.Getenv("OVERLAY_TOKEN")
	if overlayToken == "" {
		log.Println("WARNING: OVERLAY_TOKEN not set — the overlay is publicly viewable.")
	}

	adminToken := os.Getenv("ADMIN_TOKEN")
	if adminToken == "" {
		log.Println("WARNING: ADMIN_TOKEN not set — the admin CMS is publicly editable by anyone.")
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

		// Dedup by TikTok's own message ID, centralized here rather than
		// in tiktok-connector — see dedup.go for why (rolling-deploy
		// overlap can mean two connector instances briefly forward the
		// same live comment).
		if seenComments.CheckAndAdd(payload.MsgID) {
			log.Printf("⏭️ Duplicate comment ignored (msgId=%s)", payload.MsgID)
			w.WriteHeader(http.StatusNoContent)
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

			if !cfg.Snapshot().AutoApprove {
				ps := pending.Add(result.Title, result.Artist, result.VideoID, payload.Username)
				log.Printf("⏸ Pending approval: %s - %s (requested by @%s)", ps.Title, ps.Artist, ps.RequestedBy)
				json.NewEncoder(w).Encode(map[string]any{"status": "pending", "song": ps})
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
			if payload.Username != cfg.Snapshot().BroadcasterUsername {
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
			if payload.Username != cfg.Snapshot().BroadcasterUsername {
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

	// GET /api/admin/queue - current + upcoming songs, for the admin CMS
	// (separate from /api/queue so the overlay's read doesn't need
	// adminToken and the admin panel's doesn't need overlayToken).
	mux.HandleFunc("/api/admin/queue", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"queue":  queue.Snapshot(),
			"status": player.GetStatus(),
		})
	})

	// POST /api/admin/skip - skip the currently playing song
	mux.HandleFunc("/api/admin/skip", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	})

	// POST /api/admin/clear - empty the whole queue and stop playback
	mux.HandleFunc("/api/admin/clear", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		player.Stop()
		queue.Clear()
		hub.Broadcast(queue)
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/admin/add - manually add a song to the queue from the
	// admin CMS, bypassing chat entirely (and bypassing auto-approve,
	// since the broadcaster adding it directly IS the approval).
	mux.HandleFunc("/api/admin/add", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Query) == "" {
			http.Error(w, "expected non-empty \"query\"", http.StatusBadRequest)
			return
		}

		result, err := ResolveSong(body.Query)
		if err != nil {
			log.Println("resolve song failed:", err)
			http.Error(w, "could not resolve song", http.StatusBadGateway)
			return
		}

		song := queue.Add(result.Title, result.Artist, result.VideoID, "admin")
		hub.Broadcast(queue)
		if !player.GetStatus().IsPlaying {
			player.Play(song)
		}
		log.Printf("➕ Admin added: %s - %s", song.Title, song.Artist)
		json.NewEncoder(w).Encode(song)
	})

	// POST /api/admin/queue/{id}/remove - remove one song from anywhere in
	// the queue. If it was the currently playing song, advances to next.
	mux.HandleFunc("/api/admin/queue/", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/admin/queue/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 || parts[1] != "remove" {
			http.Error(w, "expected /api/admin/queue/{id}/remove", http.StatusBadRequest)
			return
		}
		id := parts[0]

		wasCurrent := player.GetStatus().CurrentSong != nil && player.GetStatus().CurrentSong.ID == id
		if !queue.Remove(id) {
			http.Error(w, "song not found in queue", http.StatusNotFound)
			return
		}
		hub.Broadcast(queue)

		if wasCurrent {
			player.Stop()
			next := queue.Peek()
			if next != nil {
				player.Play(*next)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/admin/queue/reorder - drag-to-reorder the upcoming songs
	// (everything after the one currently playing) in the admin CMS.
	mux.HandleFunc("/api/admin/queue/reorder", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		if !queue.ReorderUpcoming(body.IDs) {
			http.Error(w, "ids must match the current upcoming queue exactly", http.StatusBadRequest)
			return
		}
		hub.Broadcast(queue)
		w.WriteHeader(http.StatusNoContent)
	})

	// GET/POST /api/config - read/update broadcaster username, tiktok
	// username, and auto-approve, from the admin CMS. GET is also used
	// by the tiktok-connector sidecar to poll for a changed username.
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if !authorizedAny(r, adminToken, backendSecret) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(cfg.Snapshot())

		case http.MethodPost:
			if !authorized(r, adminToken) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var body struct {
				BroadcasterUsername *string `json:"broadcasterUsername"`
				TikTokUsername      *string `json:"tiktokUsername"`
				AutoApprove         *bool   `json:"autoApprove"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid json body", http.StatusBadRequest)
				return
			}
			updated := cfg.Update(body.BroadcasterUsername, body.TikTokUsername, body.AutoApprove)
			log.Printf("⚙️ Config updated: broadcaster=%s tiktok=%s autoApprove=%v",
				updated.BroadcasterUsername, updated.TikTokUsername, updated.AutoApprove)
			json.NewEncoder(w).Encode(updated)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// GET /api/pending - list requests awaiting approval (admin CMS only)
	mux.HandleFunc("/api/pending", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"pending": pending.Snapshot()})
	})

	// POST /api/pending/{id}/approve and /api/pending/{id}/reject
	mux.HandleFunc("/api/pending/", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/pending/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			http.Error(w, "expected /api/pending/{id}/{approve|reject}", http.StatusBadRequest)
			return
		}
		id, action := parts[0], parts[1]

		ps, ok := pending.Take(id)
		if !ok {
			http.Error(w, "pending request not found", http.StatusNotFound)
			return
		}

		switch action {
		case "approve":
			song := queue.Add(ps.Title, ps.Artist, ps.VideoID, ps.RequestedBy)
			hub.Broadcast(queue)
			if !player.GetStatus().IsPlaying {
				player.Play(song)
			}
			log.Printf("✅ Approved: %s - %s", song.Title, song.Artist)
			json.NewEncoder(w).Encode(song)

		case "reject":
			log.Printf("❌ Rejected: %s - %s", ps.Title, ps.Artist)
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "unknown action, expected approve or reject", http.StatusBadRequest)
		}
	})

	// Serve admin CMS static files
	adminRoot, err := fs.Sub(adminFS, "admin")
	if err != nil {
		log.Fatal(err)
	}
	adminFileServer := http.StripPrefix("/admin/", http.FileServer(http.FS(adminRoot)))
	mux.Handle("/admin/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, adminToken) {
			http.Error(w, "unauthorized — append ?key=<ADMIN_TOKEN> to the URL", http.StatusUnauthorized)
			return
		}
		adminFileServer.ServeHTTP(w, r)
	}))

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
