//go:build !windows

package wamonitor

import (
	"fmt"
	"os"
	"syscall"
)

// Lock takes the account's exclusive lock: one monitor per account, ever.
// The OS drops it when the process dies, so a crash never leaves it stuck.
func Lock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another apl.whatsapp holds %s — one per account", path)
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
