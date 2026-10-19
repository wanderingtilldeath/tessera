package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wanderingtilldeath/docs-rag-ingest/internal/fakesite"
	"github.com/wanderingtilldeath/docs-rag-ingest/internal/testutil"
)

func freeAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	return l.Addr().String()
}

func apiJSON(t *testing.T, method, url, contentType string, body io.Reader) (int, map[string]any) {
	t.Helper()

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)

	return resp.StatusCode, out
}

// TestBatchOfURLsAndFilesIsSearchable is the whole feature through the real
// binary: a batch of URLs plus an uploaded file is crawled, chunked, embedded
// in the background (by a fake Ollama), and found by semantic search that is
// scoped to the batch.
func TestBatchOfURLsAndFilesIsSearchable(t *testing.T) {
	requireEnv(t)
	_, dbURL := testutil.PostgresWithURL(t)
	testutil.Redis(t)

	site := httptest.NewServer(fakesite.New(fakesite.Options{Pages: 10, Fanout: 2}))
	defer site.Close()
	ollama := testutil.FakeOllama(t)

	addr := freeAddr(t)
	p := startCrawlerx(t, dbURL, "-listen", addr, "-embed-url", ollama.URL, "-host-delay", "10ms")
	api := "http://" + addr

	waitFor(t, 20*time.Second, "API to come up", func() bool {
		resp, err := http.Get(api + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, func() string { return tail(p.out.String(), 2000) })

	// Batch 1: two crawled pages and one uploaded file.
	body, _ := json.Marshal(map[string]any{"name": "mixed", "urls": []string{site.URL + "/p/1", site.URL + "/p/2"}, "max_depth": 0})
	if status, out := apiJSON(t, "POST", api+"/batches", "application/json", bytes.NewReader(body)); status != http.StatusCreated {
		t.Fatalf("create batch = %d %v", status, out)
	}

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, _ := mw.CreateFormFile("files", "handbook.md")
	_, _ = io.WriteString(part, "# Handbook\n\n## Vacation\n\nEvery employee gets twenty five paid vacation days.\n")
	_ = mw.Close()
	if status, out := apiJSON(t, "POST", api+"/batches/1/files", mw.FormDataContentType(), &form); status != http.StatusOK {
		t.Fatalf("upload = %d %v", status, out)
	}

	// Batch 2 holds a different file only.
	if status, _ := apiJSON(t, "POST", api+"/batches", "application/json", strings.NewReader(`{"name":"other"}`)); status != http.StatusCreated {
		t.Fatal("create batch 2")
	}
	form.Reset()
	mw = multipart.NewWriter(&form)
	part, _ = mw.CreateFormFile("files", "menu.txt")
	_, _ = io.WriteString(part, "The canteen serves soup every Tuesday.")
	_ = mw.Close()
	if status, out := apiJSON(t, "POST", api+"/batches/2/files", mw.FormDataContentType(), &form); status != http.StatusOK {
		t.Fatalf("upload 2 = %d %v", status, out)
	}

	for _, id := range []int{1, 2} {
		waitFor(t, 30*time.Second, fmt.Sprintf("batch %d to finish", id), func() bool {
			_, out := apiJSON(t, "GET", fmt.Sprintf("%s/batches/%d", api, id), "", nil)
			return out["done"] == true
		}, func() string {
			_, out := apiJSON(t, "GET", fmt.Sprintf("%s/batches/%d", api, id), "", nil)
			return fmt.Sprintf("batch %d = %v\n%s", id, out, tail(p.out.String(), 2000))
		})
	}

	_, view := apiJSON(t, "GET", api+"/batches/1", "", nil)
	if view["crawls_done"] != float64(2) || view["chunks_total"] == float64(0) || view["chunks_total"] != view["chunks_embedded"] {
		t.Fatalf("batch 1 = %v", view)
	}

	search := func(q string, batch int) []string {
		t.Helper()
		u := api + "/search/semantic?q=" + url.QueryEscape(q)
		if batch > 0 {
			u += fmt.Sprintf("&batch=%d", batch)
		}
		status, out := apiJSON(t, "GET", u, "", nil)
		if status != http.StatusOK {
			t.Fatalf("search %q = %d %v", q, status, out)
		}
		var urls []string
		for _, h := range out["hits"].([]any) {
			urls = append(urls, h.(map[string]any)["url"].(string))
		}
		return urls
	}

	if urls := search("how many vacation days do employees get", 1); len(urls) == 0 || urls[0] != "upload://1/handbook.md" {
		t.Errorf("batch 1 search = %v, want the handbook first", urls)
	}
	for _, u := range search("soup canteen Tuesday", 2) {
		if !strings.HasPrefix(u, "upload://2/") {
			t.Errorf("batch 2 search leaked %s from outside the batch", u)
		}
	}
	for _, u := range search("vacation days", 2) {
		if strings.Contains(u, "handbook") {
			t.Errorf("batch 2 search returned batch 1's file %s", u)
		}
	}
	if urls := search("soup canteen Tuesday", 0); len(urls) == 0 || urls[0] != "upload://2/menu.txt" {
		t.Errorf("all-batches search = %v", urls)
	}

	// A crawled page is found by its own text. Crawl 1 is the batch's first URL.
	resp, err := http.Get(api + "/crawls/1/export")
	if err != nil {
		t.Fatal(err)
	}
	line, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var chunk struct{ URL, Text string }
	if err := json.Unmarshal(bytes.SplitN(line, []byte("\n"), 2)[0], &chunk); err != nil || chunk.Text == "" {
		t.Fatalf("export line %q: %v", line, err)
	}
	if urls := search(chunk.Text, 1); len(urls) == 0 || urls[0] != chunk.URL {
		t.Errorf("searching for %s's own text returned %v", chunk.URL, urls)
	}
	if !strings.HasSuffix(chunk.URL, "/p/1") {
		t.Errorf("crawl 1 exported %s, want the /p/1 page", chunk.URL)
	}
}
