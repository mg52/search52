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

func TestTokenize_NonASCIIPreserved(t *testing.T) {
	// Unicode letters are preserved and lowercased; only non-letter/digit
	// ASCII punctuation is stripped.
	// "café" → "café", "naïve" → "naïve"
	got := Tokenize("café naïve")
	want := []string{"café", "naïve"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_CJK(t *testing.T) {
	got := Tokenize("日本語 音楽")
	want := []string{"日本語", "音楽"}
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

// ---------------------------------------------------------------------------
// alphaNumericLower — per-script unit tests
// ---------------------------------------------------------------------------

func TestAlphaNumericLower_Cyrillic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ПРИВЕТ", "привет"},
		{"привет", "привет"},
		{"МиР", "мир"},
		{"РОССИЯ", "россия"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_Greek(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Ωμέγα", "ωμέγα"},
		{"ΚΑΛΗΜΕΡΑ", "καλημερα"},
		{"ελληνικά", "ελληνικά"},
		{"Σόλων", "σόλων"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_German(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ÜBER", "über"},
		{"Straße", "straße"},
		{"MÜLLER", "müller"},
		{"Österreich", "österreich"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_Turkish(t *testing.T) {
	// İ (U+0130) → unicode.ToLower → i; ş, ç, ğ, ı are letters and pass through
	cases := []struct{ in, want string }{
		{"İSTANBUL", "istanbul"},
		{"ŞEHIR", "şehir"},
		{"Müzik", "müzik"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_Arabic(t *testing.T) {
	// Arabic letters have no case; base letters are IsLetter=true
	cases := []struct{ in, want string }{
		{"مرحبا", "مرحبا"},
		{"بالعالم", "بالعالم"},
		{"موسيقى", "موسيقى"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_Hebrew(t *testing.T) {
	cases := []struct{ in, want string }{
		{"שלום", "שלום"},
		{"עולם", "עולם"},
		{"מוזיקה", "מוזיקה"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_Korean(t *testing.T) {
	cases := []struct{ in, want string }{
		{"안녕하세요", "안녕하세요"},
		{"한국어", "한국어"},
		{"음악", "음악"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_Thai(t *testing.T) {
	// Thai vowel marks (U+0E31, U+0E35 etc.) are IsMark=true; must be preserved.
	cases := []struct{ in, want string }{
		{"สวัสดี", "สวัสดี"},
		{"ชาวโลก", "ชาวโลก"},
		{"ดนตรี", "ดนตรี"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_Devanagari(t *testing.T) {
	// Devanagari virama (U+094D) and vowel signs (U+0947 etc.) are IsMark=true.
	cases := []struct{ in, want string }{
		{"नमस्ते", "नमस्ते"},
		{"दुनिया", "दुनिया"},
		{"संगीत", "संगीत"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_EmojiStripped(t *testing.T) {
	// Emoji are not letters, digits, or marks — must be stripped.
	cases := []struct{ in, want string }{
		{"hello👋", "hello"},
		{"🎵music", "music"},
		{"👍", ""},
		{"café🎶", "café"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_UnicodePunctStripped(t *testing.T) {
	// Unicode punctuation (not letter/digit/mark) must be stripped without splitting.
	cases := []struct{ in, want string }{
		{"héllo·world", "hélloworld"}, // U+00B7 MIDDLE DOT
		{"café—latte", "cafélatte"},   // U+2014 EM DASH
		{"rock'n'roll", "rocknroll"}, // U+2019 RIGHT SINGLE QUOTATION MARK
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_MixedScripts(t *testing.T) {
	cases := []struct{ in, want string }{
		{"café123", "café123"},
		{"Hello世界", "hello世界"},
		{"ABC日本語", "abc日本語"},
		{"rock音楽", "rock音楽"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAlphaNumericLower_UnicodeDigits(t *testing.T) {
	// Arabic-Indic digits (U+0660–U+0669) are IsDigit=true and must be preserved.
	cases := []struct{ in, want string }{
		{"١٢٣", "١٢٣"},
		{"song٤٢", "song٤٢"},
	}
	for _, c := range cases {
		if got := alphaNumericLower(c.in); got != c.want {
			t.Errorf("alphaNumericLower(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tokenize — multi-language integration tests
// ---------------------------------------------------------------------------

func TestTokenize_Russian(t *testing.T) {
	got := Tokenize("Привет Мир")
	want := []string{"привет", "мир"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Greek(t *testing.T) {
	got := Tokenize("Καλημέρα Κόσμε")
	want := []string{"καλημέρα", "κόσμε"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_German(t *testing.T) {
	got := Tokenize("über Straße MÜLLER")
	want := []string{"über", "straße", "müller"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Turkish(t *testing.T) {
	got := Tokenize("İstanbul Şehir")
	want := []string{"istanbul", "şehir"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Arabic(t *testing.T) {
	got := Tokenize("مرحبا بالعالم")
	want := []string{"مرحبا", "بالعالم"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Hebrew(t *testing.T) {
	got := Tokenize("שלום עולם")
	want := []string{"שלום", "עולם"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Korean(t *testing.T) {
	got := Tokenize("안녕하세요 한국어")
	want := []string{"안녕하세요", "한국어"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Thai(t *testing.T) {
	got := Tokenize("สวัสดี ชาวโลก")
	want := []string{"สวัสดี", "ชาวโลก"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_Devanagari(t *testing.T) {
	got := Tokenize("नमस्ते दुनिया")
	want := []string{"नमस्ते", "दुनिया"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_MixedLanguages(t *testing.T) {
	got := Tokenize("hello мир 世界")
	want := []string{"hello", "мир", "世界"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_EmojiStripped(t *testing.T) {
	got := Tokenize("hello 👋 world 🎵")
	want := []string{"hello", "world"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_UnicodeDigits(t *testing.T) {
	got := Tokenize("١٢٣")
	want := []string{"١٢٣"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_UnicodePunctSplits(t *testing.T) {
	// Comma/exclamation split tokens; Unicode letters in each part are preserved.
	got := Tokenize("café, monde!")
	want := []string{"café", "monde"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_NordicChars(t *testing.T) {
	got := Tokenize("Ångström Ørsted Ñoño")
	want := []string{"ångström", "ørsted", "ñoño"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTokenize_FastPathASCII(t *testing.T) {
	// Pure lowercase ASCII takes the zero-allocation fast path.
	got := Tokenize("hello world foo bar")
	want := []string{"hello", "world", "foo", "bar"}
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
