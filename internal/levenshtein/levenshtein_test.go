package levenshtein

import (
	"fmt"
	"testing"
)

func TestDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"Иванов", "Иваноы", 1},
	}
	for _, c := range cases {
		if got := Distance(c.a, c.b); got != c.want {
			t.Errorf("Distance(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestScoreBatchParallel exercises the parallel path (>50 candidates) and
// checks it agrees with the sequential computation.
func TestScoreBatchParallel(t *testing.T) {
	const n = ParallelThreshold + 25
	cands := make([][]string, n)
	for i := range cands {
		cands[i] = []string{fmt.Sprintf("name%03d", i)}
	}
	got := ScoreBatch("name042", cands)
	for i := range cands {
		want := BestScore("name042", cands[i])
		if got[i] != want {
			t.Fatalf("candidate %d: got %v want %v", i, got[i], want)
		}
	}
	if got[42] != 1.0 {
		t.Fatalf("exact match should have score 1.0, got %v", got[42])
	}
}
