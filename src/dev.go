//go:build dev
// +build dev

package main

import (
	"net/http"
	_ "net/http/pprof"
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
		logger.Infof("Starting pprof server on http://localhost:6060")
		if err := http.ListenAndServe("localhost:6060", nil); err != nil {
			logger.Errorf("Failed to start pprof server: %v", err)
		}
	}()
}
