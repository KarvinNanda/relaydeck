package logx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewareLogsOneJSONLinePerRequest(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf)
	var seen string
	h := Middleware(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodGet, "/x?ticket=secret", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	if strings.Contains(out, "secret") {
		t.Fatalf("log leaked the query string: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %q", len(lines), out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	id, _ := rec["request_id"].(string)
	if id == "" || id != seen {
		t.Errorf("request_id = %q, handler saw %q", id, seen)
	}
	if rec["path"] != "/x" {
		t.Errorf("path = %v, want /x", rec["path"])
	}
	if rec["status"] != float64(http.StatusTeapot) {
		t.Errorf("status = %v, want 418", rec["status"])
	}
	if rec["method"] != "GET" {
		t.Errorf("method = %v", rec["method"])
	}
	if _, ok := rec["duration_ms"]; !ok {
		t.Error("duration_ms missing")
	}
}

func TestMiddlewareDefaultStatusIs200(t *testing.T) {
	var buf bytes.Buffer
	h := Middleware(New(&buf), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["status"] != float64(200) {
		t.Errorf("status = %v, want 200", rec["status"])
	}
}

func TestMiddlewarePassesHijackAndFlushThrough(t *testing.T) {
	var buf bytes.Buffer
	mw := Middleware(New(&buf), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("wrapped writer is not an http.Flusher")
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("wrapped writer is not an http.Hijacker")
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n\r\n")
		_ = rw.Flush()
	}))
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		mw.ServeHTTP(w, r)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ws?ticket=secret")
	if err != nil {
		t.Fatal(err)
	}
	_ = bufio.NewReader(resp.Body)
	resp.Body.Close()
	<-done

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line: %v: %q", err, buf.String())
	}
	if rec["status"] != float64(http.StatusSwitchingProtocols) {
		t.Errorf("status = %v, want 101", rec["status"])
	}
}
