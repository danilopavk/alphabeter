// Package game holds the learning rules: which symbol to show next, how answers
// change scores, and when new symbols unlock. It is pure — no I/O, no clock;
// randomness is passed in.
//
// All scores and recencies are integers in tenths (score 100.0 = 1000,
// recency 0.5 = 5) so the arithmetic is exact.
package game

import (
	"math"
	"strings"
)

// Symbol is a letter or digit by its stable index: A–Z = 0–25, 0–9 = 26–35.
// The index is part of stored data and must never change.
type Symbol uint8

const NumSymbols = 36

const symbolChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func (s Symbol) String() string {
	if int(s) >= NumSymbols {
		return "?"
	}
	return symbolChars[s : s+1]
}

// ParseSymbol parses a single uppercase letter or digit.
func ParseSymbol(s string) (Symbol, bool) {
	if len(s) != 1 {
		return 0, false
	}
	i := strings.IndexByte(symbolChars, s[0])
	return Symbol(i), i >= 0
}

const (
	StartScore  = 1000 // 100.0
	UnlockScore = 1300 // next batch unlocks when every unlocked symbol is above 130.0
	// RetireScore: a right answer that leaves a symbol at 160.0 or more takes it
	// out of rotation until the next batch unlocks (in the last batch, until
	// every symbol is retired and the game is completed).
	RetireScore = 1600
	// When a batch unlocks, scores restart between ResetLow (for scores at or
	// below UnlockScore) and ResetHigh (at or above RetireScore), linearly in between.
	ResetLow     = 1000 // 100.0
	ResetHigh    = 1200 // 120.0
	StartRecency = 5    // 0.5
	MaxRecency   = 10   // 1.0
	// NeutralGap is the gap (positions between two showings) that leaves recency
	// unchanged. Each position under it subtracts 0.1; each full GapStep over it
	// adds 0.1. Because of RecentWindow the gap is never below 5.
	NeutralGap = 5
	GapStep    = 5
	// RecentWindow is how many of the most recently shown symbols are never picked.
	RecentWindow = 4
)

// Batches are unlocked in order.
var Batches = [][]Symbol{
	mustSymbols("AIKMNP012"),
	mustSymbols("ERT345"),
	mustSymbols("BDGLOS67"),
	mustSymbols("CFHJQUVWXYZ89"),
}

func mustSymbols(chars string) []Symbol {
	out := make([]Symbol, len(chars))
	for i := range chars {
		s, ok := ParseSymbol(chars[i : i+1])
		if !ok {
			panic("game: bad symbol " + chars[i:i+1])
		}
		out[i] = s
	}
	return out
}

// Rand is the randomness Next needs; *math/rand/v2.Rand satisfies it.
type Rand interface {
	IntN(n int) int
}

type SymbolState struct {
	Score   uint32 // tenths, never below 0
	Recency uint8  // tenths, 0..MaxRecency
	Streak  int16  // +n: n right in a row, −n: n wrong in a row
	LastSeq uint32 // Seq of the last answer to this symbol; 0 = never shown
	Retired bool   // reached RetireScore; only shown as filler until the next batch
}

type State struct {
	Seq     uint32 // number of answers so far
	Batch   uint8  // number of unlocked batches, 1..len(Batches)
	Current Symbol // the symbol on screen, waiting for an answer
	// Completed: every symbol of the last batch was retired. From then on the
	// kid keeps playing with all symbols, without retiring or resets.
	Completed bool
	Symbols   [NumSymbols]SymbolState
}

// NewState is a kid's state before the first answer.
func NewState(rng Rand) State {
	s := initialState()
	s.Current = s.Next(rng)
	return s
}

func initialState() State {
	s := State{Batch: 1}
	for i := range s.Symbols {
		s.Symbols[i] = SymbolState{Score: StartScore, Recency: StartRecency}
	}
	return s
}

// IsUnlocked reports whether sym belongs to an unlocked batch.
func (s State) IsUnlocked(sym Symbol) bool {
	for _, batch := range Batches[:s.Batch] {
		for _, x := range batch {
			if x == sym {
				return true
			}
		}
	}
	return false
}

// Answer records whether the kid named the current symbol and picks the next one.
func (s State) Answer(correct bool, rng Rand) State {
	next, _ := s.AnswerWithOutcome(correct, rng)
	return next
}

// Outcome is what one answer did, for showing it to the parent.
type Outcome struct {
	Symbol    Symbol
	Correct   bool
	Before    uint32 // score before the answer
	After     uint32 // score right after the answer, before a level-up reset
	Retired   bool   // the answer retired the symbol
	LevelUp   bool   // the answer unlocked the next batch (and reset the scores)
	Completed bool   // the answer completed the game
}

