package engine

import (
	"sync"
	"testing"
)

// helper to build a SearchEngine with prepopulated FilterBits
func newTestEngineWithData(data map[string][]uint64) *SearchEngine {
	return &SearchEngine{
		mu:         sync.RWMutex{},
		FilterBits: data,
		DocDeleted: make(map[uint32]bool),
		ResultSize: 100,
	}
}

func TestApplyFilterLocked_SingleValue(t *testing.T) {
	bits := make(map[string][]uint64)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 1)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 2)
	se := newTestEngineWithData(bits)

	filters := map[string][]interface{}{"author": {"Alice"}}
	se.mu.RLock()
	got := se.ApplyFilterLocked(filters)
	se.mu.RUnlock()

	if got == nil {
		t.Fatal("expected non-nil bitset")
	}
	if !filterBitTest(got, 1) || !filterBitTest(got, 2) {
		t.Errorf("expected ids 1 and 2 to be set in bitset, got %v", got)
	}
}

func TestApplyFilterLocked_MultiValueOrAndMultiFieldAnd(t *testing.T) {
	bits := make(map[string][]uint64)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 1)
	bits["author:Bob"] = filterBitSet(bits["author:Bob"], 2)
	bits["author:Eve"] = filterBitSet(bits["author:Eve"], 3)
	bits["genre:rock"] = filterBitSet(bits["genre:rock"], 1)
	bits["genre:rock"] = filterBitSet(bits["genre:rock"], 2)
	bits["genre:jazz"] = filterBitSet(bits["genre:jazz"], 3)
	se := newTestEngineWithData(bits)

	se.mu.RLock()
	got := se.ApplyFilterLocked(map[string][]interface{}{
		"author": {"Alice", "Bob"},
		"genre":  {"rock"},
	})
	se.mu.RUnlock()

	if got == nil {
		t.Fatal("expected non-nil bitset")
	}
	if !filterBitTest(got, 1) || !filterBitTest(got, 2) {
		t.Fatalf("expected ids 1 and 2 after OR within author and AND with genre, got %v", got)
	}
	if filterBitTest(got, 3) {
		t.Fatalf("did not expect id 3 after genre AND, got %v", got)
	}

	se.mu.RLock()
	none := se.ApplyFilterLocked(map[string][]interface{}{
		"author": {"Alice"},
		"genre":  {"jazz"},
	})
	se.mu.RUnlock()

	if none != nil {
		hasAny := false
		for _, word := range none {
			if word != 0 {
				hasAny = true
				break
			}
		}
		if hasAny {
			t.Fatalf("expected empty intersection, got %v", none)
		}
	}
}

func TestFilterBitAnd_DifferentLengths(t *testing.T) {
	var a []uint64
	a = filterBitSet(a, 1)
	a = filterBitSet(a, 65)

	var b []uint64
	b = filterBitSet(b, 1)

	got := filterBitAnd(a, b)
	if !filterBitTest(got, 1) {
		t.Fatalf("expected id 1 in intersection, got %v", got)
	}
	if filterBitTest(got, 65) {
		t.Fatalf("did not expect id 65 in intersection, got %v", got)
	}
}

func TestApplyFilterLocked_EmptyFilters(t *testing.T) {
	se := newTestEngineWithData(make(map[string][]uint64))

	se.mu.RLock()
	got := se.ApplyFilterLocked(map[string][]interface{}{})
	se.mu.RUnlock()

	if got != nil {
		t.Fatalf("expected nil for empty filters, got %v", got)
	}
}

func TestApplyFilterLocked_MissingKey(t *testing.T) {
	bits := make(map[string][]uint64)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 1)
	se := newTestEngineWithData(bits)

	se.mu.RLock()
	got := se.ApplyFilterLocked(map[string][]interface{}{"author": {"Unknown"}})
	se.mu.RUnlock()

	if got != nil {
		t.Fatalf("expected nil for missing key, got %v", got)
	}
}

func TestApplyFilterLocked_SingleFieldMultiValue_OR(t *testing.T) {
	bits := make(map[string][]uint64)
	bits["genre:rock"] = filterBitSet(bits["genre:rock"], 10)
	bits["genre:jazz"] = filterBitSet(bits["genre:jazz"], 20)
	bits["genre:pop"] = filterBitSet(bits["genre:pop"], 30)
	se := newTestEngineWithData(bits)

	se.mu.RLock()
	got := se.ApplyFilterLocked(map[string][]interface{}{"genre": {"rock", "jazz"}})
	se.mu.RUnlock()

	if got == nil {
		t.Fatal("expected non-nil bitset for OR query")
	}
	if !filterBitTest(got, 10) || !filterBitTest(got, 20) {
		t.Fatalf("expected ids 10 and 20 set (OR), got %v", got)
	}
	if filterBitTest(got, 30) {
		t.Fatalf("did not expect id 30 (pop not in filter), got %v", got)
	}
}

func TestApplyFilterLocked_MultiFieldAndNoMatch(t *testing.T) {
	bits := make(map[string][]uint64)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 1)
	bits["genre:jazz"] = filterBitSet(bits["genre:jazz"], 2)
	se := newTestEngineWithData(bits)

	se.mu.RLock()
	got := se.ApplyFilterLocked(map[string][]interface{}{
		"author": {"Alice"},
		"genre":  {"jazz"},
	})
	se.mu.RUnlock()

	hasAny := false
	for _, w := range got {
		if w != 0 {
			hasAny = true
			break
		}
	}
	if hasAny {
		t.Fatalf("expected no matching docs (AND with disjoint sets), got %v", got)
	}
}
