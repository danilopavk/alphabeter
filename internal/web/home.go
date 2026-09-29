package web

import (
	"net/http"
	"strings"

	"github.com/danilopavk/alphabeter/internal/game"
)

type homePage struct {
	FirstLevel string // e.g. "A, I, K, M, N, P, 0, 1 and 2"
	Levels     int
	Board      []levelView
	Legend     []legendItem
}

// home is the landing page. Parents with a session go straight to their kids;
// /kids sends them to log in if the session turns out to be over.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{accessCookie, refreshCookie} {
		if _, err := r.Cookie(name); err == nil {
			http.Redirect(w, r, "/kids", http.StatusSeeOther)
			return
		}
	}
	s.ui.Page(w, http.StatusOK, "home", homePage{
		FirstLevel: listSymbols(game.Batches[0]),
		Levels:     len(game.Batches),
		Board:      sampleBoard,
		Legend:     boardLegend,
	})
}

// listSymbols writes symbols as a list for people: "A, I and 0".
func listSymbols(symbols []game.Symbol) string {
	names := make([]string, len(symbols))
	for i, sym := range symbols {
		names[i] = sym.String()
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// sampleBoard is a made-up kid halfway through level 2, to show parents the
// progress board.
var sampleBoard = func() []levelView {
	st := game.State{Batch: 2}
	for i := range st.Symbols {
		st.Symbols[i].Score = game.StartScore
	}
	for _, s := range []struct {
		char    string
		score   uint32
		retired bool
	}{
		{"A", 1620, true}, {"I", 1390, false}, {"K", 1315, false}, {"M", 1480, false}, {"N", 1180, false},
		{"P", 1265, false}, {"0", 1640, true}, {"1", 1120, false}, {"2", 975, false},
		{"E", 1045, false}, {"R", 960, false}, {"3", 1015, false},
	} {
		sym, _ := game.ParseSymbol(s.char)
		st.Symbols[sym] = game.SymbolState{Score: s.score, Retired: s.retired}
	}
	return board(st)
}()
