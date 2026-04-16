package main

import "sync/atomic"

// KeyPool is a lock-free round-robin selector over a fixed set of API keys.
// A single counter is incremented atomically; modulo distributes requests
// evenly across keys. Order within a request is stable (caller holds the key).
type KeyPool struct {
	keys    []string
	counter uint64
}

func NewKeyPool(keys []string) *KeyPool {
	return &KeyPool{keys: keys}
}

func (p *KeyPool) Size() int {
	return len(p.keys)
}

// Next returns the next key via round-robin and its index for logging.
func (p *KeyPool) Next() (string, int) {
	n := uint64(len(p.keys))
	if n == 0 {
		return "", -1
	}
	idx := atomic.AddUint64(&p.counter, 1) - 1
	i := int(idx % n)
	return p.keys[i], i
}
