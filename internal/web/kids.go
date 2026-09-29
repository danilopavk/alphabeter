package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/danilopavk/alphabeter/internal/game"
	"github.com/danilopavk/alphabeter/internal/kids"
)

type kidView struct {
	Path     string // URL segment, e.g. "mia"
	Name     string
	Progress string
	Board    []levelView
}

type kidsPage struct {
	Kids   []kidView
	Legend []legendItem
	Name   string // re-filled into the add form after a validation error
	Error  string
}

type playView struct {
	Path   string // URL segment, e.g. "mia"
	Name   string
	Symbol string
	Seq    uint32
	Score  string     // of the symbol on screen
	Note   string     // what that score means, e.g. "Over 130 needed for the next level"
	Flash  *flashView // the answer just given, animated; nil on a plain page load
	Board  []levelView
	Legend []legendItem
}

// levelView is one level (batch) of the progress board.
type levelView struct {
	Number int
	Locked bool
	Tiles  []tileView
}

// tileView is one symbol on the progress board.
type tileView struct {
	Symbol string
	Score  string // empty while locked
	Status string // locked, low, learning, known or retired; also its CSS class
}

type legendItem struct {
	Status string
	Label  string
}

var boardLegend = []legendItem{
	{"low", "Under " + formatScore(game.StartScore)},
	{"learning", formatScore(game.StartScore) + "–" + formatScore(game.UnlockScore)},
	{"known", "Over " + formatScore(game.UnlockScore)},
	{"retired", "Retired"},
}

// flashView shows the answered symbol's new score for a moment.
type flashView struct {
	Symbol  string
	Correct bool
	To      string // the new score
	Delta   string // the change, e.g. "+0.5"
	Message string // level up, retired, completed
}

func (s *Server) listKids(w http.ResponseWriter, r *http.Request, userID int64) {
	s.renderKids(w, r, userID, http.StatusOK, kidsPage{})
}

func (s *Server) renderKids(w http.ResponseWriter, r *http.Request, userID int64, status int, page kidsPage) {
	list, err := s.kids.List(r.Context(), userID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, k := range list {
		page.Kids = append(page.Kids, kidView{
			Path: k.Slug, Name: k.Name, Progress: progress(k.State), Board: board(k.State),
		})
	}
	page.Legend = boardLegend
	if isHtmx(r) {
		// htmx only swaps 2xx responses, so validation errors come back as 200.
		s.ui.Fragment(w, http.StatusOK, "kids", "kid-list", page)
		return
	}
	s.ui.Page(w, status, "kids", page)
}

func progress(st game.State) string {
	answers := "answers"
	if st.Seq == 1 {
		answers = "answer"
	}
	if st.Completed {
		return fmt.Sprintf("All levels done · %d %s", st.Seq, answers)
	}
	return fmt.Sprintf("Level %d of %d · %d %s", st.Batch, len(game.Batches), st.Seq, answers)
}

// board is the progress dashboard: every symbol by level, colored by score.
func board(st game.State) []levelView {
	levels := make([]levelView, len(game.Batches))
	for i, batch := range game.Batches {
		levels[i] = levelView{Number: i + 1, Locked: i >= int(st.Batch)}
		for _, sym := range batch {
			tile := tileView{Symbol: sym.String(), Status: tileStatus(st, sym)}
			if tile.Status != "locked" {
				tile.Score = formatScore(st.Symbols[sym].Score)
			}
			levels[i].Tiles = append(levels[i].Tiles, tile)
		}
	}
	return levels
}

func tileStatus(st game.State, sym game.Symbol) string {
	s := st.Symbols[sym]
	switch {
	case !st.IsUnlocked(sym):
		return "locked"
	case s.Retired:
		return "retired"
	case s.Score > game.UnlockScore:
		return "known"
	case s.Score < game.StartScore:
		return "low"
	default:
		return "learning"
	}
}

func (s *Server) addKid(w http.ResponseWriter, r *http.Request, userID int64) {
	name := r.PostFormValue("name")
	_, err := s.kids.Create(r.Context(), userID, name)
	switch {
	case errors.Is(err, kids.ErrInvalidName):
		s.renderKids(w, r, userID, http.StatusBadRequest, kidsPage{Name: name, Error: "Name must be 1 to 40 characters."})
	case errors.Is(err, kids.ErrDuplicateName):
		s.renderKids(w, r, userID, http.StatusBadRequest, kidsPage{Name: name, Error: "You already have a kid with that name."})
	case err != nil:
		s.serverError(w, err)
	case isHtmx(r):
		s.renderKids(w, r, userID, http.StatusOK, kidsPage{})
	default:
		http.Redirect(w, r, "/kids", http.StatusSeeOther)
	}
}

func (s *Server) deleteKid(w http.ResponseWriter, r *http.Request, userID int64) {
	err := s.kids.Delete(r.Context(), userID, r.PathValue("kid"))
	switch {
	case errors.Is(err, kids.ErrNotFound):
		s.notFound(w)
	case err != nil:
		s.serverError(w, err)
	case isHtmx(r):
		s.renderKids(w, r, userID, http.StatusOK, kidsPage{})
	default:
		http.Redirect(w, r, "/kids", http.StatusSeeOther)
	}
}

func (s *Server) play(w http.ResponseWriter, r *http.Request, userID int64) {
	kid, err := s.kids.Get(r.Context(), userID, r.PathValue("kid"))
	switch {
	case errors.Is(err, kids.ErrNotFound):
		s.notFound(w)
	case err != nil:
		s.serverError(w, err)
	default:
		s.ui.Page(w, http.StatusOK, "play", viewOf(kid, nil))
	}
}

// answer applies Yes/No to the symbol the parent saw at seq. A stale seq (double
// tap, second tab) changes nothing and just shows the current symbol.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, userID int64) {
	seq, err := strconv.ParseUint(r.PostFormValue("seq"), 10, 32)
	correct := r.PostFormValue("correct")
	if err != nil || (correct != "1" && correct != "0") {
		http.Error(w, "bad answer", http.StatusBadRequest)
		return
	}
	kid, outcome, err := s.kids.Answer(r.Context(), userID, r.PathValue("kid"), uint32(seq), correct == "1")
	var flash *flashView
	if err == nil {
		flash = flashOf(outcome, kid.State)
	}
	switch {
	case errors.Is(err, kids.ErrNotFound):
		s.notFound(w)
	case err != nil && !errors.Is(err, kids.ErrConflict):
		s.serverError(w, err)
	case isHtmx(r):
		s.ui.Fragment(w, http.StatusOK, "play", "stage", viewOf(kid, flash))
	default:
		http.Redirect(w, r, playURL(kid), http.StatusSeeOther)
	}
}

