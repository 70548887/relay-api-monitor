//go:build !windows

package main

import "os"

func replace(a, b string) error { return os.Rename(a, b) }
