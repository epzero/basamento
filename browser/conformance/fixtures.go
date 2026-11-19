package conformance

import (
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
)

//go:embed testdata/features/*/*.feature
var featureFS embed.FS

// Fixture app names.
const (
	// AppTodo is the todo app: one list, one new-item field and one delete
	// button per item.
	AppTodo = "todo"
	// AppLogin is the login app: a user field and a password field.
	AppLogin = "login"
)

// App is a reference fixture app: one small page a runner navigates to. The
// zero value is not usable; take one from Fixture.
type App struct {
	// Name is the fixture's name.
	Name string
	// HTML is the app's page.
	HTML string
	// BrokenDelete makes the delete control in the todo app fail.
	BrokenDelete bool

	server *httptest.Server
}

// Fixture returns the named reference app.
func Fixture(name string) (*App, error) {
	switch name {
	case AppTodo:
		return &App{Name: AppTodo, HTML: todoHTML}, nil
	case AppLogin:
		return &App{Name: AppLogin, HTML: loginHTML}, nil
	default:
		return nil, fmt.Errorf("unknown fixture app %q", name)
	}
}

// WithBrokenDelete returns a todo app whose delete button fails.
func (a *App) WithBrokenDelete() *App {
	broken := *a
	broken.BrokenDelete = true
	return &broken
}

// Start serves the app on a loopback address and returns its base URL in URL.
func (a *App) Start() error {
	if a.server != nil {
		return nil
	}
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(a.HTML))
	}))
	return nil
}

// URL is the app's base URL; call Start first.
func (a *App) URL() string {
	if a.server == nil {
		return ""
	}
	return a.server.URL
}

// Close stops serving the app.
func (a *App) Close() {
	if a.server != nil {
		a.server.Close()
		a.server = nil
	}
}

// Feature returns the text of one reference feature, named as the scenarios
// name it, e.g. "todo/add-item.feature".
func Feature(name string) (string, error) {
	b, err := featureFS.ReadFile("testdata/features/" + name)
	if err != nil {
		return "", fmt.Errorf("unknown feature %q", name)
	}
	return string(b), nil
}

// FeatureNames lists the reference features, sorted.
func FeatureNames() []string {
	var names []string
	entries, err := featureFS.ReadDir("testdata/features")
	if err != nil {
		return nil
	}
	for _, dir := range entries {
		files, err := featureFS.ReadDir("testdata/features/" + dir.Name())
		if err != nil {
			continue
		}
		for _, f := range files {
			names = append(names, dir.Name()+"/"+f.Name())
		}
	}
	sort.Strings(names)
	return names
}

// ScenarioName returns the name of the first scenario in a feature.
func ScenarioName(feature string) string {
	for _, line := range strings.Split(feature, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Scenario:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Scenario:"))
		}
	}
	return ""
}

// Steps returns the step lines of a feature in order, keywords included and
// surrounding whitespace removed.
func Steps(feature string) []string {
	var steps []string
	for _, line := range strings.Split(feature, "\n") {
		line = strings.TrimSpace(line)
		for _, keyword := range []string{"Given ", "When ", "Then ", "And ", "But "} {
			if strings.HasPrefix(line, keyword) {
				steps = append(steps, line)
				break
			}
		}
	}
	return steps
}

const todoHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Todo</title></head>
<body>
  <h1>Todo</h1>
  <input id="new-item" name="new-item" type="text" aria-label="New item">
  <button id="add">Add</button>
  <ul id="items"><li>an existing item <button class="delete">Delete</button></li></ul>
</body>
</html>
`

const loginHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Sign in</title></head>
<body>
  <h1>Sign in</h1>
  <input id="user" name="user" type="text" aria-label="User">
  <input id="password" name="password" type="password" aria-label="Password">
  <button id="sign-in">Sign in</button>
</body>
</html>
`
