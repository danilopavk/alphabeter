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

func TestBatchesCoverEverySymbolOnce(t *testing.T) {
	want := []string{"AIKMNP012", "ERT345", "BDGLOS67", "CFHJQUVWXYZ89"}
	if len(Batches) != len(want) {
		t.Fatalf("%d batches, want %d", len(Batches), len(want))
	}
	seen := map[Symbol]bool{}
	for i, batch := range Batches {
		var b strings.Builder
		for _, s := range batch {
			b.WriteString(s.String())
			if seen[s] {
				t.Errorf("%s appears in more than one batch", s)
			}
			seen[s] = true
		}
		if b.String() != want[i] {
			t.Errorf("batch %d = %s, want %s", i+1, b.String(), want[i])
		}
	}
	if len(seen) != NumSymbols {
		t.Errorf("batches cover %d symbols, want %d", len(seen), NumSymbols)
	}
	if len(Batches[0]) <= RecentWindow {
		t.Errorf("first batch must be larger than the recent window")
	}
}

func TestNewState(t *testing.T) {
	s := NewState(seeded(1))
	if s.Seq != 0 || s.Batch != 1 {
		t.Fatalf("seq=%d batch=%d", s.Seq, s.Batch)
	}
	for i, st := range s.Symbols {
		if st != (SymbolState{Score: StartScore, Recency: StartRecency}) {
			t.Errorf("symbol %s starts as %+v", Symbol(i), st)
		}
	}
	if !s.IsUnlocked(s.Current) {
		t.Errorf("current %s is not in batch 1", s.Current)
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
	t.Run("retiring", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Symbols[sym(t, "A")].Score = RetireScore - 5
		if _, o := s.AnswerWithOutcome(true, seeded(1)); !o.Retired || o.LevelUp || o.Completed {
			t.Errorf("outcome %+v", o)
		}
	})
	t.Run("a retired filler staying retired is not news", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Symbols[sym(t, "A")] = SymbolState{Score: 1700, Recency: 5, Retired: true}
		if _, o := s.AnswerWithOutcome(true, seeded(1)); o.Retired {
			t.Errorf("outcome %+v", o)
		}
	})
	t.Run("level up reports the score before the reset", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		masterBatches(&s, 1)
		next, o := s.AnswerWithOutcome(true, seeded(1))
		if !o.LevelUp || o.Before != UnlockScore+1 || o.After != UnlockScore+6 {
			t.Errorf("outcome %+v", o)
		}
		if next.Symbols[sym(t, "A")].Score != resetScore(UnlockScore+6) {
			t.Errorf("state was not reset")
		}
	})
	t.Run("completing", func(t *testing.T) {
		if _, o := lastLevelAllRetiredBut(t, "Q").AnswerWithOutcome(true, seeded(1)); !o.Completed {
			t.Errorf("outcome %+v", o)
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

func TestNextPicksLowestUnlockedOutsideRecentWindow(t *testing.T) {
	s := NewState(seeded(1))
	s.Seq = 100
	for _, x := range Batches[0] {
		s.Symbols[x] = SymbolState{Score: 1400, Recency: 5, LastSeq: 50}
	}
	s.Symbols[sym(t, "Z")].Score = 0 // locked, must never be picked
	// The last four shown have the lowest scores but are excluded.
	for i, x := range []string{"A", "I", "K", "N"} {
		s.Symbols[sym(t, x)] = SymbolState{Score: 100, Recency: 5, LastSeq: 100 - uint32(i)}
	}
	s.Symbols[sym(t, "2")].Score = 1300 // lowest eligible
	for seed := range uint64(20) {
		if got := s.Next(seeded(seed)); got != sym(t, "2") {
			t.Fatalf("seed %d: Next = %s, want 2", seed, got)
		}
	}

	// Fifth most recent is eligible again.
	s.Symbols[sym(t, "P")] = SymbolState{Score: 200, Recency: 5, LastSeq: 96}
	if got := s.Next(seeded(1)); got != sym(t, "P") {
		t.Errorf("Next = %s, want P (shown 5th last)", got)
	}
}

func TestNextBreaksTiesRandomly(t *testing.T) {
	s := NewState(seeded(1)) // all batch-1 symbols tied at the start score
	seen := map[Symbol]bool{}
	for seed := range uint64(200) {
		seen[s.Next(seeded(seed))] = true
	}
	if len(seen) != len(Batches[0]) {
		t.Errorf("ties produced %d distinct picks, want %d", len(seen), len(Batches[0]))
	}
}

func masterBatches(s *State, n int) {
	for _, batch := range Batches[:n] {
		for _, x := range batch {
			s.Symbols[x].Score = UnlockScore + 1
		}
	}
}

func TestUnlock(t *testing.T) {
	t.Run("all unlocked above the threshold unlocks the next batch", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		masterBatches(&s, 1)
		if s = s.Answer(true, seeded(1)); s.Batch != 2 {
			t.Errorf("batch = %d, want 2", s.Batch)
		}
	})
	t.Run("exactly the threshold is not enough", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		masterBatches(&s, 1)
		s.Symbols[sym(t, "2")].Score = UnlockScore
		if s = s.Answer(true, seeded(1)); s.Batch != 1 {
			t.Errorf("batch = %d, want 1", s.Batch)
		}
	})
	t.Run("unlocking is one-way", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Batch = 2
		s.Symbols[sym(t, "A")].Score = 0
		if s = s.Answer(false, seeded(1)); s.Batch != 2 {
			t.Errorf("batch = %d, want 2", s.Batch)
		}
	})
	t.Run("stops at the last batch", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Batch = uint8(len(Batches))
		masterBatches(&s, len(Batches))
		if s = s.Answer(true, seeded(1)); s.Batch != uint8(len(Batches)) {
			t.Errorf("batch = %d", s.Batch)
		}
	})
}

