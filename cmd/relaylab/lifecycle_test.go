package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHTTPShutdownWaitsForHandlerCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	cleaned := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		select {
		case <-release:
		case <-time.After(120 * time.Millisecond):
		}
		close(cleaned)
	})
	srv := newHTTPServer("", h, ctx)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, srv, listener) }()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler not entered")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-cleaned:
		default:
			t.Fatal("server returned before handler cleanup")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown hung")
	}
	<-clientDone
}

func TestHTTPReadDeadlineDoesNotLimitLongStreamingResponse(t *testing.T) {
	srv := newHTTPServer("", http.NotFoundHandler(), context.Background())
	if srv.ReadTimeout <= 0 || srv.ReadHeaderTimeout <= 0 {
		t.Fatal("request-body deadlines missing")
	}
	if srv.WriteTimeout != 0 {
		t.Fatal("whole-response deadline would interrupt long model streams")
	}
}

func TestUnknownCLICommandDoesNotCreateData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "should-not-exist")
	if err := run([]string{"unknown-command", "--data-dir", dir}); err == nil {
		t.Fatal("unknown command accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("unknown command modified filesystem")
	}
}
