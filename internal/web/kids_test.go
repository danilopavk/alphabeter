package web

import (
	"reflect"
	"testing"
	"time"

	"github.com/danilopavk/alphabeter/internal/game"
)

func TestProgress(t *testing.T) {
	cases := []struct {
		state game.State
		want  string
	}{
		{game.State{InPlay: 9, Seq: 0}, "9 of 36 in play · 0 answers"},
		{game.State{InPlay: 15, Seq: 1}, "15 of 36 in play · 1 answer"},
		{game.State{InPlay: 36, Seq: 812}, "All 36 in play · 812 answers"},
	}
	for _, c := range cases {
		if got := progress(c.state); got != c.want {
			t.Errorf("progress(in play %d, seq %d) = %q, want %q", c.state.InPlay, c.state.Seq, got, c.want)
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

// stateWith returns a state with n symbols in play, all scoring score.
func stateWith(n uint8, score uint32) game.State {
	s := game.State{InPlay: n}
	for _, x := range game.Order[:n] {
		s.Symbols[x].Score = score
	}
	return s
}

func TestNextBars(t *testing.T) {
	// 18 in play: the 10th to join needs top 196.4, average 162.9, lowest 106.9;
	// 17 at 170 and one at 100 average 166.1.
	mixed := stateWith(18, 1700)
	mixed.Symbols[game.Order[5]].Score = 1000
	cases := []struct {
		name  string
		state game.State
		want  []barView
	}{
		{"start", stateWith(9, 1000), []barView{
			{"average", "100", "110", false}, {"top", "100", "120", false}, {"lowest", "100", "100", true}}},
		{"some bars reached", mixed, []barView{
			{"average", "166.1", "162.9", true}, {"top", "170", "196.4", false}, {"lowest", "100", "106.9", false}}},
		{"all in play", stateWith(36, 1000), nil},
	}
	for _, c := range cases {
		if got := nextBars(c.state); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
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
		{"right", game.Outcome{Symbol: 0, Correct: true, Before: 1070, After: 1085}, game.State{InPlay: 9},
			flashView{Symbol: "A", Correct: true, To: "108.5", Delta: "+1.5"}},
		{"wrong", game.Outcome{Symbol: 26, Before: 1070, After: 1065}, game.State{InPlay: 9},
			flashView{Symbol: "0", To: "106.5", Delta: "−0.5"}},
		{"a new symbol joins", game.Outcome{Symbol: 0, Correct: true, Before: 1195, After: 1200, Added: true}, game.State{InPlay: 10},
			flashView{Symbol: "A", Correct: true, To: "120", Delta: "+0.5", Message: "New letter: E", Joined: true}},
		{"no change at zero", game.Outcome{Symbol: 0, Before: 0, After: 0}, game.State{InPlay: 9},
			flashView{Symbol: "A", To: "0", Delta: "±0"}},
	}
	for _, c := range cases {
		if got := flashOf(c.outcome, c.state); *got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, *got, c.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute: "10 minutes", time.Minute: "1 minute", time.Hour: "1 hour", 3 * time.Hour: "3 hours",
		24 * time.Hour: "1 day", 30 * 24 * time.Hour: "30 days", 90 * time.Minute: "90 minutes",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestBoard(t *testing.T) {
	st := stateWith(12, game.StartScore) // the start symbols, E, R and T
	set := func(char string, score uint32) {
		sym, _ := game.ParseSymbol(char)
		st.Symbols[sym] = game.SymbolState{Score: score}
	}
	set("A", 2420)
	set("I", 995)
	set("K", 1500)
	set("M", 1499)
	set("N", 2000)
	set("P", 1999)
	set("E", 0)
	set("3", 1450) // not in play, so its score is not shown

	tiles := board(st)
	if len(tiles) != game.NumSymbols {
		t.Fatalf("%d tiles, want %d", len(tiles), game.NumSymbols)
	}
	want := []tileView{
		{"A", "242", "mastered"}, {"I", "99.5", "low"}, {"K", "150", "known"}, {"M", "149.9", "learning"},
		{"N", "200", "mastered"}, {"P", "199.9", "known"}, {"0", "100", "learning"}, {"1", "100", "learning"},
		{"2", "100", "learning"}, {"E", "0", "low"}, {"R", "100", "learning"}, {"T", "100", "learning"},
		{"3", "", "locked"},
	}
	if !reflect.DeepEqual(tiles[:len(want)], want) {
		t.Errorf("tiles:\n got %+v\nwant %+v", tiles[:len(want)], want)
	}
	for _, tile := range tiles[len(want):] {
		if tile.Status != "locked" || tile.Score != "" {
			t.Errorf("%+v is not locked", tile)
		}
	}
	if last := tiles[len(tiles)-1]; last.Symbol != "Z" {
		t.Errorf("last tile %s, want Z", last.Symbol)
	}
}

func TestBoardLegend(t *testing.T) {
	want := []legendItem{{"low", "Under 100"}, {"learning", "100–150"}, {"known", "150–200"}, {"mastered", "200+"}}
	if !reflect.DeepEqual(boardLegend, want) {
		t.Errorf("legend %+v, want %+v", boardLegend, want)
	}
}
