package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"relaylab/internal/runtimecontrol"
	"relaylab/internal/update"
)

// A token left on disk does not prove this process belongs to a launcher.
// Only a managed child with the current instance credentials may hand off.
func wireLauncherUpdater(manager *update.Manager, autoRestart bool) (bool, int) {
	if os.Getenv("RELAYLAB_LAUNCHER_INSTANCE") == "" || manager == nil {
		return false, 0
	}
	address, token := os.Getenv("RELAYLAB_LAUNCHER_CONTROL_ADDR"), os.Getenv("RELAYLAB_LAUNCHER_TOKEN")
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || len(token) < 32 {
		return false, 0
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(ctx context.Context, method, path string, body any, out any) error {
		var encoded []byte
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			if err != nil {
				return errors.New("升级参数序列化失败")
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, bytes.NewReader(encoded))
		if err != nil {
			return errors.New("启动器地址无效")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-RelayLab-Launcher-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("启动器连接失败，请检查启动器状态")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("启动器拒绝操作（HTTP %d）", resp.StatusCode)
		}
		if out != nil {
			return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
		}
		return nil
	}
	var status struct {
		Protocol int `json:"protocol"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = call(ctx, "GET", "/status", nil, &status)
	cancel()
	if err != nil || status.Protocol != runtimecontrol.Protocol {
		return false, status.Protocol
	}
	manager.SetApplier(func(ctx context.Context, path string, info update.UpdateInfo) error {
		return call(ctx, "POST", "/control/replace", map[string]any{
			"operation_id": info.OperationID, "staged_path": path, "sha256": info.SHA256,
			"size": info.AssetSize, "version": strings.TrimPrefix(info.LatestVersion, "v"), "commit": info.Commit,
			"launcher_protocol": info.LauncherProtocol, "database_schema": info.DatabaseSchema,
			"usage_guard":         info.UsageGuard,
			"min_database_schema": info.MinDatabaseSchema, "defer_restart": !autoRestart,
		}, nil)
	})
	manager.SetRollbackWithID(func(ctx context.Context, id string) error {
		return call(ctx, "POST", "/control/rollback", map[string]any{"operation_id": id, "defer_restart": !autoRestart}, nil)
	})
	return true, status.Protocol
}
