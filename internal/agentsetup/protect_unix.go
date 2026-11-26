//go:build !windows

package agentsetup

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func privatePath(path string, dir bool) bool {
	st, err := os.Lstat(path)
	if err != nil {
		return false
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Geteuid()) && (dir && st.IsDir() || !dir && st.Mode().IsRegular()) && st.Mode().Perm()&0077 == 0
}

func seal(data []byte) ([]byte, error)   { return data, nil }
func unseal(data []byte) ([]byte, error) { return data, nil }

func lockStore(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
