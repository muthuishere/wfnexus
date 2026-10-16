//go:build !windows

package main

import (
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// detach puts the helper in its own session so it outlives the command.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func killProcess(pid int) {
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}

func stopOnSignal(ln net.Listener, cleanup func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() { <-ch; ln.Close(); cleanup(); os.Exit(0) }()
}
