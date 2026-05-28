package engine

type internalHit struct {
	id    uint32
	score int
}

// Specialized min-heap over []internalHit. Inlined sift operations avoid the
// interface dispatch and any-boxing overhead of container/heap, which matters
// in the per-document inner loops of search.

// heapPushHit appends hit and sifts it up. Returns the new slice header.
func heapPushHit(h []internalHit, hit internalHit) []internalHit {
	h = append(h, hit)
	i := len(h) - 1
	for i > 0 {
		parent := (i - 1) >> 1
		if h[parent].score <= h[i].score {
			break
		}
		h[parent], h[i] = h[i], h[parent]
		i = parent
	}
	return h
}

// heapReplaceTop overwrites the root and sifts it down: one operation instead
// of pop+push when the heap is already at capacity. len(h) > 0.
func heapReplaceTop(h []internalHit, hit internalHit) {
	h[0] = hit
	siftDownHit(h, 0, len(h))
}

func siftDownHit(h []internalHit, start, n int) {
	i := start
	for {
		left := 2*i + 1
		if left >= n {
			return
		}
		smallest := left
		if right := left + 1; right < n && h[right].score < h[left].score {
			smallest = right
		}
		if h[i].score <= h[smallest].score {
			return
		}
		h[i], h[smallest] = h[smallest], h[i]
		i = smallest
	}
}
