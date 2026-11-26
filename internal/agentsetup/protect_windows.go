//go:build windows

package agentsetup

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privatePath(path string, dir bool) bool {
	st, err := os.Lstat(path)
	return err == nil && (dir && st.IsDir() || !dir && st.Mode().IsRegular())
}

// DPAPI protects the entire credential (including its destination) with the
// current Windows user's logon keys, even if another account can read the file.
func seal(data []byte) ([]byte, error)   { return dpapi(data, true) }
func unseal(data []byte) ([]byte, error) { return dpapi(data, false) }
func dpapi(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty DPAPI input")
	}
	src := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var dst windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&src, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &dst)
	} else {
		err = windows.CryptUnprotectData(&src, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &dst)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(dst.Data)))
	return append([]byte(nil), unsafe.Slice(dst.Data, dst.Size)...), nil
}

func lockStore(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped))
}
