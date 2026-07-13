package server

import (
	"encoding/json"
	"net/http"
	"sync"
)

// sseMsg is one named event with a JSON payload.
type sseMsg struct {
	event string
	data  []byte
}

// Hub fans engine events out to all connected Server-Sent-Events clients. Events
// are one-way (server → client), which is all the UI needs (queue/progress/import),
// so SSE avoids pulling in a WebSocket dependency.
type Hub struct {
	mu      sync.Mutex
	clients map[chan sseMsg]struct{}
}

func NewHub() *Hub { return &Hub{clients: map[chan sseMsg]struct{}{}} }

// Broadcast marshals data and delivers it to every client. It is the func passed
// to core.New as emit. Slow/blocked clients drop the message rather than stall.
func (h *Hub) Broadcast(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := sseMsg{event: event, data: b}
	h.mu.Lock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default: // client is behind; skip this event for it
		}
	}
	h.mu.Unlock()
}

func (h *Hub) add() chan sseMsg {
	ch := make(chan sseMsg, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) remove(ch chan sseMsg) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// ServeHTTP streams events to one client until it disconnects.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := h.add()
	defer h.remove(ch)
	flusher.Flush() // open the stream immediately

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			_, _ = w.Write([]byte("event: " + msg.event + "\ndata: "))
			_, _ = w.Write(msg.data)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}
