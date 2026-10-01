package game

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func seeded(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, 0)) }

func sym(t *testing.T, s string) Symbol {
	t.Helper()
	v, ok := ParseSymbol(s)
	if !ok {
		t.Fatalf("bad symbol %q", s)
	}
	return v
}

func TestSymbolIndexIsStable(t *testing.T) {
	cases := map[Symbol]string{0: "A", 25: "Z", 26: "0", 35: "9"}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Symbol(%d) = %q, want %q", s, got, want)
		}
		if back, ok := ParseSymbol(want); !ok || back != s {
			t.Errorf("ParseSymbol(%q) = %d, %v", want, back, ok)
		}
	}
	for _, bad := range []string{"a", "", "AB", "!"} {
		if _, ok := ParseSymbol(bad); ok {
			t.Errorf("ParseSymbol(%q) accepted", bad)
		}
	}
}

func TestOrderCoversEverySymbolOnce(t *testing.T) {
	var b strings.Builder
	seen := map[Symbol]bool{}
	for _, s := range Order {
		b.WriteString(s.String())
		if seen[s] {
			t.Errorf("%s appears twice", s)
		}
		seen[s] = true
	}
	if got, want := b.String(), "AIKMNP012ERT345BDGLOS67CFHJUV89QWXYZ"; got != want {
		t.Errorf("order %s, want %s", got, want)
	}
	if len(seen) != NumSymbols {
		t.Errorf("order covers %d symbols, want %d", len(seen), NumSymbols)
	}
	if StartCount != 9 || StartCount <= RecentWindow {
		t.Errorf("StartCount = %d: must be 9 and larger than the recent window", StartCount)
	}
}

func TestNewState(t *testing.T) {
	s := NewState(seeded(1))
	if s.Seq != 0 || s.InPlay != StartCount {
		t.Fatalf("seq=%d in play=%d", s.Seq, s.InPlay)
	}
	for i, st := range s.Symbols {
		if st != (SymbolState{Score: StartScore, Recency: StartRecency}) {
			t.Errorf("symbol %s starts as %+v", Symbol(i), st)
		}
	}
	if !s.IsInPlay(s.Current) || !s.IsInPlay(sym(t, "2")) || s.IsInPlay(sym(t, "E")) {
		t.Errorf("current %s; in play: 2 %v, E %v", s.Current, s.IsInPlay(sym(t, "2")), s.IsInPlay(sym(t, "E")))
	}
}

func TestNextRecency(t *testing.T) {
	cases := []struct {
		recency uint8
		gap     uint32
		want    uint8
	}{
		{5, 5, 5},  // neutral gap keeps it
		{5, 9, 5},  // not yet a full 5 over
		{5, 10, 6}, // +0.1 per full 5 over 5
		{5, 14, 6},
		{5, 15, 7},
		{5, 4, 4}, // −0.1 per step under 5
		{5, 1, 1},
		{2, 1, 0},   // clamped at 0
		{9, 30, 10}, // clamped at 1.0
		{5, 1 << 31, 10},
	}
	for _, c := range cases {
		if got := nextRecency(c.recency, c.gap); got != c.want {
			t.Errorf("nextRecency(%d, gap %d) = %d, want %d", c.recency, c.gap, got, c.want)
		}
	}
}

// stateWithCurrent returns a fresh state whose current symbol is forced to cur.
func stateWithCurrent(t *testing.T, cur string) State {
	s := NewState(seeded(1))
	s.Current = sym(t, cur)
	return s
}

func TestAnswerWithOutcome(t *testing.T) {
	t.Run("right answer", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		next, o := s.AnswerWithOutcome(true, seeded(1))
		want := Outcome{Symbol: sym(t, "A"), Correct: true, Before: 1000, After: 1005}
		if o != want {
			t.Errorf("outcome %+v, want %+v", o, want)
		}
		if next != s.Answer(true, seeded(1)) {
			t.Error("AnswerWithOutcome and Answer disagree")
		}
	})
	t.Run("wrong answer", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Symbols[sym(t, "A")].Score = 1070
		_, o := s.AnswerWithOutcome(false, seeded(1))
		if o.Correct || o.Before != 1070 || o.After != 1065 {
			t.Errorf("outcome %+v", o)
		}
	})
	t.Run("a new symbol joining", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		readyToAdd(&s)
		next, o := s.AnswerWithOutcome(true, seeded(1))
		if !o.Added || o.Before != 1200 || o.After != 1205 {
			t.Errorf("outcome %+v", o)
		}
		if next.InPlay != StartCount+1 || next.Newest() != sym(t, "E") || next.Current != sym(t, "E") {
			t.Errorf("in play %d, newest %s, next %s", next.InPlay, next.Newest(), next.Current)
		}
	})
}

