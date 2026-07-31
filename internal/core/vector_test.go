package core

import (
	"math"
	"testing"
)

func TestEncodeDecodeVectorRoundTrip(t *testing.T) {
	in := []float32{0.5, -1.25, 3.75, 0, 42}
	b := EncodeVector(in)
	if len(b) != 20 {
		t.Fatalf("encoded length = %d, want 20", len(b))
	}
	out, err := DecodeVector(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if in[i] != out[i] {
			t.Fatalf("round trip[%d] = %v, want %v", i, out[i], in[i])
		}
	}
}

func TestDecodeVectorRejectsRaggedBlob(t *testing.T) {
	if _, err := DecodeVector(make([]byte, 7)); err == nil {
		t.Fatal("a 7-byte blob decoded without error; corrupt rows must be loud")
	}
}

func TestNormalize(t *testing.T) {
	v, err := Normalize([]float32{3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("normalize(3,4) = %v, want (0.6, 0.8)", v)
	}
	if _, err := Normalize([]float32{0, 0, 0}); err == nil {
		t.Fatal("zero vector normalized without error; NaNs would poison every dot product")
	}
}

func TestDotHandComputed(t *testing.T) {
	// Length 6 exercises both the unrolled body (4) and the tail (2).
	a := []float32{1, 2, 3, 4, 5, 6}
	b := []float32{6, 5, 4, 3, 2, 1}
	got, err := Dot(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if got != 56 {
		t.Fatalf("dot = %v, want 56", got)
	}
	if _, err := Dot(a, a[:3]); err == nil {
		t.Fatal("dimension mismatch returned a number instead of an error")
	}
}

func TestTopKOrderAndBound(t *testing.T) {
	q := []float32{1, 0}
	cands := map[int64][]float32{
		1: {0.9, 0.1},
		2: {0.5, 0.5},
		3: {0.99, 0.01},
		4: {0.1, 0.9},
	}
	got := TopK(q, cands, 2)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 1 {
		t.Fatalf("top-2 = %+v, want ids [3 1]", got)
	}
}

func TestTopKTiesBreakTowardLowerID(t *testing.T) {
	q := []float32{1}
	cands := map[int64][]float32{5: {0.7}, 2: {0.7}, 9: {0.7}}
	got := TopK(q, cands, 2)
	if got[0].ID != 2 || got[1].ID != 5 {
		t.Fatalf("tied top-2 = %+v, want ids [2 5] (deterministic order is what graphContext's tests rely on)", got)
	}
}

func TestTopKSkipsMismatchedDims(t *testing.T) {
	q := []float32{1, 0}
	cands := map[int64][]float32{
		1: {0.9, 0.1},
		2: {0.9},       // stale vector from an older dims config
		3: {0.9, 0, 0}, // ditto, longer
	}
	got := TopK(q, cands, 3)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("got %+v, want only id 1 — mismatched dims are skipped, not errors, so a re-embed in progress keeps answering", got)
	}
}

func TestTopKEmptyAndZeroK(t *testing.T) {
	if got := TopK([]float32{1}, nil, 5); got != nil {
		t.Fatalf("empty candidates returned %+v, want nil", got)
	}
	if got := TopK([]float32{1}, map[int64][]float32{1: {1}}, 0); got != nil {
		t.Fatalf("k=0 returned %+v, want nil", got)
	}
}
