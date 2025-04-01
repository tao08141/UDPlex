package main

import "net"

type Packet struct {
	buffer  []byte
	length  int
	srcAddr net.Addr
	srcTag  string
}
