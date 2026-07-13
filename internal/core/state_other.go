//go:build !windows

package core

// hideDir is a no-op off Windows (dot-prefixed dirs are already hidden there).
func hideDir(string) {}
