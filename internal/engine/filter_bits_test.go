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

func TestApplyFilter_SingleValue(t *testing.T) {
	bits := make(map[string][]uint64)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 1)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 2)
	se := newTestEngineWithData(bits)

	filters := map[string][]interface{}{"author": {"Alice"}}
	got := se.ApplyFilter(filters)
	if got == nil {
		t.Fatal("expected non-nil bitset")
	}
	if !filterBitTest(got, 1) || !filterBitTest(got, 2) {
		t.Errorf("expected ids 1 and 2 to be set in bitset, got %v", got)
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

func TestApplyFilter_MultiValueOrAndMultiFieldAnd(t *testing.T) {
	bits := make(map[string][]uint64)
	bits["author:Alice"] = filterBitSet(bits["author:Alice"], 1)
	bits["author:Bob"] = filterBitSet(bits["author:Bob"], 2)
	bits["author:Eve"] = filterBitSet(bits["author:Eve"], 3)
	bits["genre:rock"] = filterBitSet(bits["genre:rock"], 1)
	bits["genre:rock"] = filterBitSet(bits["genre:rock"], 2)
	bits["genre:jazz"] = filterBitSet(bits["genre:jazz"], 3)
	se := newTestEngineWithData(bits)

	got := se.ApplyFilter(map[string][]interface{}{
		"author": {"Alice", "Bob"},
		"genre":  {"rock"},
	})
	if got == nil {
		t.Fatal("expected non-nil bitset")
	}
	if !filterBitTest(got, 1) || !filterBitTest(got, 2) {
		t.Fatalf("expected ids 1 and 2 after OR within author and AND with genre, got %v", got)
	}
	if filterBitTest(got, 3) {
		t.Fatalf("did not expect id 3 after genre AND, got %v", got)
	}

	none := se.ApplyFilter(map[string][]interface{}{
		"author": {"Alice"},
		"genre":  {"jazz"},
	})
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
