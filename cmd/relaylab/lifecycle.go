package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

func newHTTPServer(address string, handler http.Handler, ctx context.Context) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 75 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
}

// Wait for Shutdown to drain handlers before returning to the caller that owns
// the database. Serve itself returns as soon as the listener is closed.
func serveHTTP(ctx context.Context, server *http.Server, listener net.Listener) error {
	servingDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := server.Shutdown(stop); err != nil {
				_ = server.Close()
			}
		case <-servingDone:
		}
	}()
	err := server.Serve(listener)
	close(servingDone)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		_ = server.Close()
	}
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