func TestAnswerNeverRepeatsWithinFivePositions(t *testing.T) {
	rng := seeded(7)
	s := NewState(rng)
	var shown []Symbol
	for range 2000 {
		shown = append(shown, s.Current)
		s = s.Answer(rng.IntN(4) != 0, rng) // 75% right
	}
	for i := range shown {
		for j := i + 1; j < len(shown) && j-i < 5; j++ {
			if shown[i] == shown[j] {
				t.Fatalf("%s shown at %d and %d", shown[i], i, j)
			}
		}
	}
}

// Regression for the recency dead end: with a neutral gap of 10, batches of
// 7–8 symbols could never progress.
func TestAlwaysRightKidUnlocksEveryBatch(t *testing.T) {
	for seed := range uint64(10) {
		rng := seeded(seed)
		s := NewState(rng)
		answers := 0
		for s.Batch < uint8(len(Batches)) && answers < 2000 {
			s = s.Answer(true, rng)
			answers++
		}
		if s.Batch != uint8(len(Batches)) {
			t.Errorf("seed %d: stuck in batch %d after %d answers", seed, s.Batch, answers)
		}
	}
}

func TestThresholds(t *testing.T) {
	if UnlockScore != 1300 {
		t.Errorf("UnlockScore = %d, want 1300 (130.0)", UnlockScore)
	}
	if RetireScore != 1600 {
		t.Errorf("RetireScore = %d, want 1600 (160.0)", RetireScore)
	}
}

func TestResetScore(t *testing.T) {
	cases := map[uint32]uint32{
		1000: 1000, // never above 130 (new symbols): 100
		1300: 1000,
		1301: 1000, // 100.07 rounds down
		1330: 1020,
		1450: 1100, // halfway between 130 and 160 → halfway between 100 and 120
		1599: 1199,
		1600: 1200,
		2500: 1200, // 160 and above: 120
	}
	for score, want := range cases {
		if got := resetScore(score); got != want {
			t.Errorf("resetScore(%d) = %d, want %d", score, got, want)
		}
	}
}

