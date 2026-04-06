//go:build !linux

package main

import "syscall"

func outboundInterfaceControl(interfaceName string) func(string, string, syscall.RawConn) error {
	return nil
}
