//go:build linux || darwin

package exclusion

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNewExcluderRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exclusions.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := NewExcluder(path)
		result <- err
	}()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("NewExcluder blocked while opening a FIFO")
	}
}
