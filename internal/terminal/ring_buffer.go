package terminal

// ringBuffer keeps the last N bytes appended.
// It stores raw terminal output (including escape sequences) so late attachers can replay state.
type ringBuffer struct {
	maxBytes int
	buf      []byte
}

func newRingBuffer(maxBytes int) ringBuffer {
	if maxBytes <= 0 {
		maxBytes = 1024 * 1024
	}
	return ringBuffer{maxBytes: maxBytes}
}

func (r *ringBuffer) appendBytes(p []byte) {
	if len(p) == 0 {
		return
	}

	// If the incoming chunk is bigger than our buffer, keep only the tail.
	if len(p) >= r.maxBytes {
		r.buf = append(r.buf[:0], p[len(p)-r.maxBytes:]...)
		return
	}

	// Fast path: fits.
	if len(r.buf)+len(p) <= r.maxBytes {
		r.buf = append(r.buf, p...)
		return
	}

	// Drop oldest bytes to make room.
	drop := len(r.buf) + len(p) - r.maxBytes
	r.buf = append(r.buf[drop:], p...)
}

func (r *ringBuffer) bytes() []byte {
	return append([]byte(nil), r.buf...)
}
