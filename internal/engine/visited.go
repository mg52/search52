package engine

import "sync"

// visitedBits is a pooled bitset used to deduplicate internal document IDs while
// scanning multiple posting lists within a single search. It replaces a per-query
// map[uint32]struct{}, eliminating the allocation and hashing cost on the hot path
// for queries whose candidates span more than one posting list.
type visitedBits struct {
	bits []uint64
}

var visitedPool = sync.Pool{
	New: func() any { return &visitedBits{} },
}

// getVisited returns a zeroed bitset able to hold IDs in [0, size). Pass the
// engine's nextInternalID so every assignable internal ID fits. The buffer is
// reused across queries via a sync.Pool; release it with putVisited once the
// scan completes.
func getVisited(size uint32) *visitedBits {
	v := visitedPool.Get().(*visitedBits)
	words := int((size + 63) >> 6)
	if cap(v.bits) < words {
		v.bits = make([]uint64, words) // freshly zeroed
	} else {
		v.bits = v.bits[:words]
		clear(v.bits)
	}
	return v
}

func putVisited(v *visitedBits) {
	visitedPool.Put(v)
}

// markSeen reports whether id has already been seen during this scan, recording
// it as seen otherwise. Callers must size the bitset (via getVisited) so that
// id < size for every id passed here.
func (v *visitedBits) markSeen(id uint32) bool {
	word, bit := id>>6, uint64(1)<<(id&63)
	if v.bits[word]&bit != 0 {
		return true
	}
	v.bits[word] |= bit
	return false
}