func TestAnswerFirstShowingKeepsRecency(t *testing.T) {
	s := stateWithCurrent(t, "A").Answer(true, seeded(1))
	a := s.Symbols[sym(t, "A")]
	want := SymbolState{Score: StartScore + 5, Recency: 5, Streak: 1, LastSeq: 1}
	if a != want || s.Seq != 1 {
		t.Errorf("after first answer: seq=%d A=%+v, want %+v", s.Seq, a, want)
	}
}

func TestAnswerRecalculatesRecencyBeforeScore(t *testing.T) {
	s := stateWithCurrent(t, "A")
	s.Seq = 20
	s.Symbols[sym(t, "A")] = SymbolState{Score: 1200, Recency: 5, Streak: 2, LastSeq: 11}
	s = s.Answer(true, seeded(1)) // seq 21, gap 10 → recency 0.6, streak 3
	a := s.Symbols[sym(t, "A")]
	if a.Recency != 6 || a.Streak != 3 || a.Score != 1200+6*3 || a.LastSeq != 21 {
		t.Errorf("got %+v", a)
	}
}

func TestAnswerStreaksAndScores(t *testing.T) {
	cases := []struct {
		name       string
		streak     int16
		score      uint32
		correct    bool
		wantStreak int16
		wantScore  uint32
	}{
		{"right extends a right streak", 2, 1000, true, 3, 1000 + 5*3},
		{"right after wrongs restarts at 1", -3, 1000, true, 1, 1000 + 5},
		{"wrong extends a wrong streak", -2, 1000, false, -3, 1000 - 5*3},
		{"wrong after rights restarts at -1", 4, 1000, false, -1, 1000 - 5},
		{"score never goes below zero", -9, 20, false, -10, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := stateWithCurrent(t, "K")
			s.Seq = 10
			s.Symbols[sym(t, "K")] = SymbolState{Score: c.score, Recency: 5, Streak: c.streak, LastSeq: 5} // gap 6
			s = s.Answer(c.correct, seeded(1))
			k := s.Symbols[sym(t, "K")]
			if k.Streak != c.wantStreak || k.Score != c.wantScore {
				t.Errorf("streak=%d score=%d, want streak=%d score=%d", k.Streak, k.Score, c.wantStreak, c.wantScore)
			}
		})
	}
}

// readyToAdd puts every symbol in play at the first bars: A at 120, the rest
// at 110, so the average is 111.1. One right answer on A (+0.5) adds E.
func readyToAdd(s *State) {
	for _, x := range Order[:s.InPlay] {
		s.Symbols[x].Score = 1100
	}
	s.Symbols[Order[0]].Score = 1200
}

func TestBars(t *testing.T) {
	cases := []struct {
		k    int
		want Bars
	}{
		{1, Bars{Top: 1200, Avg: 1100, Low: 1000}},
		// Top and Avg follow a square root, Low a straight line:
		// 120 + 130·√(9/26) = 196.4, 110 + 90·√(9/26) = 162.9, 100 + 20·9/26 = 106.9.
		{10, Bars{Top: 1964, Avg: 1629, Low: 1069}},
		{2, Bars{Top: 1454, Avg: 1276, Low: 1007}},
		{27, Bars{Top: 2500, Avg: 2000, Low: 1200}},
	}
	for _, c := range cases {
		if got := barsFor(c.k); got != c.want {
			t.Errorf("barsFor(%d) = %+v, want %+v", c.k, got, c.want)
		}
	}
	s := initialState()
	if b, ok := s.NextBars(); !ok || b != barsFor(1) {
		t.Errorf("NextBars at the start = %+v, %v", b, ok)
	}
	s.InPlay = 18
	if b, ok := s.NextBars(); !ok || b != barsFor(10) {
		t.Errorf("NextBars with 18 in play = %+v, %v", b, ok)
	}
	s.InPlay = NumSymbols
	if _, ok := s.NextBars(); ok {
		t.Error("bars with every symbol in play")
	}
}

