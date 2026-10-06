package traffic

import (
	"math"
	"math/bits"
)

const (
	// sketchPrecision is how many hash bits pick a register. 14 gives 16,384
	// one-byte registers and a standard error of 1.04/sqrt(16384), about 0.8 %.
	sketchPrecision = 14

	sketchRegisters = 1 << sketchPrecision
)

// Sketch is a HyperLogLog counter: it estimates how many distinct values it has
// been shown without keeping any of them.
//
// Among n distinct uniformly random hashes, the longest run of leading zero
// bits is about log2(n) — a hash that starts with twenty zeros turns up roughly
// once in a million. One such observation is far too noisy to use, so the first
// 14 bits of each hash route it to one of 16,384 registers, each register keeps
// the longest run it has seen, and the estimate is a harmonic mean across all
// of them.
//
// What that buys here: a fixed 16 KB whether it has seen ten addresses or ten
// million, repeats that cost nothing, and sketches that merge by taking the
// larger register — which is what lets 25 hourly sketches answer "how many in
// the last day". A register holds a number below 52, so no address can be read
// back out of one.
//
// The zero value is an empty sketch. It is not safe for concurrent use.
type Sketch struct {
	registers [sketchRegisters]uint8
}

// Add records one value by its 64-bit hash. The hash has to be uniform: the
// estimate is only as good as the bits it is given.
func (s *Sketch) Add(hash uint64) {
	index := hash >> (64 - sketchPrecision)

	// The remaining 50 bits, left-aligned, with a guard bit below them so a
	// run of zeros cannot be counted past the bits that actually exist.
	rest := hash<<sketchPrecision | 1<<(sketchPrecision-1)
	rank := uint8(bits.LeadingZeros64(rest) + 1)

	if rank > s.registers[index] {
		s.registers[index] = rank
	}
}

// Merge folds another sketch into this one, leaving it as if it had been shown
// everything either of them was.
func (s *Sketch) Merge(other *Sketch) {
	for i, rank := range other.registers {
		if rank > s.registers[i] {
			s.registers[i] = rank
		}
	}
}

// Count estimates how many distinct values were added.
func (s *Sketch) Count() int64 {
	const m = float64(sketchRegisters)

	sum := 0.0
	empty := 0
	for _, rank := range s.registers {
		sum += math.Ldexp(1, -int(rank))
		if rank == 0 {
			empty++
		}
	}

	alpha := 0.7213 / (1 + 1.079/m)
	estimate := alpha * m * m / sum

	// The harmonic mean overestimates while most registers are still empty.
	// Counting the empty ones instead is near-exact in that range, which is
	// where a small server spends its whole life.
	if estimate <= 2.5*m && empty > 0 {
		estimate = m * math.Log(m/float64(empty))
	}

	return int64(estimate + 0.5)
}

// Reset empties the sketch.
func (s *Sketch) Reset() {
	s.registers = [sketchRegisters]uint8{}
}
