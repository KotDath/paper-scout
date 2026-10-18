package retrieval

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"

	"paper-scout/internal/embeddings"
)

func inputHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func vectorBytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

// Check persisted bytes too: an incompatible/corrupt cache is never compared.
func readVector(b []byte) ([]float32, bool) {
	if len(b) != 4*embeddings.Dimensions {
		return nil, false
	}
	v := make([]float32, embeddings.Dimensions)
	norm := 0.0
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		x := float64(v[i])
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, false
		}
		norm += x * x
	}
	return v, math.Abs(norm-1) < 1e-4
}
