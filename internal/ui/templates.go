// Package ui embeds and parses HTML templates.
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

// Shared templates are parsed into every page set. Every non-base file is
// directly renderable by its file name, which is how HTMX fragments are served.
var shared = []string{"base.html", "tags_fragment.html"}

var funcs = template.FuncMap{
	// shortDigest renders "sha256:0123456789ab" for display.
	"shortDigest": func(d string) string {
		if len(d) > 19 {
			return d[:19] + "…"
		}
		return d
	},
}

// Templates holds one independent template set per page.
//
// Pages override the "title" and "content" blocks of base.html. Parsing all
// pages into a single set would make the last-parsed page's blocks win for
// every page, so each page is parsed into its own set.
type Templates struct {
	sets map[string]*template.Template
}

// Load parses all embedded HTML templates.
func Load() (*Templates, error) {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, err
	}
	t := &Templates{sets: map[string]*template.Template{}}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".html") || name == "base.html" {
			continue
		}
		files := []string{path.Join("templates", name)}
		for _, s := range shared {
			if s != name {
				files = append(files, path.Join("templates", s))
			}
		}
		set, err := template.New(name).Funcs(funcs).ParseFS(templateFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		t.sets[name] = set
	}
	return t, nil
}

// Render executes the named page into a buffer and writes it to w only on
// success, so template errors never produce half-written pages.
func (t *Templates) Render(w http.ResponseWriter, name string, data any) error {
	set, ok := t.sets[name]
	if !ok {
		return fmt.Errorf("unknown template %q", name)
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, name, data); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}
