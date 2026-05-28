package engine

// Bitsets are indexed by internalDocID: word = id>>6, bit = id&63.

// filterBitSet grows bits as needed and sets the bit for id.
func filterBitSet(bits []uint64, id uint32) []uint64 {
	word := id >> 6
	for uint32(len(bits)) <= word {
		bits = append(bits, 0)
	}
	bits[word] |= 1 << (id & 63)
	return bits
}

func filterBitTest(bits []uint64, id uint32) bool {
	word := id >> 6
	return uint32(len(bits)) > word && bits[word]&(1<<(id&63)) != 0
}

// filterBitOr returns a new bitset that is the union of a and b.
func filterBitOr(a, b []uint64) []uint64 {
	if len(b) > len(a) {
		a, b = b, a
	}
	out := make([]uint64, len(a))
	copy(out, a)
	for i := range b {
		out[i] |= b[i]
	}
	return out
}

// filterBitAnd returns a new bitset that is the intersection of a and b.
func filterBitAnd(a, b []uint64) []uint64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	out := make([]uint64, n)
	for i := range out {
		out[i] = a[i] & b[i]
	}
	return out
}
