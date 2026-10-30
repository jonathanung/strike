package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

// Regression for #1678: an open SSE tail must not pin Shutdown. Before
// the fix the handler only watched the request context, which
// http.Server.Shutdown never cancels, so Shutdown burned its whole
// timeout and the stream stayed open afterwards.
func TestShutdownEndsSessionEventStream(t *testing.T) {
	dir := t.TempDir()
	writeFixtureSession(t, dir, "s1", protocol.UserMessage{Text: "hi"})
	srv := testServer(t, dir, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/v1/sessions/s1/events")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	// Headers are back, so the handler is parked in its tail loop.
	bodyDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(resp.Body)
		bodyDone <- err
	}()

	shutDone := make(chan error, 1)
	start := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutDone <- srv.Shutdown(ctx)
	}()
	select {
	case err := <-shutDone:
		if err != nil {
			t.Fatalf("Shutdown = %v, want nil (graceful, not timed out)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return within 3s with an SSE stream open")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Shutdown took %v, want prompt", elapsed)
	}
	select {
	case <-bodyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream did not end after Shutdown")
	}
	_ = resp.Body.Close()
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve = %v, want ErrServerClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}

// A handler that ignores the shutdown signal must still not hang the
// process: when the graceful context expires, Shutdown force-closes
// the remaining connections and returns the context error promptly.
func TestShutdownForceClosesStubbornHandler(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	srv.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		resp, err := http.Get("http://" + ln.Addr().String() + "/stuck")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stubborn handler never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = srv.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Shutdown took %v, want bounded by its context", elapsed)
	}
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve = %v, want ErrServerClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after force close")
	}
}
