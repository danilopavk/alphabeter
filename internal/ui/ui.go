// Package ui holds the HTML templates and static assets shared by the identity
// provider and the web app.
package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// shared are the templates parsed into every page: the layout and the partials
// more than one page uses.
var shared = []string{"templates/layout.html", "templates/board.html"}

// Renderer renders pages (templates/<page>.html inside layout.html) and named
// fragments of them for htmx swaps.
type Renderer struct {
	pages map[string]*template.Template
}

func NewRenderer() *Renderer {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		panic(err)
	}
	pages := map[string]*template.Template{}
	for _, name := range names {
		if slices.Contains(shared, name) {
			continue
		}
		page := strings.TrimSuffix(path.Base(name), ".html")
		pages[page] = template.Must(template.New(page).Funcs(template.FuncMap{"asset": Asset}).
			ParseFS(templateFS, append(slices.Clone(shared), name)...))
	}
	return &Renderer{pages: pages}
}

// Page renders a full HTML document.
func (r *Renderer) Page(w http.ResponseWriter, status int, page string, data any) {
	r.execute(w, status, page, "layout", data)
}

// Fragment renders one {{define}} block of a page, for htmx swaps.
func (r *Renderer) Fragment(w http.ResponseWriter, status int, page, block string, data any) {
	r.execute(w, status, page, block, data)
}

func (r *Renderer) execute(w http.ResponseWriter, status int, page, name string, data any) {
	t, ok := r.pages[page]
	if !ok {
		panic("ui: unknown page " + page)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render", "page", page, "template", name, "err", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// Error renders the generic error page.
func (r *Renderer) Error(w http.ResponseWriter, status int, message string) {
	r.Page(w, status, "error", struct{ Message string }{message})
}

// assetVersions maps each static file to a short hash of its content, so its
// URL changes whenever the file does and browsers never use a stale copy.
var assetVersions = func() map[string]string {
	versions := map[string]string{}
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		versions[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(sum[:4])
		return nil
	})
	if err != nil {
		panic(err)
	}
	return versions
}()

// Asset returns the versioned URL of a static file, e.g. /static/app.css?v=1a2b3c4d.
func Asset(name string) string {
	v, ok := assetVersions[name]
	if !ok {
		panic("ui: unknown asset " + name)
	}
	return "/static/" + name + "?v=" + v
}

// Static serves /static/*. Versioned URLs (see Asset) are cached forever;
// anything else must be revalidated.
func Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		if v := r.URL.Query().Get("v"); v != "" && v == assetVersions[name] {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
