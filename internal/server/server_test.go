package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElkoAI/elko-feedback/internal/store"
	"github.com/ElkoAI/elko-feedback/pkg/client"
)

func setup(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	for _, id := range []string{"elko.mcp.news", "elko.mcp.market", "elko.project-wizard"} {
		c := store.Channel{ID: id, Title: id, Enabled: true, RatePerHour: 20, AllowedOrigins: "https://hub.docker.com",
			Fields: store.Fields{Rating: true, Email: true, Category: []string{"bug", "idea"}}}
		if err := st.UpsertChannel(&c); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateAdmin("john", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	srv := New(st)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func postJSON(t *testing.T, c *http.Client, u, body string, hdr ...string) (*http.Response, map[string]any) {
	req, _ := http.NewRequest("POST", u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp, m
}

func TestPublicSubmitAndValidation(t *testing.T) {
	ts, st := setup(t)
	c := ts.Client()
	r, m := postJSON(t, c, ts.URL+"/v1/feedback", `{"channel":"elko.mcp.news","message":"love it","rating":5,"app_version":"1.2"}`, "Origin", "https://hub.docker.com")
	if r.StatusCode != 201 || m["id"] == nil {
		t.Fatalf("submit: %d %v", r.StatusCode, m)
	}
	if r.Header.Get("Access-Control-Allow-Origin") != "https://hub.docker.com" {
		t.Fatal("CORS header missing for allowed origin")
	}
	for body, code := range map[string]int{
		`{"channel":"elko.mcp.news","message":"  "}`:               400,
		`{"channel":"nope","message":"x"}`:                         404,
		`{"channel":"elko.mcp.news","message":"x","rating":9}`:     400,
		`{"channel":"elko.mcp.news","message":"x","email":"no"}`:   400,
		`{"channel":"elko.mcp.news","message":"x","category":"z"}`: 400,
	} {
		if r, _ := postJSON(t, c, ts.URL+"/v1/feedback", body); r.StatusCode != code {
			t.Errorf("%s: got %d want %d", body, r.StatusCode, code)
		}
	}
	items, total, _ := st.ListFeedback(store.Filter{})
	if total != 1 || items[0].IPHash == "" || strings.Contains(items[0].IPHash, "127.0.0.1") || items[0].AppVersion != "1.2" {
		t.Fatalf("stored: %+v", items)
	}
}

func TestHoneypotRateLimitAndSize(t *testing.T) {
	ts, st := setup(t)
	c := ts.Client()
	m, _ := st.Channel("elko.mcp.market")
	m.RatePerHour = 3
	_ = st.UpsertChannel(m)
	if r, _ := postJSON(t, c, ts.URL+"/v1/feedback", `{"channel":"elko.mcp.market","message":"spam","website":"http://x"}`); r.StatusCode != 201 {
		t.Fatal("honeypot should look like success")
	}
	if _, n, _ := st.ListFeedback(store.Filter{}); n != 0 {
		t.Fatal("honeypot submission was stored")
	}
	big := strings.Repeat("a", 9000)
	if r, _ := postJSON(t, c, ts.URL+"/v1/feedback", `{"channel":"elko.mcp.market","message":"`+big+`"}`); r.StatusCode != 413 {
		t.Fatalf("size cap: %d", r.StatusCode)
	}
	codes := []int{}
	for i := 0; i < 4; i++ {
		r, _ := postJSON(t, c, ts.URL+"/v1/feedback", `{"channel":"elko.mcp.market","message":"m"}`)
		codes = append(codes, r.StatusCode)
	}
	if codes[2] != 201 || codes[3] != 429 {
		t.Fatalf("rate limit codes %v", codes)
	}
}

func TestHTMLFormNoJS(t *testing.T) {
	ts, _ := setup(t)
	c := ts.Client()
	resp, _ := c.Get(ts.URL + "/f/elko.project-wizard")
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `name="message"`) || !strings.Contains(string(b), `name="website"`) {
		t.Fatalf("form: %d", resp.StatusCode)
	}
	resp, _ = c.PostForm(ts.URL+"/f/elko.project-wizard", url.Values{"message": {"works without JS"}, "rating": {"4"}})
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Thank you") {
		t.Fatalf("thanks: %d %s", resp.StatusCode, b)
	}
	resp, _ = c.PostForm(ts.URL+"/f/elko.project-wizard", url.Values{"message": {""}, "email": {"bad"}})
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !strings.Contains(string(b), "message is required") {
		t.Fatalf("error re-render: %d", resp.StatusCode)
	}
}

func TestAdminSessionAndAPIKey(t *testing.T) {
	ts, st := setup(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if r, _ := c.Get(ts.URL + "/v1/admin/feedback"); r.StatusCode != 401 {
		t.Fatal("expected 401 before login")
	}
	if r, _ := postJSON(t, c, ts.URL+"/v1/admin/login", `{"username":"john","password":"wrong-password"}`); r.StatusCode != 401 {
		t.Fatal("bad password accepted")
	}
	if r, _ := postJSON(t, c, ts.URL+"/v1/admin/login", `{"username":"john","password":"correct-horse-battery"}`); r.StatusCode != 200 {
		t.Fatal("login failed")
	}
	for _, ch := range []string{"elko.mcp.news", "elko.mcp.market", "elko.project-wizard"} {
		postJSON(t, ts.Client(), ts.URL+"/v1/feedback", `{"channel":"`+ch+`","message":"hello `+ch+`","rating":3}`)
	}
	get := func(c *http.Client, path string, hdr ...string) map[string]any {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		r, err := c.Do(req)
		if err != nil || r.StatusCode != 200 {
			t.Fatalf("GET %s: %v %v", path, err, r.StatusCode)
		}
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		return m
	}
	if n := get(c, "/v1/admin/feedback?prefix=elko.mcp")["total"].(float64); n != 2 {
		t.Fatalf("prefix filter total=%v", n)
	}
	if n := get(c, "/v1/admin/feedback?prefix=elko")["total"].(float64); n != 3 {
		t.Fatalf("root prefix total=%v", n)
	}
	// patch status/tags/notes
	items, _, _ := st.ListFeedback(store.Filter{Prefix: "elko.mcp.news"})
	req, _ := http.NewRequest("PATCH", ts.URL+"/v1/admin/feedback/"+itoa(items[0].ID), strings.NewReader(`{"status":"triaged","tags":["Bug","ui"],"notes":"look"}`))
	req.Header.Set("Content-Type", "application/json")
	if r, _ := c.Do(req); r.StatusCode != 200 {
		t.Fatalf("patch %d", r.StatusCode)
	}
	if n := get(c, "/v1/admin/feedback?tag=bug&status=triaged")["total"].(float64); n != 1 {
		t.Fatalf("tag filter total=%v", n)
	}
	// cross-origin cookie write is rejected
	if r, _ := postJSON(t, c, ts.URL+"/v1/admin/keys", `{"name":"x"}`, "Origin", "https://evil.example"); r.StatusCode != 403 {
		t.Fatalf("CSRF guard: %d", r.StatusCode)
	}
	// API key
	r, k := postJSON(t, c, ts.URL+"/v1/admin/keys", `{"name":"mcp"}`)
	if r.StatusCode != 201 || !strings.HasPrefix(k["key"].(string), "efk_") {
		t.Fatalf("key create %d", r.StatusCode)
	}
	if n := get(&http.Client{}, "/v1/admin/feedback", "Authorization", "Bearer "+k["key"].(string))["total"].(float64); n != 3 {
		t.Fatal("api key list failed")
	}
	if s := get(c, "/v1/admin/settings"); s["ip_salt"] != nil {
		t.Fatal("ip_salt exposed")
	}
	// export
	resp, _ := c.Get(ts.URL + "/v1/admin/export?format=csv&prefix=elko.mcp")
	b, _ := io.ReadAll(resp.Body)
	if lines := strings.Count(strings.TrimSpace(string(b)), "\n"); lines != 2 {
		t.Fatalf("csv rows=%d\n%s", lines, b)
	}
	resp, _ = c.Get(ts.URL + "/v1/admin/export?format=json")
	var arr []any
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil || len(arr) != 3 {
		t.Fatalf("json export %v %d", err, len(arr))
	}
	// channel CRUD
	req, _ = http.NewRequest("PUT", ts.URL+"/v1/admin/channels/elko.test", strings.NewReader(`{"title":"T","enabled":true}`))
	if r, _ := c.Do(req); r.StatusCode != 200 {
		t.Fatalf("channel put %d", r.StatusCode)
	}
	req, _ = http.NewRequest("PUT", ts.URL+"/v1/admin/channels/Bad..Id", strings.NewReader(`{"title":"T"}`))
	if r, _ := c.Do(req); r.StatusCode != 400 {
		t.Fatalf("bad id accepted %d", r.StatusCode)
	}
	req, _ = http.NewRequest("DELETE", ts.URL+"/v1/admin/channels/elko.test", nil)
	if r, _ := c.Do(req); r.StatusCode != 200 {
		t.Fatal("channel delete")
	}
	// logout kills session
	postJSON(t, c, ts.URL+"/v1/admin/logout", `{}`)
	if r, _ := c.Get(ts.URL + "/v1/admin/me"); r.StatusCode != 401 {
		t.Fatal("session survived logout")
	}
}

func TestGoClient(t *testing.T) {
	ts, st := setup(t)
	cl := client.New(ts.URL)
	r := 4
	id, err := cl.Submit(context.Background(), client.Feedback{Channel: "elko.mcp.news", Message: "from mcp", Rating: &r, AppVersion: "0.9"})
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	fb, _ := st.GetFeedback(id)
	if fb.Source != "mcp" {
		t.Fatalf("source=%q", fb.Source)
	}
	if _, err := cl.Submit(context.Background(), client.Feedback{Channel: "nope", Message: "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestTurnstile(t *testing.T) {
	ts, st := setup(t)
	ch, _ := st.Channel("elko.mcp.news")
	ch.TurnstileSecret, ch.TurnstileSiteKey = "sec", "site"
	_ = st.UpsertChannel(ch)
	_ = ts
	srv := New(st)
	srv.verifyTS = func(secret, token, ip string) bool { return secret == "sec" && token == "good" }
	ts2 := httptest.NewServer(srv.Handler())
	defer ts2.Close()
	if r, _ := postJSON(t, ts2.Client(), ts2.URL+"/v1/feedback", `{"channel":"elko.mcp.news","message":"x"}`); r.StatusCode != 400 {
		t.Fatal("missing turnstile accepted")
	}
	if r, _ := postJSON(t, ts2.Client(), ts2.URL+"/v1/feedback", `{"channel":"elko.mcp.news","message":"x","cf-turnstile-response":"good"}`); r.StatusCode != 201 {
		t.Fatal("valid turnstile rejected")
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
