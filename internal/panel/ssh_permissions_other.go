//go:build !windows

package panel

import "os"

func restrictSSHDirectory(path string) error { return os.Chmod(path, 0700) }
