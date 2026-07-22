package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/orlandoburli/apiary/internal/db"
)

func executionEventFilter(r *http.Request) db.ExecutionEventFilter {
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	return db.ExecutionEventFilter{TaskID: q.Get("task"), WorkflowInstanceID: q.Get("instance"), Type: q.Get("type"), AfterID: after, Limit: limit}
}

func (d *Dispatcher) handleExecutionEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if d.db == nil {
		http.Error(w, "event store unavailable", http.StatusServiceUnavailable)
		return
	}
	events, err := d.db.ListExecutionEvents(r.Context(), executionEventFilter(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events)
}

func (d *Dispatcher) handleExecutionEventStream(w http.ResponseWriter, r *http.Request) {
	if d.db == nil {
		http.Error(w, "event store unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	filter := executionEventFilter(r)
	live, cancel := d.db.SubscribeExecutionEvents(64)
	defer cancel()
	write := func(event db.ExecutionEvent) bool {
		if (filter.TaskID != "" && event.TaskID != filter.TaskID) || (filter.WorkflowInstanceID != "" && event.WorkflowInstanceID != filter.WorkflowInstanceID) || (filter.Type != "" && event.Type != filter.Type) {
			return true
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: execution\ndata: %s\n\n", event.ID, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	replayed, err := d.db.ListExecutionEvents(r.Context(), filter)
	if err != nil {
		return
	}
	lastID := filter.AfterID
	for _, event := range replayed {
		if !write(event) {
			return
		}
		lastID = event.ID
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-live:
			if !ok {
				return
			}
			if event.ID > lastID && !write(event) {
				return
			}
		}
	}
}
