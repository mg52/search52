package engine

import (
	"reflect"
	"testing"
)

func TestTokenize_Basic(t *testing.T) {
	got := Tokenize("Hello World")
	want := []string{"hello", "world"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Uppercase(t *testing.T) {
	got := Tokenize("APPLE IPHONE PRO")
	want := []string{"apple", "iphone", "pro"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Stopwords(t *testing.T) {
	got := Tokenize("a the and coffee")
	want := []string{"coffee"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Punctuation(t *testing.T) {
	// Punctuation is stripped; adjacent alpha chars are joined into one token.
	got := Tokenize("hello, world! foo-bar co.uk")
	want := []string{"hello", "world", "foobar", "couk"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Numbers(t *testing.T) {
	got := Tokenize("iphone12 co2 123")
	want := []string{"iphone12", "co2", "123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Empty(t *testing.T) {
	if got := Tokenize(""); len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestTokenize_OnlyPunctuation(t *testing.T) {
	if got := Tokenize("!!! ??? ---"); len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestTokenize_OnlyStopwords(t *testing.T) {
	if got := Tokenize("a the and"); len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestTokenize_NonASCIIStripped(t *testing.T) {
	// Non-ASCII bytes are silently stripped (same behavior as the former
	// [^a-zA-Z0-9]+ regex, which only kept ASCII alphanumeric chars).
	// "café" → "caf", "naïve" → "nave"
	got := Tokenize("café naïve")
	want := []string{"caf", "nave"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_WhitespaceOnly(t *testing.T) {
	if got := Tokenize("   \t\n  "); len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestTokenize_MixedCase(t *testing.T) {
	got := Tokenize("iPhone MacBook")
	want := []string{"iphone", "macbook"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAlphaNumericLower_AllRanges(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ABC", "abc"},
		{"abc", "abc"},
		{"123", "123"},
		{"a1B2c3", "a1b2c3"},
		{"!@#$%", ""},
		{"", ""},
		{"Hello-World", "helloworld"},
	}
	for _, c := range cases {
		got := alphaNumericLower(c.in)
		if got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
