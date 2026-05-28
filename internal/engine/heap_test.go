package engine

import "testing"

func TestMinHeap_PushPopOrder(t *testing.T) {
	var h []internalHit

	input := []internalHit{
		{id: 1, score: 50},
		{id: 2, score: 10},
		{id: 3, score: 30},
		{id: 4, score: 5},
		{id: 5, score: 20},
	}

	for _, v := range input {
		h = heapPushHit(h, v)
	}

	n := len(h)
	out := make([]internalHit, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		out[i] = hit
	}

	expectedOrder := []int{50, 30, 20, 10, 5}
	for i, want := range expectedOrder {
		if out[i].score != want {
			t.Errorf("index %d: expected %v, got %v", i, want, out[i].score)
		}
	}
}

func TestMinHeap_Len(t *testing.T) {
	var h []internalHit

	if len(h) != 0 {
		t.Fatalf("expected empty heap")
	}

	h = heapPushHit(h, internalHit{id: 1, score: 10})
	h = heapPushHit(h, internalHit{id: 2, score: 20})

	if len(h) != 2 {
		t.Fatalf("expected len 2, got %d", len(h))
	}
}

func TestMinHeap_SingleElement(t *testing.T) {
	var h []internalHit

	h = heapPushHit(h, internalHit{id: 1, score: 42})

	n := len(h)
	out := make([]internalHit, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		out[i] = hit
	}

	if out[0].score != 42 {
		t.Errorf("expected 42, got %v", out[0].score)
	}
}

func TestMinHeap_StabilityRandomInsertions(t *testing.T) {
	var h []internalHit

	values := []int{100, 1, 50, 2, 99, 3, 75, 4, 60}

	for i, v := range values {
		h = heapPushHit(h, internalHit{id: uint32(i), score: v})
	}

	n := len(h)
	out := make([]internalHit, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		out[i] = hit
	}

	for i := 1; i < len(out); i++ {
		if out[i].score > out[i-1].score {
			t.Errorf("heap order violated at index %d: %v > %v", i, out[i].score, out[i-1].score)
		}
	}
}

func TestMinHeap_StabilityRandomInsertions2(t *testing.T) {
	var h []internalHit

	values := []int{100, 105, 1, 50, 2, 99, 101, 3, 75, 4, 60, 104, 110, 95, 90, 90, 106, 8, 111, 101, 106, 79}

	k := 5
	for i, score := range values {
		if len(h) < k {
			h = heapPushHit(h, internalHit{id: uint32(i), score: score})
		} else if h[0].score < score {
			heapReplaceTop(h, internalHit{id: uint32(i), score: score})
		}
	}

	n := len(h)
	scores := make([]int, n)
	for i := n - 1; i >= 0; i-- {
		hit := h[0]
		if i > 0 {
			h[0] = h[i]
			siftDownHit(h, 0, i)
		}
		scores[i] = hit.score
	}

	for i := 1; i < len(scores); i++ {
		if scores[i] > scores[i-1] {
			t.Errorf("heap order violated at index %d: %v > %v", i, scores[i], scores[i-1])
		}
	}
	if scores[0] != 111 || scores[1] != 110 || scores[2] != 106 || scores[3] != 106 || scores[4] != 105 {
		t.Errorf("Score violated: got %v", scores)
	}
}
