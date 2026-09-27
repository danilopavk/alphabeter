package web

import (
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

func TestKidPath(t *testing.T) {
	cases := []struct {
		id   int64
		name string
		want string
	}{
		{4, "Mia", "4-mia"},
		{12, "Ana Marija", "12-ana-marija"},
		{3, "Đorđe", "3-đorđe"},
		{7, "  O'Brien!! ", "7-o-brien"},
		{5, "!!!", "5"},
	}
	for _, c := range cases {
		if got := kidPath(c.id, c.name); got != c.want {
			t.Errorf("kidPath(%d, %q) = %q, want %q", c.id, c.name, got, c.want)
		}
	}
}

func TestParseKidPath(t *testing.T) {
	for path, want := range map[string]int64{"4-mia": 4, "4": 4, "12-ana-marija": 12, "3-đorđe": 3} {
		if got, ok := parseKidPath(path); !ok || got != want {
			t.Errorf("parseKidPath(%q) = %d, %v; want %d", path, got, ok, want)
		}
	}
	for _, bad := range []string{"", "mia", "-4", "4x", "x-4", "99999999999999999999-mia"} {
		if _, ok := parseKidPath(bad); ok {
			t.Errorf("parseKidPath(%q) accepted", bad)
		}
	}
}
