package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// Allow connections from anywhere — this only runs locally / inside OBS,
	// there's no real cross-origin risk here.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Hub keeps track of connected overlay clients and broadcasts queue updates.
type Hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]bool
}

func NewHub() *Hub {
	return &Hub{clients: make(map[*websocket.Conn]bool)}
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, q *Queue) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("ws upgrade error:", err)
		return
	}

	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()

	// Send current state immediately on connect so the overlay
	// doesn't start empty if it (re)connects mid-stream.
	h.sendTo(conn, q.Snapshot())

	// We don't expect messages from the overlay, but we need to keep
	// reading so we notice when the connection closes.
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.clients, conn)
			h.mu.Unlock()
			conn.Close()
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}

func (h *Hub) sendTo(conn *websocket.Conn, songs []Song) {
	payload, err := json.Marshal(map[string]any{"queue": songs})
	if err != nil {
		log.Println("marshal error:", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		log.Println("write error:", err)
	}
}

// Broadcast pushes the current queue snapshot to every connected overlay.
func (h *Hub) Broadcast(q *Queue) {
	songs := q.Snapshot()
	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.clients {
		h.sendTo(conn, songs)
	}
}
