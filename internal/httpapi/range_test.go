package httpapi

import "testing"

func TestParseRange(t *testing.T) {
	tests := []struct {
		header           string
		size, start, end int64
		partial, valid   bool
	}{
		{"", 100, 0, 99, false, true},
		{"bytes=10-19", 100, 10, 19, true, true},
		{"bytes=90-", 100, 90, 99, true, true},
		{"bytes=-10", 100, 90, 99, true, true},
		{"bytes=100-", 100, 0, 0, false, false},
	}
	for _, tt := range tests {
		start, end, partial, err := parseRange(tt.header, tt.size)
		if (err == nil) != tt.valid {
			t.Fatalf("%q validity: %v", tt.header, err)
		}
		if tt.valid && (start != tt.start || end != tt.end || partial != tt.partial) {
			t.Fatalf("%q = %d-%d partial=%v", tt.header, start, end, partial)
		}
	}
}
