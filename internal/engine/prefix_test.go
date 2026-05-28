package engine

import "testing"

func TestPrefixLimitForQuery(t *testing.T) {
	tests := []struct {
		prefix string
		want   int
	}{
		{"m", 100},
		{"ir", 100},
		{"iro", 60},
		{"maide", 60},
		{"maiden", 50},
	}

	for _, tt := range tests {
		if got := prefixLimitForQuery(tt.prefix); got != tt.want {
			t.Fatalf("prefixLimitForQuery(%q) = %d, want %d", tt.prefix, got, tt.want)
		}
	}
}
