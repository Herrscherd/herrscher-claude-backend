package claude

import (
	"strings"
	"testing"
)

func TestTruncateRunesKeepsItsContractAtTheEdges(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{name: "empty text", text: "", limit: 3, want: ""},
		{name: "exactly the limit", text: "abc", limit: 3, want: "abc"},
		{name: "one rune over", text: "abcd", limit: 3, want: "ab…"},
		{name: "multibyte kept whole", text: "éé中😀", limit: 3, want: "éé…"},
		{name: "single multibyte rune at limit one", text: "😀", limit: 1, want: "😀"},
		{name: "two runes at limit one", text: "éa", limit: 1, want: "…"},
		{name: "limit zero", text: "a", limit: 0, want: "…"},
		{name: "long multibyte text", text: strings.Repeat("中", 50), limit: 10, want: strings.Repeat("中", 9) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateRunes(tc.text, tc.limit); got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.text, tc.limit, got, tc.want)
			}
		})
	}
}
