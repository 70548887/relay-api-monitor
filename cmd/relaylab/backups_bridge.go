package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"relaylab/internal/launcher"
	"relaylab/internal/runtimecontrol"
	"relaylab/internal/server"
	"slices"
	"time"
)

type restoreRejectedError struct{ status int }

func (e restoreRejectedError) Error() string         { return "启动器已拒绝恢复操作" }
func (e restoreRejectedError) RestoreRejected() bool { return e.status >= 400 && e.status < 500 }

func wireLauncherRestore(app *server.Server) {
	if os.Getenv("RELAYLAB_LAUNCHER_INSTANCE") == "" {
		return
	}
	address, token := os.Getenv("RELAYLAB_LAUNCHER_CONTROL_ADDR"), os.Getenv("RELAYLAB_LAUNCHER_TOKEN")
	host, _, e := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if e != nil || ip == nil || !ip.IsLoopback() || len(token) < 32 {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(ctx context.Context, method, path string, body any, out any) error {
		var data []byte
		if body != nil {
			var e error
			data, e = json.Marshal(body)
			if e != nil {
				return e
			}
		}
		req, e := http.NewRequestWithContext(ctx, method, "http://"+address+path, bytes.NewReader(data))
		if e != nil {
			return e
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-RelayLab-Launcher-Token", token)
		resp, e := client.Do(req)
		if e != nil {
			return errors.New("启动器连接失败")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return restoreRejectedError{status: resp.StatusCode}
		}
		if out != nil {
			return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
		}
		return nil
	}
	var status struct {
		Protocol     int      `json:"protocol"`
		Capabilities []string `json:"capabilities"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if call(ctx, "GET", "/status", nil, &status) != nil || status.Protocol != runtimecontrol.Protocol || !slices.Contains(status.Capabilities, "restore_v1") {
		return
	}
	app.ConfigureBackupRestore(true, func(ctx context.Context, req launcher.RestoreRequest) error {
		return call(ctx, "POST", "/control/restore", req, nil)
	})
}
