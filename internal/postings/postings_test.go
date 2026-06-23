package postings

import (
	"reflect"
	"testing"
)

func collect(l *List) []uint64 {
	var ids []uint64
	l.ForEach(func(id uint64) { ids = append(ids, id) })
	return ids
}

func TestSparseRoundTrip(t *testing.T) {
	l := New()
	for _, id := range []uint64{5, 1, 9, 1, 3} { // includes duplicate
		l.Add(id)
	}
	if l.Dense() {
		t.Fatal("small list should stay sparse")
	}
	got, err := Unmarshal(l.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{1, 3, 5, 9}; !reflect.DeepEqual(collect(got), want) {
		t.Fatalf("got %v want %v", collect(got), want)
	}
}

func TestPromotionToDense(t *testing.T) {
	l := New()
	for i := uint64(0); i <= DenseThreshold; i++ {
		l.Add(i * 2)
	}
	if !l.Dense() {
		t.Fatal("large list should promote to dense bitset")
	}
	got, err := Unmarshal(l.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dense() {
		t.Fatal("dense flag lost after round-trip")
	}
	if got.Count() != l.Count() {
		t.Fatalf("count mismatch: %d vs %d", got.Count(), l.Count())
	}
	if !got.Contains(2000) || got.Contains(2001) {
		t.Fatal("dense membership wrong after round-trip")
	}
}
