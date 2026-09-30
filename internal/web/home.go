package web

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/danilopavk/alphabeter/internal/game"
)

// seo is what search engines and link previews show for a public page. Every
// other page is marked noindex (see layout.html).
type seo struct {
	Title, Description string
	URL                string // canonical, absolute
}

// publicPages are the pages search engines may index, in the sitemap.
var publicPages = []string{"/", "/privacy"}

type homePage struct {
	SEO        seo
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
		SEO: seo{
			Title: "Learn letters and numbers with your toddler",
			Description: "A letters-and-numbers game for toddlers, played with a parent. Your kid names the big " +
				"letter on screen, you tap Yes or No, and the game adapts to what they know.",
			URL: s.cfg.BaseURL + "/",
		},
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

// privacyPage fills in how long the sign-in cookies last.
type privacyPage struct {
	SEO                          seo
	AccessTTL, RefreshTTL, TxTTL string
}

func (s *Server) privacy(w http.ResponseWriter, r *http.Request) {
	s.ui.Page(w, http.StatusOK, "privacy", privacyPage{
		SEO: seo{
			Title:       "Privacy",
			Description: "What Alphabeter stores about you and your kids, why, who else handles it, and for how long.",
			URL:         s.cfg.BaseURL + "/privacy",
		},
		AccessTTL:  humanDuration(s.idp.AccessTTL()),
		RefreshTTL: humanDuration(s.idp.RefreshTTL()),
		TxTTL:      humanDuration(txTTL),
	})
}

// humanDuration writes a duration in its largest whole unit: "30 days", "1 hour", "90 minutes".
func humanDuration(d time.Duration) string {
	n, unit := int64(d/time.Minute), "minute"
	switch {
	case d%(24*time.Hour) == 0:
		n, unit = int64(d/(24*time.Hour)), "day"
	case d%time.Hour == 0:
		n, unit = int64(d/time.Hour), "hour"
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}

// robots lets crawlers everywhere: pages that shouldn't be in search results
// say so themselves (noindex), which crawlers only see if they may fetch them.
func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nDisallow:\n\nSitemap: %s/sitemap.xml\n", s.cfg.BaseURL)
}

func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	type url struct {
		Loc string `xml:"loc"`
	}
	set := struct {
		XMLName xml.Name `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
		URLs    []url    `xml:"url"`
	}{}
	for _, path := range publicPages {
		set.URLs = append(set.URLs, url{s.cfg.BaseURL + path})
	}
	out, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	io.WriteString(w, xml.Header)
	w.Write(out)
}
