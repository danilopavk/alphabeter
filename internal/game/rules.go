// Package game holds the learning rules: which symbol to show next, how answers
// change scores, and when the next symbol joins. It is pure — no I/O, no clock;
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
	StartScore   = 1000 // 100.0
	StartRecency = 5    // 0.5
	MaxRecency   = 10   // 1.0
	// NeutralGap is the gap (positions between two showings) that leaves recency
	// unchanged. Each position under it subtracts 0.1; each full GapStep over it
	// adds 0.1. Because of RecentWindow the gap is never below 5.
	NeutralGap = 5
	GapStep    = 5
	// RecentWindow is how many of the most recently shown symbols are never picked.
	RecentWindow = 4
	// HalfLife: every 10.0 points a symbol scores above the lowest eligible one
	// halves its chance of being picked.
	HalfLife = 100
	// JoinGap: a new symbol joins 1.0 under the lowest score in play.
	JoinGap = 10
)

// Order is the order symbols come into play: the first StartCount from the
// start, then one at a time. It is part of stored data (State.InPlay counts a
// prefix of it) and must never change.
var Order = mustSymbols("AIKMNP012" + "ERT345" + "BDGLOS67" + "CFHJUV89QWXYZ")

const StartCount = 9

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
}

type State struct {
	Seq     uint32 // number of answers so far
	InPlay  uint8  // the first InPlay symbols of Order are in play, StartCount..NumSymbols
	Current Symbol // the symbol on screen, waiting for an answer
	Symbols [NumSymbols]SymbolState
}

// NewState is a kid's state before the first answer.
func NewState(rng Rand) State {
	s := initialState()
	s.Current = s.Next(rng)
	return s
}

func initialState() State {
	s := State{InPlay: StartCount}
	for i := range s.Symbols {
		s.Symbols[i] = SymbolState{Score: StartScore, Recency: StartRecency}
	}
	return s
}

// IsInPlay reports whether sym has joined.
func (s State) IsInPlay(sym Symbol) bool {
	for _, x := range Order[:s.InPlay] {
		if x == sym {
			return true
		}
	}
	return false
}

// Newest is the symbol that joined last.
func (s State) Newest() Symbol { return Order[s.InPlay-1] }

// Bars are what the scores in play must reach before the next symbol joins:
// at least one score of Top, an average of Avg and no score under Low.
type Bars struct{ Top, Avg, Low uint32 }

// FirstBars are for the first symbol to join after the start, LastBars for the
// last. In between Top and Avg follow a square root, rising fast at first the
// way scores do, and Low a straight line.
var (
	FirstBars = Bars{Top: 1200, Avg: 1100, Low: 1000}
	LastBars  = Bars{Top: 2500, Avg: 2000, Low: 1200}
)

// barsFor returns the bars for the k-th symbol to join, 1..NumSymbols−StartCount.
func barsFor(k int) Bars {
	steps := uint64(NumSymbols - StartCount - 1)
	step := uint64(k - 1)
	curve := func(from, to uint32) uint32 {
		d := uint64(to - from)
		return from + uint32(math.Sqrt(float64(d*d*step/steps)))
	}
	return Bars{
		Top: curve(FirstBars.Top, LastBars.Top),
		Avg: curve(FirstBars.Avg, LastBars.Avg),
		Low: FirstBars.Low + uint32(uint64(LastBars.Low-FirstBars.Low)*step/steps),
	}
}

// NextBars returns the bars for the next symbol to join; false once all are in play.
func (s State) NextBars() (Bars, bool) {
	if s.InPlay >= NumSymbols {
		return Bars{}, false
	}
	return barsFor(int(s.InPlay) - StartCount + 1), true
}

// Standing is the top, average (rounded down) and lowest score in play.
type Standing struct{ Top, Avg, Low uint32 }

func (s State) Standing() Standing {
	st := Standing{Low: math.MaxUint32}
	var sum uint64
	for _, x := range Order[:s.InPlay] {
		score := s.Symbols[x].Score
		st.Top, st.Low = max(st.Top, score), min(st.Low, score)
		sum += uint64(score)
	}
	st.Avg = uint32(sum / uint64(s.InPlay))
	return st
}

// Reaches reports whether the standing clears all three bars.
func (st Standing) Reaches(b Bars) bool {
	return st.Top >= b.Top && st.Avg >= b.Avg && st.Low >= b.Low
}

// Answer records whether the kid named the current symbol and picks the next one.
func (s State) Answer(correct bool, rng Rand) State {
	next, _ := s.AnswerWithOutcome(correct, rng)
	return next
}

// Outcome is what one answer did, for showing it to the parent.
type Outcome struct {
	Symbol  Symbol
	Correct bool
	Before  uint32 // score before the answer
	After   uint32 // score after the answer
	Added   bool   // the answer brought the next symbol into play (State.Newest)
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
	o.Added = s.maybeAdd()
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

// maybeAdd brings the next symbol into play once the scores reach its bars.
// Symbols never leave.
func (s *State) maybeAdd() bool {
	bars, ok := s.NextBars()
	if !ok {
		return false
	}
	standing := s.Standing()
	if !standing.Reaches(bars) {
		return false
	}
	s.InPlay++
	// It joins a point under the lowest score, and under the next Low bar, so it
	// needs right answers of its own before another symbol can follow. Both are
	// at least StartScore, so this can't go below zero.
	start := standing.Low
	if next, ok := s.NextBars(); ok {
		start = min(start, next.Low)
	}
	s.Symbols[s.Newest()] = SymbolState{Score: start - JoinGap, Recency: StartRecency}
	return true
}

// weightBits scales pick weights to integers: the lowest eligible score gets
// 2^weightBits per answer waited.
const weightBits = 16

// Next picks the symbol to show. A symbol that just joined comes first.
// Otherwise it is a weighted draw among the symbols in play not shown in the
// last RecentWindow answers: the weight halves for every HalfLife a score is
// above the lowest of them, and grows with the answers since the symbol became
// eligible, so weak symbols come up most and strong ones still come back.
func (s State) Next(rng Rand) Symbol {
	if s.InPlay > StartCount && s.Symbols[s.Newest()].LastSeq == 0 {
		return s.Newest()
	}
	var eligible []Symbol
	low := uint32(math.MaxUint32)
	for _, x := range Order[:s.InPlay] {
		st := s.Symbols[x]
		if st.LastSeq != 0 && s.Seq-st.LastSeq < RecentWindow {
			continue
		}
		eligible = append(eligible, x)
		low = min(low, st.Score)
	}
	weights := make([]uint64, len(eligible))
	var total uint64
	for i, x := range eligible {
		st := s.Symbols[x]
		waited := uint64(s.Seq) + 1 // never shown: waiting since the start
		if st.LastSeq != 0 {
			waited = uint64(s.Seq-st.LastSeq-RecentWindow) + 1
		}
		weights[i] = uint64(float64(waited) * math.Exp2(weightBits-float64(st.Score-low)/HalfLife))
		total += weights[i]
	}
	if total == 0 {
		// Unreachable while StartCount is larger than RecentWindow.
		panic("game: no symbol eligible")
	}
	r := uint64(rng.IntN(int(total)))
	for i, w := range weights {
		if r < w {
			return eligible[i]
		}
		r -= w
	}
	panic("game: weighted draw out of range")
}
