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

// authorized checks a shared token sent either as "Authorization: Bearer <token>"
// or as a "?key=<token>" query param (browsers can't set custom headers on
// page navigation or a WebSocket handshake, so the overlay relies on the
// query param). An empty configured token means the check is disabled —
// this keeps local dev (`go run .` with no env vars) working without setup.
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

	// The username allowed to run broadcaster-only commands (!skip, !clearqueue).
	// In the real setup this would be your own TikTok username.
	broadcaster := os.Getenv("BROADCASTER_USERNAME")
	if broadcaster == "" {
		broadcaster = "broadcaster" // default for local testing
	}

	// Shared secret the tiktok-connector must send so /api/comment can't be
	// hit by an arbitrary POST once the backend is publicly reachable. Set
	// via env var on both the backend and tiktok-connector; empty disables
	// the check (local dev / curl testing without setup).
	backendSecret := os.Getenv("BACKEND_SHARED_SECRET")
	if backendSecret == "" {
		log.Println("WARNING: BACKEND_SHARED_SECRET not set — /api/comment accepts any request. Set it before exposing this server publicly.")
	}

	// Token gating the overlay page, its WebSocket feed, and the queue/advance
	// endpoints, so the overlay URL isn't fully public. Paste it into OBS as
	// http://host/overlay/?key=<token>. Empty disables the check.
	overlayToken := os.Getenv("OVERLAY_TOKEN")
	if overlayToken == "" {
		log.Println("WARNING: OVERLAY_TOKEN not set — the overlay is publicly viewable to anyone with the URL.")
	}

	mux := http.NewServeMux()

	// POST /api/comment — this is where real chat events land.
	// For now you POST to this yourself to simulate a TikTok comment.
	// Later, the TikTok connector (Node sidecar) calls this same endpoint
	// for every real chat message it receives.
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
			hub.Broadcast(queue)
			json.NewEncoder(w).Encode(song)
			return

		case "skip":
			if payload.Username != broadcaster {
				http.Error(w, "only the broadcaster can skip", http.StatusForbidden)
				return
			}
			queue.Skip()
			hub.Broadcast(queue)
			w.WriteHeader(http.StatusNoContent)
			return

		case "clearqueue":
			if payload.Username != broadcaster {
				http.Error(w, "only the broadcaster can clear the queue", http.StatusForbidden)
				return
			}
			queue.Clear()
			hub.Broadcast(queue)
			w.WriteHeader(http.StatusNoContent)
			return

		default:
			// Not a recognized command — ignore it (this is normal, most
			// chat messages won't be song requests).
			w.WriteHeader(http.StatusNoContent)
			return
		}
	})

	// POST /api/advance — called by the overlay itself when the YouTube
	// player reports the current video ended, so the queue moves on
	// automatically. No broadcaster check: this is a system event, not a
	// viewer command.
	mux.HandleFunc("/api/advance", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		queue.Skip()
		hub.Broadcast(queue)
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/queue — plain JSON snapshot, handy for debugging with curl.
	mux.HandleFunc("/api/queue", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"queue": queue.Snapshot()})
	})

	// GET /ws — the overlay connects here to receive live updates.
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, overlayToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		hub.ServeWS(w, r, queue)
	})

	// Serve the overlay's static files at /overlay/ — this is the URL
	// you paste into OBS as a Browser Source. Embedded into the binary
	// (rather than served from a relative "../overlay" path) so it works
	// regardless of the working directory or deployment root — e.g. when
	// only the backend/ subfolder is uploaded as the build context.
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

	addr := ":8080"
	log.Println("song-request backend listening on", addr)
	log.Println("overlay URL for OBS: http://localhost:8080/overlay/")
	log.Fatal(http.ListenAndServe(addr, mux))
}
