package web

import (
	"reflect"
	"testing"

	"github.com/danilopavk/alphabeter/internal/game"
)

func TestProgress(t *testing.T) {
	cases := []struct {
		state game.State
		want  string
	}{
		{game.State{Batch: 1, Seq: 0}, "Level 1 of 4 · 0 answers"},
		{game.State{Batch: 2, Seq: 1}, "Level 2 of 4 · 1 answer"},
		{game.State{Batch: 4, Seq: 812, Completed: true}, "All levels done · 812 answers"},
	}
	for _, c := range cases {
		if got := progress(c.state); got != c.want {
			t.Errorf("progress(batch %d, seq %d, completed %v) = %q, want %q",
				c.state.Batch, c.state.Seq, c.state.Completed, got, c.want)
		}
	}
}

func TestFormatScore(t *testing.T) {
	for tenths, want := range map[uint32]string{0: "0", 1000: "100", 1075: "107.5", 1301: "130.1", 12: "1.2"} {
		if got := formatScore(tenths); got != want {
			t.Errorf("formatScore(%d) = %q, want %q", tenths, got, want)
		}
	}
}

func stateShowing(batch uint8, st game.SymbolState) game.State {
	s := game.State{Batch: batch, Current: 0}
	s.Symbols[0] = st
	return s
}

func TestScoreNote(t *testing.T) {
	cases := []struct {
		name  string
		state game.State
		want  string
	}{
		{"below the level threshold", stateShowing(1, game.SymbolState{Score: 1070}), "Over 130 needed for the next level"},
		{"exactly the threshold", stateShowing(1, game.SymbolState{Score: 1300}), "Over 130 needed for the next level"},
		{"above the threshold", stateShowing(2, game.SymbolState{Score: 1420}), "Over 130 ✓ · retires at 160"},
		{"retired filler", stateShowing(3, game.SymbolState{Score: 1700, Retired: true}), "Retired until the next level"},
		{"last level", stateShowing(4, game.SymbolState{Score: 1420}), "Retires at 160 · retire all to finish"},
		{"retired in the last level", stateShowing(4, game.SymbolState{Score: 1700, Retired: true}), "Retired"},
		{"free play", game.State{Batch: 4, Completed: true}, "Free play"},
	}
	for _, c := range cases {
		if got := scoreNote(c.state); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFlash(t *testing.T) {
	cases := []struct {
		name    string
		outcome game.Outcome
		state   game.State // after the answer
		want    flashView
	}{
		{"right", game.Outcome{Symbol: 0, Correct: true, Before: 1070, After: 1085}, game.State{Batch: 1},
			flashView{Symbol: "A", Correct: true, To: "108.5", Delta: "+1.5"}},
		{"wrong", game.Outcome{Symbol: 26, Before: 1070, After: 1065}, game.State{Batch: 1},
			flashView{Symbol: "0", To: "106.5", Delta: "−0.5"}},
		{"retired", game.Outcome{Symbol: 0, Correct: true, Before: 1590, After: 1610, Retired: true}, game.State{Batch: 2},
			flashView{Symbol: "A", Correct: true, To: "161", Delta: "+2", Message: "Retired until the next level"}},
		{"retired in the last level", game.Outcome{Symbol: 0, Correct: true, Before: 1590, After: 1610, Retired: true}, game.State{Batch: 4},
			flashView{Symbol: "A", Correct: true, To: "161", Delta: "+2", Message: "Retired"}},
		{"level up", game.Outcome{Symbol: 0, Correct: true, Before: 1290, After: 1310, LevelUp: true}, game.State{Batch: 3},
			flashView{Symbol: "A", Correct: true, To: "131", Delta: "+2", Message: "Level 3 unlocked!"}},
		{"no change at zero", game.Outcome{Symbol: 0, Before: 0, After: 0}, game.State{Batch: 1},
			flashView{Symbol: "A", To: "0", Delta: "±0"}},
		{"completed", game.Outcome{Symbol: 0, Correct: true, Before: 1590, After: 1610, Retired: true, Completed: true}, game.State{Batch: 4, Completed: true},
			flashView{Symbol: "A", Correct: true, To: "161", Delta: "+2", Message: "All levels done!"}},
	}
	for _, c := range cases {
		if got := flashOf(c.outcome, c.state); *got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, *got, c.want)
		}
	}
}

func TestBoard(t *testing.T) {
	st := game.State{Batch: 2}
	for i := range st.Symbols {
		st.Symbols[i].Score = game.StartScore
	}
	set := func(char string, score uint32, retired bool) {
		sym, _ := game.ParseSymbol(char)
		st.Symbols[sym] = game.SymbolState{Score: score, Retired: retired}
	}
	set("A", 1420, false)
	set("I", 995, false)
	set("K", 1301, false)
	set("M", 1650, true)
	set("N", 1300, false)
	set("E", 0, false)
	set("B", 1450, false) // locked, so its score is not shown

	levels := board(st)
	if len(levels) != len(game.Batches) {
		t.Fatalf("%d levels, want %d", len(levels), len(game.Batches))
	}
	want := levelView{Number: 1, Tiles: []tileView{
		{"A", "142", "known"}, {"I", "99.5", "low"}, {"K", "130.1", "known"}, {"M", "165", "retired"},
		{"N", "130", "learning"}, {"P", "100", "learning"},
		{"0", "100", "learning"}, {"1", "100", "learning"}, {"2", "100", "learning"},
	}}
	if !reflect.DeepEqual(levels[0], want) {
		t.Errorf("level 1:\n got %+v\nwant %+v", levels[0], want)
	}
	if l := levels[1]; l.Number != 2 || l.Locked || l.Tiles[0] != (tileView{"E", "0", "low"}) {
		t.Errorf("level 2: %+v", l)
	}
	for _, l := range levels[2:] {
		if !l.Locked {
			t.Errorf("level %d is not locked", l.Number)
		}
		for _, tile := range l.Tiles {
			if tile.Status != "locked" || tile.Score != "" {
				t.Errorf("level %d: %+v", l.Number, tile)
			}
		}
	}
	if l := levels[2]; l.Number != 3 || l.Tiles[0].Symbol != "B" || len(l.Tiles) != len(game.Batches[2]) {
		t.Errorf("level 3: %+v", l)
	}

	done := game.State{Batch: 4, Completed: true}
	for _, l := range board(done) {
		if l.Locked {
			t.Errorf("level %d locked in free play", l.Number)
		}
	}
}

func TestBoardLegend(t *testing.T) {
	want := []legendItem{{"low", "Under 100"}, {"learning", "100–130"}, {"known", "Over 130"}, {"retired", "Retired"}}
	if !reflect.DeepEqual(boardLegend, want) {
		t.Errorf("legend %+v, want %+v", boardLegend, want)
	}
}
