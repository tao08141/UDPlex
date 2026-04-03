//go:build dev
// +build dev

package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sync/atomic"
)

func (r *Router) incrementBufferRef() {
	atomic.AddInt32(&r.bufferRefCount, 1)
}

func (r *Router) decrementBufferRef() {
	atomic.AddInt32(&r.bufferRefCount, -1)
}

func (r *Router) logBufferRef() {
	if atomic.LoadInt32(&r.bufferRefCount)%1000 == 0 {
		logger.Debugf("Buffer reference count: %d", atomic.LoadInt32(&r.bufferRefCount))
	}
}

// initPprof Start the pprof server in the dev environment
func initPprof() {
	go func() {
		addr := os.Getenv("UDPLEX_PPROF_ADDR")
		if addr == "" {
			if port := os.Getenv("UDPLEX_PPROF_PORT"); port != "" {
				addr = fmt.Sprintf("127.0.0.1:%s", port)
			} else {
				addr = "127.0.0.1:6060"
			}
		}
		logger.Infof("Starting pprof server on http://%s", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			logger.Errorf("Failed to start pprof server: %v", err)
		}
	}()
}
