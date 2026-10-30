//go:build windows

package backup

import "golang.org/x/sys/windows"

func protectDirectory(path string) error { return protectPath(path, "OICI") }
func protectFile(path string) error      { return protectPath(path, "") }

// Newly created backup directories may contain the plaintext DB and master
// key. Fail closed if we cannot restrict them, rather than inheriting a shared
// parent directory's read permissions.
func protectPath(path, inheritance string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + inheritance + ";FA;;;SY)(A;" + inheritance + ";FA;;;BA)(A;" + inheritance + ";FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