func TestAddingSymbols(t *testing.T) {
	answerA := func(t *testing.T, s State) State {
		t.Helper()
		s.Current = sym(t, "A")
		return s.Answer(true, seeded(1))
	}
	t.Run("reaching all three bars brings the next symbol in", func(t *testing.T) {
		s := initialState()
		readyToAdd(&s)
		s = answerA(t, s)
		if s.InPlay != StartCount+1 || !s.IsInPlay(sym(t, "E")) {
			t.Errorf("in play %d", s.InPlay)
		}
	})
	t.Run("exactly at the bars is enough", func(t *testing.T) {
		// After A's +0.5: top 120 (A), lowest 100 (I), and the other seven at
		// 110 make the average exactly 110.
		s := initialState()
		readyToAdd(&s)
		s.Symbols[sym(t, "A")].Score = 1195
		s.Symbols[sym(t, "I")].Score = 1000
		if s = answerA(t, s); s.InPlay != StartCount+1 {
			t.Errorf("in play %d", s.InPlay)
		}
	})
	t.Run("one bar missed keeps the next symbol out", func(t *testing.T) {
		cases := map[string]func(*State){
			"top": func(s *State) { s.Symbols[Order[0]].Score = 1194; s.Symbols[Order[1]].Score = 1199 },
			"average": func(s *State) { // 120 + 8 × 108.7 = 989.6, under 9 × 110
				for _, x := range Order[1:StartCount] {
					s.Symbols[x].Score = 1087
				}
			},
			"lowest": func(s *State) { s.Symbols[Order[8]].Score = 999; s.Symbols[Order[7]].Score = 1500 },
		}
		for name, miss := range cases {
			s := initialState()
			readyToAdd(&s)
			s.Symbols[Order[0]].Score = 1195
			miss(&s)
			if s = answerA(t, s); s.InPlay != StartCount {
				t.Errorf("%s missed: in play %d", name, s.InPlay)
			}
		}
	})
	t.Run("the new symbol starts a point under the lowest score", func(t *testing.T) {
		s := initialState()
		readyToAdd(&s)
		s.Symbols[sym(t, "2")].Score = 1003 // under the next Low bar, 100.7
		s = answerA(t, s)
		if got := s.Symbols[sym(t, "E")]; got != (SymbolState{Score: 993, Recency: StartRecency}) {
			t.Errorf("E joined as %+v", got)
		}
	})
	t.Run("but never above a point under the next Low bar", func(t *testing.T) {
		s := initialState()
		readyToAdd(&s) // lowest 110
		s = answerA(t, s)
		if got := s.Symbols[sym(t, "E")].Score; got != barsFor(2).Low-10 {
			t.Errorf("E joined at %d, want %d", got, barsFor(2).Low-10)
		}
	})
	t.Run("the new symbol joins fresh", func(t *testing.T) {
		s := initialState()
		readyToAdd(&s)
		s.Symbols[sym(t, "E")] = SymbolState{Score: 3000, Recency: 9, Streak: 7, LastSeq: 40}
		s = answerA(t, s)
		if got := s.Symbols[sym(t, "E")]; got.Recency != StartRecency || got.Streak != 0 || got.LastSeq != 0 {
			t.Errorf("E joined as %+v", got)
		}
	})
	t.Run("the last symbol starts a point under the lowest score", func(t *testing.T) {
		s := initialState()
		s.InPlay = NumSymbols - 1
		for _, x := range Order[:s.InPlay] {
			s.Symbols[x].Score = 2500
		}
		s.Symbols[sym(t, "8")].Score = 1300
		if s = answerA(t, s); s.InPlay != NumSymbols || s.Symbols[sym(t, "Z")].Score != 1290 {
			t.Errorf("in play %d, Z at %d", s.InPlay, s.Symbols[sym(t, "Z")].Score)
		}
	})
	t.Run("symbols never leave", func(t *testing.T) {
		s := initialState()
		s.InPlay = 20
		s.Symbols[sym(t, "A")].Score = 0
		if s = s.Answer(false, seeded(1)); s.InPlay != 20 {
			t.Errorf("in play %d", s.InPlay)
		}
	})
	t.Run("nothing joins once all are in play", func(t *testing.T) {
		s := initialState()
		s.InPlay = NumSymbols
		for i := range s.Symbols {
			s.Symbols[i].Score = 5000
		}
		if s = answerA(t, s); s.InPlay != NumSymbols {
			t.Errorf("in play %d", s.InPlay)
		}
	})
}

func TestNextShowsANewSymbolFirst(t *testing.T) {
	s := recentState(t)
	s.InPlay = StartCount + 1
	s.Symbols[sym(t, "N")].Score = 1000
	s.Symbols[sym(t, "E")].Score = 9000 // a draw would never pick it over N
	if got := shares(s, 50); got[sym(t, "E")] != 50 {
		t.Errorf("picks %v, want the new E every time", got)
	}
	s.Symbols[sym(t, "E")].LastSeq = 90 // once shown, it is drawn like the others
	if got := shares(s, 50); got[sym(t, "E")] != 0 {
		t.Errorf("picks %v, want no E", got)
	}
}

// recentState has the start symbols in play at seq 100: A, I, K and M were the
// last four shown, the other five were last shown 4–8 answers ago and score 900.
func recentState(t *testing.T) State {
	s := initialState()
	s.Seq = 100
	for i, x := range []string{"A", "I", "K", "M"} {
		s.Symbols[sym(t, x)] = SymbolState{Score: 0, Recency: 5, LastSeq: 100 - uint32(i)}
	}
	for i, x := range []string{"N", "P", "0", "1", "2"} {
		s.Symbols[sym(t, x)] = SymbolState{Score: 9000, Recency: 5, LastSeq: 96 - uint32(i)}
	}
	return s
}