func TestUnlockResetsScoresButKeepsStreaksAndRecency(t *testing.T) {
	s := stateWithCurrent(t, "A")
	masterBatches(&s, 1) // every batch-1 symbol at 130.1
	k := sym(t, "K")
	s.Symbols[k] = SymbolState{Score: 1700, Recency: 8, Streak: 12, LastSeq: 0, Retired: true}
	s = s.Answer(true, seeded(1)) // A → 130.6 → level 2
	if s.Batch != 2 {
		t.Fatalf("batch = %d", s.Batch)
	}
	want := map[string]uint32{"A": 1004, "K": 1200, "I": 1000, "E": 1000}
	for x, score := range want {
		if got := s.Symbols[sym(t, x)].Score; got != score {
			t.Errorf("%s reset to %d, want %d", x, got, score)
		}
	}
	for i, st := range s.Symbols {
		if st.Retired {
			t.Errorf("%s still retired after unlock", Symbol(i))
		}
	}
	if got := s.Symbols[k]; got.Recency != 8 || got.Streak != 12 {
		t.Errorf("K lost its recency/streak: %+v", got)
	}
	if got := s.Symbols[sym(t, "A")]; got.Streak != 1 || got.LastSeq != 1 {
		t.Errorf("A lost its streak/last seq: %+v", got)
	}
}

// lastLevelAllRetiredBut returns a state in the last level where every symbol
// is retired except cur, which is one right answer away from retiring.
func lastLevelAllRetiredBut(t *testing.T, cur string) State {
	s := stateWithCurrent(t, cur)
	s.Batch = uint8(len(Batches))
	s.Seq = 1000
	for i := range s.Symbols {
		s.Symbols[i] = SymbolState{Score: 1700, Recency: 5, Streak: 3, LastSeq: 900, Retired: true}
	}
	s.Symbols[sym(t, cur)] = SymbolState{Score: RetireScore - 5, Recency: 5, LastSeq: 990}
	return s
}

func TestCompletingTheLastLevel(t *testing.T) {
	t.Run("retiring the last symbol completes the game and brings everything back", func(t *testing.T) {
		s := lastLevelAllRetiredBut(t, "Q").Answer(true, seeded(1))
		if !s.Completed {
			t.Fatal("not completed")
		}
		for i, st := range s.Symbols {
			if st.Retired || st.Score < 1600 {
				t.Errorf("%s after completion: %+v", Symbol(i), st)
			}
		}
	})
	t.Run("a wrong answer does not complete", func(t *testing.T) {
		if s := lastLevelAllRetiredBut(t, "Q").Answer(false, seeded(1)); s.Completed {
			t.Error("completed on a wrong answer")
		}
	})
	t.Run("after completion nothing retires and scores keep going", func(t *testing.T) {
		s := lastLevelAllRetiredBut(t, "Q").Answer(true, seeded(1))
		for range 500 {
			s = s.Answer(true, seeded(uint64(s.Seq)))
			for i, st := range s.Symbols {
				if st.Retired {
					t.Fatalf("%s retired after completion", Symbol(i))
				}
			}
		}
		if !s.Completed || s.Batch != uint8(len(Batches)) {
			t.Errorf("completed=%v batch=%d", s.Completed, s.Batch)
		}
		for i, st := range s.Symbols {
			if st.Score <= 1700 {
				t.Errorf("%s did not keep scoring: %d", Symbol(i), st.Score)
			}
		}
	})
}

