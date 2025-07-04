//go:build dev
// +build dev

package main

import "sync/atomic"

func (r *Router) incrementBufferRef() {
	atomic.AddInt32(&r.bufferRefCount, 1)
}

func (r *Router) decrementBufferRef() {
	atomic.AddInt32(&r.bufferRefCount, -1)
}

func (r *Router) logBufferRef() {
	if atomic.LoadInt32(&r.bufferRefCount)%100 == 0 {
		logger.Debugf("Buffer reference count: %d", atomic.LoadInt32(&r.bufferRefCount))
	}
}
