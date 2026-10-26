package main

import (
	"golang.org/x/sys/windows"
	"time"
)

func replace(a, b string) error {
	from, e := windows.UTF16PtrFromString(a)
	if e != nil {
		return e
	}
	to, e := windows.UTF16PtrFromString(b)
	if e != nil {
		return e
	}
	for i := 0; ; i++ {
		e := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if e == nil || i >= 19 {
			return e
		}
		time.Sleep(5 * time.Millisecond)
	}
}
