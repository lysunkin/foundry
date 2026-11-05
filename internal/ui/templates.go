// Package ui embeds and parses HTML templates.
package ui

import (
	"embed"
	"html/template"
	"io/fs"
)

//go:embed templates/*.html
var templateFS embed.FS

// Load parses all embedded HTML templates and returns the set.
func Load() (*template.Template, error) {
	tmpl := template.New("")

	err := fs.WalkDir(templateFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}
		name := d.Name()
		_, err = tmpl.New(name).Parse(string(content))
		return err
	})
	if err != nil {
		return nil, err
	}
	return tmpl, nil
}
