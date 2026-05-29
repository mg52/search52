package engine

import "testing"

func TestPrefixLimitForQuery(t *testing.T) {
	tests := []struct {
		prefix string
		want   int
	}{
		{"m", 200},
		{"ir", 200},
		{"iro", 100},
		{"maide", 100},
		{"maiden", 80},
	}

	for _, tt := range tests {
		if got := prefixLimitForQuery(tt.prefix); got != tt.want {
			t.Fatalf("prefixLimitForQuery(%q) = %d, want %d", tt.prefix, got, tt.want)
		}
	}
}

func TestFuzzyLimitForQuery(t *testing.T) {
	tests := []struct {
		prefix string
		want   int
	}{
		{"m", 80},
		{"ir", 80},
		{"iro", 80},
		{"maide", 50},
		{"maidene", 30},
	}

	for _, tt := range tests {
		if got := fuzzyLimitForQuery(tt.prefix); got != tt.want {
			t.Fatalf("fuzzyLimitForQuery(%q) = %d, want %d", tt.prefix, got, tt.want)
		}
	}
}
