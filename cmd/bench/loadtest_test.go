package main

import (
	"math/rand"
	"testing"
)

func TestLoadtestPrefixWordUsesConfiguredRange(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	for i := 0; i < 100; i++ {
		got, n, ok := loadtestPrefixWord(r, "abcdefghijklmno", 1, 10)
		if !ok {
			t.Fatal("expected prefix")
		}
		if n < 1 || n > 10 {
			t.Fatalf("prefix length = %d, want 1..10", n)
		}
		if got != "abcdefghijklmno"[:n] {
			t.Fatalf("prefix = %q, want first %d chars", got, n)
		}
	}
}

func TestLoadtestPrefixWordKeepsPrefixForShortWords(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	got, n, ok := loadtestPrefixWord(r, "hor", 1, 10)
	if !ok {
		t.Fatal("expected prefix")
	}
	if got != "ho" || n != 2 {
		t.Fatalf("prefix = %q len=%d, want ho len=2", got, n)
	}
}
