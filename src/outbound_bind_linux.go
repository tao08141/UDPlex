//go:build linux

package main

import (
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func outboundInterfaceControl(interfaceName string) func(string, string, syscall.RawConn) error {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return nil
	}

	return func(network, address string, rawConn syscall.RawConn) error {
		var sockErr error
		err := rawConn.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, interfaceName)
		})
		if err != nil {
			return err
		}
		if sockErr != nil {
			return fmt.Errorf("failed to bind socket to interface %q: %w", interfaceName, sockErr)
		}
		return nil
	}
}
