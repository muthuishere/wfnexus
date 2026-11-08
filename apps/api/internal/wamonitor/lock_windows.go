//go:build windows

package wamonitor

import (
	"fmt"
	"os"
)

// Lock on Windows: an exclusive create. A crash leaves the file; delete it.
func Lock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("another apl.whatsapp holds %s — one per account (delete it if none runs)", path)
	}
	return func() { f.Close(); os.Remove(path) }, nil
}