func TestRetirement(t *testing.T) {
	a := func(t *testing.T) Symbol { return sym(t, "A") }

	t.Run("a right answer reaching the retire score retires the symbol", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Symbols[a(t)].Score = RetireScore - 5 // first showing adds 0.5
		if s = s.Answer(true, seeded(1)); !s.Symbols[a(t)].Retired {
			t.Errorf("not retired at %d", s.Symbols[a(t)].Score)
		}
	})
	t.Run("just under the retire score stays in rotation", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Symbols[a(t)].Score = RetireScore - 6
		if s = s.Answer(true, seeded(1)); s.Symbols[a(t)].Retired {
			t.Errorf("retired at %d", s.Symbols[a(t)].Score)
		}
	})
	t.Run("a wrong answer brings a retired symbol back", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Symbols[a(t)] = SymbolState{Score: 2500, Recency: 5, Streak: 9, Retired: true}
		if s = s.Answer(false, seeded(1)); s.Symbols[a(t)].Retired {
			t.Error("still retired after a wrong answer")
		}
	})
	t.Run("unlocking the next batch brings retired symbols back", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		masterBatches(&s, 1)
		s.Symbols[sym(t, "K")] = SymbolState{Score: 2100, Recency: 5, Retired: true}
		s = s.Answer(true, seeded(1))
		if s.Batch != 2 || s.Symbols[sym(t, "K")].Retired {
			t.Errorf("batch %d, K retired = %v", s.Batch, s.Symbols[sym(t, "K")].Retired)
		}
	})
	t.Run("in the last batch retirement is permanent", func(t *testing.T) {
		s := stateWithCurrent(t, "A")
		s.Batch = uint8(len(Batches))
		masterBatches(&s, len(Batches))
		s.Symbols[sym(t, "K")] = SymbolState{Score: 2100, Recency: 5, Retired: true}
		if s = s.Answer(true, seeded(1)); !s.Symbols[sym(t, "K")].Retired {
			t.Error("K came back although there is no next batch")
		}
	})
}

func TestNextSkipsRetiredSymbols(t *testing.T) {
	s := NewState(seeded(1))
	s.Seq = 100
	for _, x := range Batches[0] {
		s.Symbols[x] = SymbolState{Score: 1100, Recency: 5, LastSeq: 50}
	}
	s.Symbols[sym(t, "A")] = SymbolState{Score: 1000, Recency: 5, LastSeq: 50, Retired: true}
	s.Symbols[sym(t, "K")].Score = 1050
	for seed := range uint64(20) {
		if got := s.Next(seeded(seed)); got != sym(t, "K") {
			t.Fatalf("Next = %s, want K", got)
		}
	}
}

func TestNextFallsBackToRetiredSymbols(t *testing.T) {
	// Every symbol still in rotation was among the last four shown, so the
	// filler is the retired symbol with the lowest score.
	s := NewState(seeded(1))
	s.Seq = 100
	for i, x := range Batches[0] {
		s.Symbols[x] = SymbolState{Score: 2500 + uint32(i), Recency: 5, LastSeq: 50, Retired: true}
	}
	for i, x := range []string{"A", "I", "K", "N"} {
		s.Symbols[sym(t, x)] = SymbolState{Score: 1000, Recency: 5, LastSeq: 100 - uint32(i)}
	}
	if got := s.Next(seeded(1)); got != sym(t, "M") {
		t.Errorf("Next = %s, want M (lowest retired)", got)
	}
}

func TestKidsCompleteTheGameAndKeepPlayingWithoutRepeats(t *testing.T) {
	rng := seeded(5)
	s := NewState(rng)
	var shown []Symbol
	completedAt := 0
	for i := range 8000 {
		shown = append(shown, s.Current)
		s = s.Answer(rng.IntN(10) != 0, rng) // 90% right
		if s.Completed && completedAt == 0 {
			completedAt = i + 1
		}
	}
	if !s.Completed {
		t.Fatalf("not completed after %d answers (batch %d)", len(shown), s.Batch)
	}
	t.Logf("completed after %d answers", completedAt)
	// Free play shows every symbol, not just a few.
	seen := map[Symbol]bool{}
	for _, x := range shown[len(shown)-500:] {
		seen[x] = true
	}
	if len(seen) != NumSymbols {
		t.Errorf("free play showed only %d symbols", len(seen))
	}
	for i := range shown {
		for j := i + 1; j < len(shown) && j-i < 5; j++ {
			if shown[i] == shown[j] {
				t.Fatalf("%s shown at %d and %d", shown[i], i, j)
			}
		}
	}
}

func TestAlwaysRightKidUnlocksBatch2Quickly(t *testing.T) {
	rng := seeded(3)
	s := NewState(rng)
	answers := 0
	for s.Batch == 1 {
		s = s.Answer(true, rng)
		answers++
	}
	t.Logf("batch 2 unlocked after %d answers", answers)
	if answers > 150 {
		t.Errorf("took %d answers", answers)
	}
}
