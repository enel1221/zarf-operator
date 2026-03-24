package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// capturingHandler wraps a slog.Handler and stores formatted log lines
// in a bounded ring buffer for error reporting.
type capturingHandler struct {
	next  slog.Handler
	state *captureState
}

func newCapturingHandler(next slog.Handler, capacity int) *capturingHandler {
	if capacity <= 0 {
		capacity = 1
	}
	return &capturingHandler{
		next: next,
		state: &captureState{
			capacity: capacity,
			lines:    make([]string, capacity),
		},
	}
}

func (h *capturingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *capturingHandler) Handle(ctx context.Context, r slog.Record) error {
	h.addLine(formatRecord(r))
	return h.next.Handle(ctx, r)
}

func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &capturingHandler{
		next:  h.next.WithAttrs(attrs),
		state: h.state,
	}
}

func (h *capturingHandler) WithGroup(name string) slog.Handler {
	return &capturingHandler{
		next:  h.next.WithGroup(name),
		state: h.state,
	}
}

func (h *capturingHandler) addLine(line string) {
	h.state.mu.Lock()
	defer h.state.mu.Unlock()

	if h.state.count < h.state.capacity {
		idx := (h.state.start + h.state.count) % h.state.capacity
		h.state.lines[idx] = line
		h.state.count++
		return
	}

	h.state.lines[h.state.start] = line
	h.state.start = (h.state.start + 1) % h.state.capacity
}

func (h *capturingHandler) Lines() []string {
	h.state.mu.Lock()
	defer h.state.mu.Unlock()

	if h.state.count == 0 {
		return nil
	}

	out := make([]string, 0, h.state.count)
	for i := 0; i < h.state.count; i++ {
		idx := (h.state.start + i) % h.state.capacity
		out = append(out, h.state.lines[idx])
	}
	return out
}

type captureState struct {
	capacity int
	lines    []string
	start    int
	count    int
	mu       sync.Mutex
}

func formatRecord(r slog.Record) string {
	parts := []string{
		strings.ToUpper(r.Level.String()),
		r.Message,
	}

	r.Attrs(func(a slog.Attr) bool {
		parts = append(parts, fmt.Sprintf("%s=%v", a.Key, a.Value.Any()))
		return true
	})

	return strings.TrimSpace(strings.Join(parts, " "))
}
