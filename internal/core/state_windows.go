//go:build windows

package core

import "syscall"

// hideDir marks a directory hidden so .keepsake doesn't clutter the vault in
// Explorer (the dot-prefix alone doesn't hide files on Windows).
func hideDir(path string) {
	if p, err := syscall.UTF16PtrFromString(path); err == nil {
		_ = syscall.SetFileAttributes(p, syscall.FILE_ATTRIBUTE_HIDDEN)
	}
}
