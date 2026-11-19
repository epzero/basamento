package inspector

import (
	"embed"
	"fmt"
	"html/template"
)

//go:embed templates/*.html assets/*
var assetsFS embed.FS

// parseTemplates loads the server-rendered pages from the embedded assets.
func parseTemplates() (map[string]*template.Template, error) {
	names := []string{"list.html", "run.html", "denied.html"}
	out := make(map[string]*template.Template, len(names))
	for _, name := range names {
		tmpl, err := template.New(name).ParseFS(assetsFS, "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("inspector: parse %s: %w", name, err)
		}
		out[name] = tmpl
	}
	return out, nil
}