// AnswerWithOutcome is Answer that also reports what the answer did.
func (s State) AnswerWithOutcome(correct bool, rng Rand) (State, Outcome) {
	o := s.apply(s.Current, correct)
	s.Current = s.Next(rng)
	return s, o
}

// apply is Answer without picking the next symbol; replay uses it directly.
func (s *State) apply(sym Symbol, correct bool) Outcome {
	s.Seq++
	st := &s.Symbols[sym]
	o := Outcome{Symbol: sym, Correct: correct, Before: st.Score}
	if st.LastSeq != 0 {
		st.Recency = nextRecency(st.Recency, s.Seq-st.LastSeq)
	}
	if correct {
		if st.Streak < 0 {
			st.Streak = 0
		}
		if st.Streak < math.MaxInt16 {
			st.Streak++
		}
		gain := uint32(st.Recency) * uint32(st.Streak)
		if st.Score > math.MaxUint32-gain {
			st.Score = math.MaxUint32
		} else {
			st.Score += gain
		}
	} else {
		if st.Streak > 0 {
			st.Streak = 0
		}
		if st.Streak > math.MinInt16+1 {
			st.Streak--
		}
		penalty := uint32(st.Recency) * uint32(-int32(st.Streak))
		if penalty >= st.Score {
			st.Score = 0
		} else {
			st.Score -= penalty
		}
	}
	st.LastSeq = s.Seq
	o.After = st.Score
	if s.Completed {
		return o
	}
	wasRetired := st.Retired
	st.Retired = correct && st.Score >= RetireScore
	o.Retired = st.Retired && !wasRetired
	o.LevelUp = s.maybeUnlock()
	o.Completed = s.maybeComplete()
	return o
}

func nextRecency(recency uint8, gap uint32) uint8 {
	r := int64(recency)
	g := int64(gap)
	if g < NeutralGap {
		r -= NeutralGap - g
	} else {
		r += (g - NeutralGap) / GapStep
	}
	return uint8(min(max(r, 0), MaxRecency))
}

func (s *State) maybeUnlock() bool {
	if int(s.Batch) >= len(Batches) {
		return false
	}
	for _, batch := range Batches[:s.Batch] {
		for _, x := range batch {
			if s.Symbols[x].Score <= UnlockScore {
				return false
			}
		}
	}
	// A new level restarts every score between 100 and 120 and brings retired
	// symbols back. Streaks and recency carry over, so known symbols climb back quickly.
	s.Batch++
	for i := range s.Symbols {
		s.Symbols[i].Score = resetScore(s.Symbols[i].Score)
		s.Symbols[i].Retired = false
	}
	return true
}

func resetScore(score uint32) uint32 {
	switch {
	case score <= UnlockScore:
		return ResetLow
	case score >= RetireScore:
		return ResetHigh
	}
	return ResetLow + (score-UnlockScore)*(ResetHigh-ResetLow)/(RetireScore-UnlockScore)
}

func (s *State) maybeComplete() bool {
	if int(s.Batch) < len(Batches) {
		return false
	}
	for _, st := range s.Symbols {
		if !st.Retired {
			return false
		}
	}
	s.Completed = true
	for i := range s.Symbols {
		s.Symbols[i].Retired = false
	}
	return true
}

// Next picks the unlocked symbol with the lowest score among those not shown in
// the last RecentWindow answers, skipping retired symbols. Only when every
// symbol still in rotation was shown recently is a retired one picked as filler.
// Ties are broken randomly so the order can't be memorised.
func (s State) Next(rng Rand) Symbol {
	if x, ok := s.lowest(rng, false); ok {
		return x
	}
	if x, ok := s.lowest(rng, true); ok {
		return x
	}
	// Unreachable while the first batch is larger than RecentWindow.
	panic("game: no symbol eligible")
}

func (s State) lowest(rng Rand, retired bool) (Symbol, bool) {
	var candidates []Symbol
	best := uint32(math.MaxUint32)
	for _, batch := range Batches[:s.Batch] {
		for _, x := range batch {
			st := s.Symbols[x]
			if st.Retired != retired || st.LastSeq != 0 && s.Seq-st.LastSeq < RecentWindow {
				continue
			}
			switch {
			case st.Score < best:
				best = st.Score
				candidates = append(candidates[:0], x)
			case st.Score == best:
				candidates = append(candidates, x)
			}
		}
	}
	if len(candidates) == 0 {
		return 0, false
	}
	return candidates[rng.IntN(len(candidates))], true
}
