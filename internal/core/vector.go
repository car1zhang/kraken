package core

import (
	"encoding/binary"
	"fmt"
	"math"
)

// vector.go — brute-force cosine search over embedding slabs, pure functions.
//
// The spec's Q1 decision: no vector extension. Embeddings are little-endian
// float32 BLOBs in kraken.db, decoded into contiguous slices and scanned
// linearly. Vectors are normalized ON WRITE so cosine is a plain dot
// product, and the dot is 4x-unrolled with a bounded top-K selection instead
// of a full sort — both taken from the spec's research round
// (artifacts/research/sqlite-vec-from-go.md), whose benchmarks on this
// machine put a brute-force scan at 1.29 ms for 10k×384 and 13.1 ms for
// 100k×384; the 1024 dims this deployment configures scale those roughly
// linearly. The seam to an ANN index, if the corpus ever grows 10x past
// that, is TopK alone.

// EncodeVector packs a float32 vector into the little-endian BLOB shape the
// kg_* embedding columns store.
func EncodeVector(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(f))
	}
	return out
}

// DecodeVector unpacks a BLOB written by EncodeVector. A length that is not
// a multiple of 4 is a corrupt row, not a shorter vector — error, never a
// silently truncated result.
func DecodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector blob length %d is not a multiple of 4", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out, nil
}

// Normalize scales v to unit length in place and returns it. A zero vector
// cannot be normalized — error rather than NaNs that poison every later dot
// product.
func Normalize(v []float32) ([]float32, error) {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return nil, fmt.Errorf("cannot normalize a zero vector")
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return v, nil
}

// Dot is the 4x-unrolled inner product. Both inputs must be the same length;
// the mismatch is the caller's bug and errors rather than reading garbage.
// Over pre-normalized vectors this IS cosine similarity.
func Dot(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("dot: dimension mismatch %d vs %d", len(a), len(b))
	}
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3, nil
}

// Scored is one TopK result: the caller's id for a candidate vector and its
// similarity to the query.
type Scored struct {
	ID    int64
	Score float32
}

// TopK scans candidates (id -> vector) against a query vector and returns
// the k best by dot product, best first. Bounded selection, never a full
// sort: results are kept in a small ordered slice and each candidate does one
// comparison against the current floor in the common case. Ties break toward
// the lower id, which is what makes the result deterministic for a fixed
// input despite map iteration order — pinned by
// TestTopKTiesBreakTowardLowerID in this package.
//
// Candidates whose dimension does not match the query are SKIPPED, not
// errors: a model/dims config change mid-corpus leaves old vectors behind,
// and a search must keep answering from the vectors that do match while
// graphPass re-embeds the rest.
func TopK(query []float32, candidates map[int64][]float32, k int) []Scored {
	if k <= 0 || len(candidates) == 0 {
		return nil
	}
	best := make([]Scored, 0, k)
	for id, v := range candidates {
		if len(v) != len(query) {
			continue
		}
		var score float32
		// Inline of Dot's loop; the mismatch case is already excluded above,
		// so Dot's error return would be dead weight here.
		var s0, s1, s2, s3 float32
		i := 0
		for ; i+4 <= len(query); i += 4 {
			s0 += query[i] * v[i]
			s1 += query[i+1] * v[i+1]
			s2 += query[i+2] * v[i+2]
			s3 += query[i+3] * v[i+3]
		}
		for ; i < len(query); i++ {
			s0 += query[i] * v[i]
		}
		score = s0 + s1 + s2 + s3

		// A NaN score fails BOTH floor comparisons below (every comparison
		// with NaN is false), so without this guard one corrupt stored
		// vector — a torn write decodes to NaN without any length error —
		// takes the TOP result slot of every search, at a position that
		// depends on map iteration order (review finding, reproduced: a
		// poisoned row ranked above an exact match). Skip it; the row costs
		// one candidate of recall until re-embedded, nothing more.
		if math.IsNaN(float64(score)) {
			continue
		}

		if len(best) == k {
			floor := best[k-1]
			if score < floor.Score || (score == floor.Score && id > floor.ID) {
				continue
			}
		}
		// Insert in order (descending score, ascending id on ties).
		pos := len(best)
		for pos > 0 {
			p := best[pos-1]
			if p.Score > score || (p.Score == score && p.ID < id) {
				break
			}
			pos--
		}
		if len(best) < k {
			best = append(best, Scored{})
		}
		copy(best[pos+1:], best[pos:])
		best[pos] = Scored{ID: id, Score: score}
	}
	return best
}
