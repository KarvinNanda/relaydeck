// Package logx sets up structured JSON logging and an HTTP middleware that
// logs one line per request.
package logx

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type ctxKey struct{}

// New returns a logger that writes one JSON object per line to w.
func New(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}

// WithRequestID returns a context that carries the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID returns the request id in ctx, or an empty string.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("logx: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Flush passes through to the underlying writer, so streaming handlers that
// assert http.Flusher directly keep working.
func (s *statusWriter) Flush() {
	_ = http.NewResponseController(s.ResponseWriter).Flush()
}

// Hijack passes through to the underlying writer, so WebSocket libraries that
// assert http.Hijacker directly keep working. A successful hijack is a protocol
// switch, so the log line records status 101.
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err == nil && s.status == 0 {
		s.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

// Middleware sets a random request id on the request context and logs one line
// per request: method, path, status, duration_ms, request_id. The query string
// is never logged, because tickets travel there.
func Middleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newID()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(WithRequestID(r.Context(), id)))
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", id,
		)
	})
}
