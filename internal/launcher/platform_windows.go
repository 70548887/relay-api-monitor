//go:build windows

package launcher

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"strings"
	"time"
)

func acquireLock(path string) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = protectFile(path); e != nil {
		f.Close()
		return nil, e
	}
	e = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if e != nil {
		f.Close()
		return nil, fmt.Errorf("launcher: data directory is already owned: %w", e)
	}
	return f, nil
}
func releaseLock(f *os.File) error {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
	return f.Close()
}
func atomicRename(source, dest string) error {
	from, e := windows.UTF16PtrFromString(source)
	if e != nil {
		return e
	}
	to, e := windows.UTF16PtrFromString(dest)
	if e != nil {
		return e
	}
	for attempt := 0; ; attempt++ {
		err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil {
			return nil
		}
		if attempt >= 49 || (err != windows.ERROR_SHARING_VIOLATION && err != windows.ERROR_ACCESS_DENIED && err != windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func syncDirectory(string) error { return nil } // MoveFileEx WRITE_THROUGH provides the replacement durability.
func processAlive(pid int) bool {
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if e != nil {
		return e != windows.ERROR_INVALID_PARAMETER
	}
	defer windows.CloseHandle(h)
	var code uint32
	if e = windows.GetExitCodeProcess(h, &code); e != nil {
		return true
	}
	return code == 259
}
func processIdentity(pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return "", os.ErrNotExist
		}
		return "", err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err = windows.GetExitCodeProcess(h, &code); err != nil {
		return "", err
	}
	if code != 259 {
		return "", os.ErrNotExist
	}
	var creation, exit, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	name := make([]uint16, 32768)
	size := uint32(len(name))
	if err = windows.QueryFullProcessImageName(h, 0, &name[0], &size); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d|%s", creation.HighDateTime, creation.LowDateTime, strings.ToLower(windows.UTF16ToString(name[:size]))), nil
}

// Restrict only files owned by this launcher. Never rewrite the data directory
// ACL or touch unrelated user files. SYSTEM and the current principal retain access.
func protectFile(path string) error {
	return protectOwnedPath(path, false)
}
func protectDirectory(path string) error {
	return protectOwnedPath(path, true)
}
func protectOwnedPath(path string, directory bool) error {
	user, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return e
	}
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	sd, e := windows.SecurityDescriptorFromString("D:P(A;" + inherit + ";FA;;;SY)(A;" + inherit + ";FA;;;" + user.User.Sid.String() + ")")
	if e != nil {
		return e
	}
	acl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