// shares counts what Next picks over n seeds.
func shares(s State, n int) map[Symbol]int {
	got := map[Symbol]int{}
	for seed := range uint64(n) {
		got[s.Next(seeded(seed))]++
	}
	return got
}

func TestNextNeverPicksRecentOrLockedSymbols(t *testing.T) {
	s := recentState(t)
	s.Symbols[sym(t, "E")].Score = 0 // not in play
	for x := range shares(s, 500) {
		if !s.IsInPlay(x) || s.Seq-s.Symbols[x].LastSeq < RecentWindow {
			t.Fatalf("picked %s", x)
		}
	}
}

func TestNextFavoursLowScores(t *testing.T) {
	// N and P waited equally long; every 10 points halve the chance.
	s := recentState(t)
	s.Symbols[sym(t, "P")].LastSeq = 96
	s.Symbols[sym(t, "N")].Score = 1000
	s.Symbols[sym(t, "P")].Score = 1100
	got := shares(s, 3000)
	if got[sym(t, "N")] < 1850 || got[sym(t, "N")] > 2150 || got[sym(t, "N")]+got[sym(t, "P")] != 3000 {
		t.Errorf("picks %v, want N about 2000 and P about 1000", got)
	}
}

func TestNextFavoursSymbolsNotSeenForLong(t *testing.T) {
	// Same score; N became eligible 1 answer ago, P 3 answers ago.
	s := recentState(t)
	s.Symbols[sym(t, "N")] = SymbolState{Score: 1000, Recency: 5, LastSeq: 96}
	s.Symbols[sym(t, "P")] = SymbolState{Score: 1000, Recency: 5, LastSeq: 94}
	s.Symbols[sym(t, "0")].LastSeq = 95
	got := shares(s, 4000)
	if got[sym(t, "P")] < 2850 || got[sym(t, "P")] > 3150 || got[sym(t, "N")]+got[sym(t, "P")] != 4000 {
		t.Errorf("picks %v, want P about 3000 and N about 1000", got)
	}
}

func TestNextBreaksTiesRandomly(t *testing.T) {
	s := NewState(seeded(1)) // all start symbols tied and never shown
	if got := shares(s, 200); len(got) != StartCount {
		t.Errorf("ties produced %d distinct picks, want %d", len(got), StartCount)
	}
}

func TestAnswerNeverRepeatsWithinFivePositions(t *testing.T) {
	rng := seeded(7)
	s := NewState(rng)
	var shown []Symbol
	for range 3000 {
		shown = append(shown, s.Current)
		s = s.Answer(rng.IntN(4) != 0, rng) // 75% right
	}
	if s.InPlay == StartCount {
		t.Fatal("no symbol joined")
	}
	for i := range shown {
		for j := i + 1; j < len(shown) && j-i < 5; j++ {
			if shown[i] == shown[j] {
				t.Fatalf("%s shown at %d and %d", shown[i], i, j)
			}
		}
	}
}

func TestAlwaysRightKidGetsEverySymbol(t *testing.T) {
	for seed := range uint64(10) {
		rng := seeded(seed)
		s := NewState(rng)
		first, answers := 0, 0
		for s.InPlay < NumSymbols && answers < 1000 {
			s = s.Answer(true, rng)
			answers++
			if s.InPlay > StartCount && first == 0 {
				first = answers
			}
		}
		if s.InPlay != NumSymbols || first > 100 {
			t.Errorf("seed %d: %d in play after %d answers, first joined after %d", seed, s.InPlay, answers, first)
		}
	}
}

func TestKidsGetEverySymbolAndKeepPlayingWithoutRepeats(t *testing.T) {
	rng := seeded(5)
	s := NewState(rng)
	var shown []Symbol
	allAt := 0
	for i := range 8000 {
		shown = append(shown, s.Current)
		s = s.Answer(rng.IntN(10) != 0, rng) // 90% right
		if s.InPlay == NumSymbols && allAt == 0 {
			allAt = i + 1
		}
	}
	if allAt == 0 {
		t.Fatalf("%d in play after %d answers", s.InPlay, len(shown))
	}
	t.Logf("all in play after %d answers", allAt)
	// Strong symbols still come back: every one shows up in the last 500.
	seen := map[Symbol]bool{}
	for _, x := range shown[len(shown)-500:] {
		seen[x] = true
	}
	if len(seen) != NumSymbols {
		t.Errorf("the last 500 answers showed only %d symbols", len(seen))
	}
	for i := range shown {
		for j := i + 1; j < len(shown) && j-i < 5; j++ {
			if shown[i] == shown[j] {
				t.Fatalf("%s shown at %d and %d", shown[i], i, j)
			}
		}
	}
}