func viewOf(k kids.Kid, flash *flashView) playView {
	st := k.State
	return playView{
		Path:   k.Slug,
		Name:   k.Name,
		Symbol: st.Current.String(),
		Seq:    st.Seq,
		Score:  formatScore(st.Symbols[st.Current].Score),
		Note:   scoreNote(st),
		Flash:  flash,
		Board:  board(st),
		Legend: boardLegend,
	}
}

// formatScore turns tenths into a score with at most one decimal: 1075 → "107.5".
func formatScore(tenths uint32) string {
	if tenths%10 == 0 {
		return strconv.FormatUint(uint64(tenths/10), 10)
	}
	return fmt.Sprintf("%d.%d", tenths/10, tenths%10)
}

func formatDelta(tenths int64) string {
	switch {
	case tenths > 0:
		return "+" + formatScore(uint32(tenths))
	case tenths < 0:
		return "−" + formatScore(uint32(-tenths))
	default:
		return "±0"
	}
}

// scoreNote explains what the score of the symbol on screen needs next.
func scoreNote(st game.State) string {
	sym := st.Symbols[st.Current]
	lastLevel := int(st.Batch) == len(game.Batches)
	switch {
	case st.Completed:
		return "Free play"
	case sym.Retired && lastLevel:
		return "Retired"
	case sym.Retired:
		return "Retired until the next level"
	case lastLevel:
		return "Retires at " + formatScore(game.RetireScore) + " · retire all to finish"
	case sym.Score > game.UnlockScore:
		return "Over " + formatScore(game.UnlockScore) + " ✓ · retires at " + formatScore(game.RetireScore)
	default:
		return "Over " + formatScore(game.UnlockScore) + " needed for the next level"
	}
}

// flashOf describes an answer for the animation; st is the state after it.
func flashOf(o game.Outcome, st game.State) *flashView {
	f := &flashView{
		Symbol:  o.Symbol.String(),
		Correct: o.Correct,
		To:      formatScore(o.After),
		Delta:   formatDelta(int64(o.After) - int64(o.Before)),
	}
	switch {
	case o.Completed:
		f.Message = "All levels done!"
	case o.LevelUp:
		f.Message = fmt.Sprintf("Level %d unlocked!", st.Batch)
	case o.Retired && int(st.Batch) == len(game.Batches):
		f.Message = "Retired"
	case o.Retired:
		f.Message = "Retired until the next level"
	}
	return f
}

func playURL(k kids.Kid) string {
	return (&url.URL{Path: "/kids/" + k.Slug + "/play"}).String()
}

func (s *Server) notFound(w http.ResponseWriter) {
	s.ui.Error(w, http.StatusNotFound, "That page doesn't exist.")
}
