package api

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// The dashboard is split into index.html, style.css and small JS modules. Nothing compiles them
// together, so these tests catch the breakages the split makes possible: a file that is referenced
// but not shipped, a wrong content type (browsers refuse module scripts served as text/plain), or
// JS that looks up an element the HTML no longer has.

func (f *fixture) get(t *testing.T, path string) (string, string) {
	t.Helper()

	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", path, resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)

	return string(body), resp.Header.Get("Content-Type")
}

func TestDashboardAssetsAreServed(t *testing.T) {
	f := newFixture(t)

	html, ct := f.get(t, "/")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("/ content type = %q", ct)
	}

	// Everything the page references, then every module those import, transitively.
	refs := regexp.MustCompile(`(?:src|href)="([^"#]+)"`)
	imports := regexp.MustCompile(`from '\./([^']+)'`)

	queue := []string{}
	for _, m := range refs.FindAllStringSubmatch(html, -1) {
		queue = append(queue, m[1])
	}

	seen := map[string]bool{}
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if seen[path] {
			continue
		}
		seen[path] = true

		body, ct := f.get(t, "/"+path)
		switch {
		case strings.HasSuffix(path, ".css") && !strings.HasPrefix(ct, "text/css"):
			t.Errorf("%s served as %q", path, ct)
		case strings.HasSuffix(path, ".js"):
			if !strings.Contains(ct, "javascript") {
				t.Errorf("%s served as %q; browsers reject module scripts with that type", path, ct)
			}
			dir := path[:strings.LastIndex(path, "/")+1]
			for _, m := range imports.FindAllStringSubmatch(body, -1) {
				queue = append(queue, dir+m[1])
			}
		}
	}

	if !seen["style.css"] || !seen["js/main.js"] || len(seen) < 8 {
		t.Fatalf("expected the stylesheet, main.js and its modules to be reachable, got %v", seen)
	}
}

func TestDashboardScriptsOnlyUseElementsThatExist(t *testing.T) {
	f := newFixture(t)
	html, _ := f.get(t, "/")

	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		ids[m[1]] = true
	}

	used := regexp.MustCompile(`\$\('([^']+)'\)|getElementById\('([^']+)'\)`)
	for _, name := range []string{"add", "progress", "library", "search", "results", "main"} {
		src, _ := f.get(t, "/js/"+name+".js")
		for _, m := range used.FindAllStringSubmatch(src, -1) {
			id := m[1] + m[2]
			if !ids[id] {
				t.Errorf("js/%s.js uses #%s, which index.html does not have", name, id)
			}
		}
	}
}

func TestDashboardModulesOnlyTouchTheirOwnPart(t *testing.T) {
	f := newFixture(t)

	// Only api.js talks to the server; the others go through it.
	for _, name := range []string{"add", "progress", "library", "search", "results", "main", "poller", "state"} {
		src, _ := f.get(t, "/js/"+name+".js")
		if strings.Contains(src, "fetch(") || strings.Contains(src, "XMLHttpRequest") {
			t.Errorf("js/%s.js talks to the server directly; that belongs in api.js", name)
		}
	}
}
