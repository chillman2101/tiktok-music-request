package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

type CommentPayload struct {
	Username string `json:"username"`
	Comment  string `json:"comment"`
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
		queue.Skip()
		hub.Broadcast(queue)
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/queue — plain JSON snapshot, handy for debugging with curl.
	mux.HandleFunc("/api/queue", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"queue": queue.Snapshot()})
	})

	// GET /ws — the overlay connects here to receive live updates.
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r, queue)
	})

	// Serve the overlay's static files at /overlay/ — this is the URL
	// you paste into OBS as a Browser Source.
	mux.Handle("/overlay/", http.StripPrefix("/overlay/", http.FileServer(http.Dir("../overlay"))))

	addr := ":8080"
	log.Println("song-request backend listening on", addr)
	log.Println("overlay URL for OBS: http://localhost:8080/overlay/")
	log.Fatal(http.ListenAndServe(addr, mux))
}
