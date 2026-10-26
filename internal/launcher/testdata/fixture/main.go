package main

import (
	"archive/zip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

var version = "old"

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	role := os.Args[1]
	if role == "version" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"version": releaseVersion(), "commit": "commit-" + version, "os": runtime.GOOS, "arch": runtime.GOARCH, "launcher_protocol": 2, "database_schema": 1, "min_database_schema": 1})
		return
	}
	fs := flag.NewFlagSet(role, flag.ExitOnError)
	dir := fs.String("data-dir", "", "data")
	_ = fs.String("addr", "", "addr")
	_ = fs.String("config", "", "config")
	output := fs.String("output", "", "output")
	input := fs.String("input", "", "input")
	_ = fs.Parse(os.Args[2:])
	if role == "backup" {
		f, e := os.Create(*output)
		check(e)
		zw := zip.NewWriter(f)
		for _, name := range []string{"relaylab.db", "master.key"} {
			b, e := os.ReadFile(filepath.Join(*dir, name))
			check(e)
			w, e := zw.Create(name)
			check(e)
			_, e = w.Write(b)
			check(e)
		}
		check(zw.Close())
		check(f.Close())
		return
	}
	if role == "restore" {
		check(os.MkdirAll(*dir, 0700))
		zr, e := zip.OpenReader(*input)
		check(e)
		defer zr.Close()
		for _, member := range zr.File {
			r, e := member.Open()
			check(e)
			b, e := io.ReadAll(r)
			check(e)
			check(r.Close())
			check(os.WriteFile(filepath.Join(*dir, member.Name), b, 0600))
		}
		return
	}
	if role != "serve" && role != "worker" {
		os.Exit(2)
	}
	if !exists(filepath.Join(*dir, "relaylab.db")) {
		if role == "worker" {
			fmt.Fprintln(os.Stderr, "worker opened before database initialization")
			os.Exit(3)
		}
		time.Sleep(120 * time.Millisecond)
		check(os.WriteFile(filepath.Join(*dir, "relaylab.db"), []byte("original-data"), 0600))
		check(os.WriteFile(filepath.Join(*dir, "master.key"), make([]byte, 32), 0600))
	}
	if version == "bad" {
		check(os.WriteFile(filepath.Join(*dir, "relaylab.db"), []byte("bad-migration"), 0600))
	}
	event(*dir, role+" start "+version)
	for {
		maintenance := exists(filepath.Join(*dir, "maintenance.request"))
		active := 0
		if exists(filepath.Join(*dir, "hold-active")) {
			active = 1
		}
		instance := os.Getenv("RELAYLAB_LAUNCHER_INSTANCE")
		if exists(filepath.Join(*dir, "wrong-identity")) {
			instance = "another-instance"
		}
		report := map[string]any{"pid": os.Getpid(), "instance_id": instance, "role": role, "version": releaseVersion(), "commit": "commit-" + version, "launcher_protocol": 2, "database_schema": 1, "maintenance": maintenance, "active": active, "ready": version != "bad", "heartbeat_at": time.Now().UTC()}
		b, _ := json.Marshal(report)
		tmp := filepath.Join(*dir, "runtime-"+role+".json.tmp")
		check(os.WriteFile(tmp, b, 0600))
		check(replace(tmp, filepath.Join(*dir, "runtime-"+role+".json")))
		if exists(filepath.Join(*dir, "stop.request")) || exists(filepath.Join(*dir, role+".stop.request")) {
			event(*dir, role+" stop "+version)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func exists(path string) bool { _, e := os.Stat(path); return e == nil }
func releaseVersion() string {
	return map[string]string{"old": "1.0.0", "new": "1.1.0", "bad": "1.2.0"}[version]
}
func event(dir, message string) {
	f, e := os.OpenFile(filepath.Join(dir, "fixture-events.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	check(e)
	_, e = fmt.Fprintln(f, message)
	check(e)
	check(f.Close())
}
func check(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
