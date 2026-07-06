package tui

import (
	"testing"
)

func TestMatchesFilter(t *testing.T) {
	tests := []struct {
		name   string
		cells  []string
		filter string
		want   bool
	}{
		{"exact", []string{"pod-abc", "default"}, "pod-abc", true},
		{"substring", []string{"pod-abc", "default"}, "pod", true},
		{"case", []string{"Pod-Abc", "Default"}, "pod", true},
		{"no match", []string{"svc-xyz", "default"}, "pod", false},
		{"empty filter", []string{"any"}, "", true},
		{"empty cells", []string{}, "pod", false},
		{"multiple cells", []string{"name", "ns", "ready"}, "ns", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesFilter(tt.cells, tt.filter); got != tt.want {
				t.Errorf("matchesFilter(%v, %q) = %v, want %v", tt.cells, tt.filter, got, tt.want)
			}
		})
	}
}

func TestColumnWidth(t *testing.T) {
	tests := []struct {
		cols []string
		ci   int
		want int
	}{
		{[]string{"NAME"}, 0, 40},
		{[]string{"NAMESPACE"}, 0, 20},
		{[]string{"KIND"}, 0, 20},
		{[]string{"AGE"}, 0, 8},
		{[]string{"STATUS"}, 0, 20},
		{[]string{"UNKNOWN"}, 0, 15},
		{[]string{"a", "b"}, -1, 10},
		{[]string{"a", "b"}, 2, 10},
	}

	for _, tt := range tests {
		t.Run(tt.cols[0], func(t *testing.T) {
			if got := columnWidth(tt.cols, tt.ci); got != tt.want {
				t.Errorf("columnWidth(%v, %d) = %d, want %d", tt.cols, tt.ci, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"hello world", 10, "hello wo.."},
		{"ab", 3, "ab"},
		{"abcdef", 3, "abc"},
		{"ab", 2, "ab"},
		// max <= 3, no ".." suffix
		{"abcdef", 3, "abc"},
		{"abcdef", 2, "ab"},
		{"abcdef", 1, "a"},
	}

	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			if got := truncate(tt.s, tt.max); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
		})
	}
}
