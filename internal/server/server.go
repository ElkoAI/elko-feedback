// Package server implements the Elko Feedback HTTP API, the hosted
// public form and the admin dashboard (a static client of the API).
package server

import (
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ElkoAI/elko-feedback/internal/store"
)

//go:embed web
var webFS embed.FS

// Notifier is called after each accepted submission. The default is a
// no-op; email/Slack/webhook implementations plug in here later.
type Notifier interface {
	FeedbackCreated(ctx context.Context, ch *store.Channel, fb *store.Feedback)
}

type NopNotifier struct{}

func (NopNotifier) FeedbackCreated(context.Context, *store.Channel, *store.Feedback) {}

type Server struct {
	Store        *store.Store
	Notifier     Notifier
	TrustProxy   bool // read client IP from X-Forwarded-For / CF-Connecting-IP
	SecureCookie bool
	limiter      *limiter
	tmpl         *template.Template
	verifyTS     func(secret, token, ip string) bool
}

func New(st *store.Store) *Server {
	s := &Server{Store: st, Notifier: NopNotifier{}, limiter: newLimiter(), verifyTS: verifyTurnstile}
	s.tmpl = template.Must(template.New("").Funcs(template.FuncMap{
		// revseq(5) = [5 4 3 2 1]; stars are laid out row-reverse so
		// the CSS sibling selector can highlight without JS.
		"revseq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = n - i
			}
			return out
		},
	}).ParseFS(webFS, "web/*.html"))
	return s
}

const sessionCookie = "ef_session"

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	// public
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /f/{channel}", s.formPage)
	m.HandleFunc("POST /f/{channel}", s.formSubmit)
	m.HandleFunc("GET /v1/channels/{channel}", s.publicChannel)
	m.HandleFunc("POST /v1/feedback", s.apiSubmit)
	m.HandleFunc("OPTIONS /v1/feedback", s.preflight)
	// admin auth
	m.HandleFunc("POST /v1/admin/login", s.login)
	m.HandleFunc("POST /v1/admin/logout", s.logout)
	m.HandleFunc("GET /v1/admin/me", s.auth(s.me))
	// admin resources
	m.HandleFunc("GET /v1/admin/stats", s.auth(s.stats))
	m.HandleFunc("GET /v1/admin/channels", s.auth(s.listChannels))
	m.HandleFunc("POST /v1/admin/channels", s.auth(s.putChannel))
	m.HandleFunc("GET /v1/admin/channels/{channel}", s.auth(s.getChannel))
	m.HandleFunc("PUT /v1/admin/channels/{channel}", s.auth(s.putChannel))
	m.HandleFunc("DELETE /v1/admin/channels/{channel}", s.auth(s.deleteChannel))
	m.HandleFunc("GET /v1/admin/feedback", s.auth(s.listFeedback))
	m.HandleFunc("GET /v1/admin/feedback/{id}", s.auth(s.getFeedback))
	m.HandleFunc("PATCH /v1/admin/feedback/{id}", s.auth(s.patchFeedback))
	m.HandleFunc("DELETE /v1/admin/feedback/{id}", s.auth(s.deleteFeedback))
	m.HandleFunc("GET /v1/admin/export", s.auth(s.export))
	m.HandleFunc("GET /v1/admin/keys", s.auth(s.listKeys))
	m.HandleFunc("POST /v1/admin/keys", s.auth(s.createKey))
	m.HandleFunc("DELETE /v1/admin/keys/{id}", s.auth(s.deleteKey))
	m.HandleFunc("GET /v1/admin/settings", s.auth(s.getSettings))
	m.HandleFunc("PUT /v1/admin/settings", s.auth(s.putSettings))
	// dashboard (static client of the API)
	static, _ := fs.Sub(webFS, "web/static")
	m.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", http.FileServer(http.FS(static))))
	m.HandleFunc("GET /admin", s.dashboard)
	m.HandleFunc("GET /admin/", s.dashboard)
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin", http.StatusFound) })
	return securityHeaders(m)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		}
		h.ServeHTTP(w, r)
	})
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxy {
		if v := r.Header.Get("CF-Connecting-IP"); v != "" {
			return strings.TrimSpace(v)
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- rate limiting (policy per channel from DB, counters in memory) ----------

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

func (l *limiter) allow(key string, perHour int) bool {
	if perHour <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-time.Hour)
	h := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			h = append(h, t)
		}
	}
	if len(h) >= perHour {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, time.Now())
	return true
}

// ---------- submission core ----------

type submission struct {
	Channel     string            `json:"channel"`
	Message     string            `json:"message"`
	Rating      *int              `json:"rating"`
	Category    string            `json:"category"`
	Email       string            `json:"email"`
	Custom      map[string]string `json:"custom"`
	AppVersion  string            `json:"app_version"`
	Source      string            `json:"source"`
	Website     string            `json:"website"` // honeypot: must stay empty
	Turnstile   string            `json:"cf-turnstile-response"`
	PageReferer string            `json:"referrer"`
}

type submitError struct {
	code int
	msg  string
}

func (e submitError) Error() string { return e.msg }

func (s *Server) accept(r *http.Request, sub submission) (*store.Channel, *store.Feedback, error) {
	ch, err := s.Store.Channel(sub.Channel)
	if err != nil || !ch.Enabled {
		return nil, nil, submitError{404, "unknown or disabled channel"}
	}
	ip := s.clientIP(r)
	ipHash := s.Store.IPHash(ip)
	if sub.Website != "" { // honeypot tripped: pretend success, store nothing
		return ch, nil, nil
	}
	if !s.limiter.allow(ch.ID+"|"+ipHash, ch.RatePerHour) {
		return ch, nil, submitError{429, "too many submissions, please try again later"}
	}
	if ch.TurnstileSecret != "" && !s.verifyTS(ch.TurnstileSecret, sub.Turnstile, ip) {
		return ch, nil, submitError{400, "bot check failed"}
	}
	sub.Message = strings.TrimSpace(sub.Message)
	if sub.Message == "" {
		return ch, nil, submitError{400, "message is required"}
	}
	f := ch.Fields
	if f.MessageMin > 0 && len([]rune(sub.Message)) < f.MessageMin {
		return ch, nil, submitError{400, fmt.Sprintf("message must be at least %d characters", f.MessageMin)}
	}
	if f.MessageMax > 0 && len([]rune(sub.Message)) > f.MessageMax {
		return ch, nil, submitError{400, fmt.Sprintf("message must be at most %d characters", f.MessageMax)}
	}
	maxRating := 5
	fmt.Sscan(s.Store.Setting("max_rating"), &maxRating)
	if sub.Rating != nil && (*sub.Rating < 1 || *sub.Rating > maxRating) {
		return ch, nil, submitError{400, fmt.Sprintf("rating must be 1..%d", maxRating)}
	}
	if !f.Rating {
		sub.Rating = nil
	}
	if sub.Email = strings.TrimSpace(sub.Email); sub.Email != "" {
		if _, err := mail.ParseAddress(sub.Email); err != nil {
			return ch, nil, submitError{400, "invalid email address"}
		}
	}
	if len(f.Category) > 0 && sub.Category != "" {
		ok := false
		for _, c := range f.Category {
			ok = ok || c == sub.Category
		}
		if !ok {
			return ch, nil, submitError{400, "invalid category"}
		}
	}
	custom := map[string]string{}
	for _, cf := range f.Custom {
		v := strings.TrimSpace(sub.Custom[cf.Name])
		if cf.Required && v == "" {
			return ch, nil, submitError{400, cf.Label + " is required"}
		}
		if v != "" {
			custom[cf.Name] = v
		}
	}
	cj, _ := json.Marshal(custom)
	ref := sub.PageReferer
	if ref == "" {
		ref = r.Referer()
	}
	src := sub.Source
	if src == "" {
		src = "web"
	}
	fb := &store.Feedback{
		Channel: ch.ID, Message: sub.Message, Rating: sub.Rating, Category: sub.Category, Email: sub.Email,
		Custom: cj, Source: trunc(src, 32), AppVersion: trunc(sub.AppVersion, 64), UserAgent: trunc(r.UserAgent(), 400),
		Referrer: trunc(ref, 500), IPHash: ipHash,
	}
	if err := s.Store.InsertFeedback(fb); err != nil {
		return ch, nil, submitError{500, "could not store feedback"}
	}
	s.Notifier.FeedbackCreated(r.Context(), ch, fb)
	return ch, fb, nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func verifyTurnstile(secret, token, ip string) bool {
	if token == "" {
		return false
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).PostForm("https://challenges.cloudflare.com/turnstile/v0/siteverify",
		url.Values{"secret": {secret}, "response": {token}, "remoteip": {ip}})
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Success
}

// ---------- public endpoints ----------

func (s *Server) cors(w http.ResponseWriter, r *http.Request, ch *store.Channel) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	allowed := false
	if ch != nil {
		allowed = ch.OriginAllowed(origin)
	} else {
		allowed = s.Store.AnyOriginAllowed(origin)
	}
	if allowed {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "3600")
	}
}

func (s *Server) preflight(w http.ResponseWriter, r *http.Request) {
	s.cors(w, r, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) publicChannel(w http.ResponseWriter, r *http.Request) {
	ch, err := s.Store.Channel(r.PathValue("channel"))
	if err != nil || !ch.Enabled {
		apiErr(w, 404, "unknown or disabled channel")
		return
	}
	writeJSON(w, 200, map[string]any{"id": ch.ID, "title": ch.Title, "description": ch.Description, "accent_color": ch.AccentColor,
		"logo_url": ch.LogoURL, "fields": ch.Fields, "turnstile_sitekey": ch.TurnstileSiteKey, "form_url": "/f/" + ch.ID})
}

func (s *Server) apiSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var sub submission
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
			apiErr(w, 400, "invalid JSON body")
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			apiErr(w, 400, "invalid form body")
			return
		}
		sub = subFromForm(r.PostForm)
	}
	var ch *store.Channel
	if c, err := s.Store.Channel(sub.Channel); err == nil {
		ch = c
		if len(sub.Message)+len(sub.Email)+len(sub.Category) > ch.MaxBytes {
			s.cors(w, r, ch)
			apiErr(w, 413, "submission too large")
			return
		}
	}
	s.cors(w, r, ch)
	_, fb, err := s.accept(r, sub)
	var se submitError
	if errors.As(err, &se) {
		apiErr(w, se.code, se.msg)
		return
	}
	if fb == nil { // honeypot
		writeJSON(w, 201, map[string]any{"ok": true})
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "id": fb.ID, "created_at": fb.CreatedAt})
}

func subFromForm(v url.Values) submission {
	sub := submission{
		Channel: v.Get("channel"), Message: v.Get("message"), Category: v.Get("category"), Email: v.Get("email"),
		AppVersion: v.Get("app_version"), Source: v.Get("source"), Website: v.Get("website"),
		Turnstile: v.Get("cf-turnstile-response"), PageReferer: v.Get("referrer"), Custom: map[string]string{},
	}
	if rs := v.Get("rating"); rs != "" {
		if n, err := strconv.Atoi(rs); err == nil {
			sub.Rating = &n
		}
	}
	for k := range v {
		if name, ok := strings.CutPrefix(k, "custom."); ok {
			sub.Custom[name] = v.Get(k)
		}
	}
	return sub
}

type formData struct {
	Ch        *store.Channel
	Error     string
	Values    url.Values
	MaxRating int
	Referrer  string
	AppVer    string
	Thanks    string
}

func (s *Server) formPage(w http.ResponseWriter, r *http.Request) {
	ch, err := s.Store.Channel(r.PathValue("channel"))
	if err != nil || !ch.Enabled {
		http.Error(w, "Unknown feedback channel", http.StatusNotFound)
		return
	}
	s.renderForm(w, 200, ch, "", r.URL.Query(), r.Referer())
}

func (s *Server) renderForm(w http.ResponseWriter, code int, ch *store.Channel, msg string, vals url.Values, ref string) {
	mr := 5
	fmt.Sscan(s.Store.Setting("max_rating"), &mr)
	if vals.Get("referrer") != "" {
		ref = vals.Get("referrer")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := s.tmpl.ExecuteTemplate(w, "form.html", formData{Ch: ch, Error: msg, Values: vals, MaxRating: mr, Referrer: ref,
		AppVer: firstNonEmpty(vals.Get("app_version"), vals.Get("v"))}); err != nil {
		log.Printf("render form: %v", err)
	}
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func (s *Server) formSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	sub := subFromForm(r.PostForm)
	sub.Channel = r.PathValue("channel")
	ch, fb, err := s.accept(r, sub)
	var se submitError
	if errors.As(err, &se) {
		if ch == nil {
			http.Error(w, se.msg, se.code)
			return
		}
		s.renderForm(w, se.code, ch, se.msg, r.PostForm, sub.PageReferer)
		return
	}
	_ = fb
	thanks := ch.ThanksMessage
	if thanks == "" {
		thanks = "Thank you! Your feedback was received and will be read by a human."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "thanks.html", formData{Ch: ch, Thanks: thanks})
}

// ---------- admin auth ----------

type ctxKey struct{}

func (s *Server) principal(r *http.Request) (string, bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if name, ok := s.Store.CheckAPIKey(strings.TrimPrefix(h, "Bearer ")); ok {
			return "key:" + name, true
		}
		return "", false
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, ok := s.Store.SessionAdmin(c.Value); ok {
			return "admin:" + u, true
		}
	}
	return "", false
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		who, ok := s.principal(r)
		if !ok {
			apiErr(w, 401, "authentication required")
			return
		}
		// CSRF guard for cookie sessions: state-changing requests must
		// come from this origin (API-key clients are exempt).
		if strings.HasPrefix(who, "admin:") && r.Method != http.MethodGet {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					apiErr(w, 403, "cross-origin request rejected")
					return
				}
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, who)))
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	} else {
		_ = r.ParseForm()
		in.Username, in.Password = r.PostForm.Get("username"), r.PostForm.Get("password")
	}
	if !s.limiter.allow("login|"+s.clientIP(r), 20) {
		apiErr(w, 429, "too many login attempts")
		return
	}
	tok, exp, err := s.Store.Login(in.Username, in.Password)
	if err != nil {
		apiErr(w, 401, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", Expires: exp, HttpOnly: true,
		Secure: s.SecureCookie, SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]any{"ok": true, "username": in.Username, "expires_at": exp, "token": tok})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"principal": r.Context().Value(ctxKey{})})
}

// ---------- admin resources ----------

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Stats()
	if err != nil {
		apiErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"channels": st})
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := s.Store.Channels(r.URL.Query().Get("prefix"))
	if err != nil {
		apiErr(w, 500, err.Error())
		return
	}
	for i := range chs {
		if chs[i].TurnstileSecret != "" {
			chs[i].TurnstileSecret = "********"
		}
	}
	writeJSON(w, 200, map[string]any{"channels": chs})
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	ch, err := s.Store.Channel(r.PathValue("channel"))
	if err != nil {
		apiErr(w, 404, "channel not found")
		return
	}
	if ch.TurnstileSecret != "" {
		ch.TurnstileSecret = "********"
	}
	writeJSON(w, 200, ch)
}

func (s *Server) putChannel(w http.ResponseWriter, r *http.Request) {
	var c store.Channel
	c.Enabled = true
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&c); err != nil {
		apiErr(w, 400, "invalid JSON body")
		return
	}
	if id := r.PathValue("channel"); id != "" {
		c.ID = id
	}
	if c.TurnstileSecret == "********" { // unchanged masked secret
		if old, err := s.Store.Channel(c.ID); err == nil {
			c.TurnstileSecret = old.TurnstileSecret
		}
	}
	if err := s.Store.UpsertChannel(&c); err != nil {
		apiErr(w, 400, err.Error())
		return
	}
	ch, _ := s.Store.Channel(c.ID)
	if ch.TurnstileSecret != "" {
		ch.TurnstileSecret = "********"
	}
	writeJSON(w, 200, ch)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteChannel(r.PathValue("channel")); err != nil {
		apiErr(w, 404, "channel not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func filterFrom(q url.Values) store.Filter {
	f := store.Filter{Prefix: q.Get("prefix"), Since: q.Get("since"), Until: q.Get("until"), Status: q.Get("status"), Tag: q.Get("tag"), Q: q.Get("q")}
	f.MinRating, _ = strconv.Atoi(q.Get("min_rating"))
	f.MaxRating, _ = strconv.Atoi(q.Get("max_rating"))
	if r, err := strconv.Atoi(q.Get("rating")); err == nil {
		f.MinRating, f.MaxRating = r, r
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	return f
}

func (s *Server) listFeedback(w http.ResponseWriter, r *http.Request) {
	f := filterFrom(r.URL.Query())
	items, total, err := s.Store.ListFeedback(f)
	if err != nil {
		apiErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "limit": f.Limit, "offset": f.Offset})
}

func idParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *Server) getFeedback(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	fb, err := s.Store.GetFeedback(id)
	if !ok || err != nil {
		apiErr(w, 404, "feedback not found")
		return
	}
	writeJSON(w, 200, fb)
}

func (s *Server) patchFeedback(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(r)
	if !ok {
		apiErr(w, 404, "feedback not found")
		return
	}
	var p store.FeedbackPatch
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
		apiErr(w, 400, "invalid JSON body")
		return
	}
	fb, err := s.Store.PatchFeedback(id, p)
	if errors.Is(err, store.ErrNotFound) {
		apiErr(w, 404, "feedback not found")
		return
	}
	if err != nil {
		apiErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, fb)
}

func (s *Server) deleteFeedback(w http.ResponseWriter, r *http.Request) {
	id, _ := idParam(r)
	if err := s.Store.DeleteFeedback(id); err != nil {
		apiErr(w, 404, "feedback not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	f := filterFrom(r.URL.Query())
	stamp := time.Now().UTC().Format("20060102-150405")
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="feedback-`+stamp+`.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "created_at", "channel", "status", "rating", "category", "email", "message", "tags", "notes", "app_version", "source", "referrer", "user_agent", "custom"})
		_ = s.Store.EachFeedback(f, func(fb *store.Feedback) error {
			rating := ""
			if fb.Rating != nil {
				rating = strconv.Itoa(*fb.Rating)
			}
			return cw.Write([]string{strconv.FormatInt(fb.ID, 10), fb.CreatedAt, fb.Channel, fb.Status, rating, fb.Category, fb.Email,
				csvSafe(fb.Message), strings.Join(fb.Tags, ";"), csvSafe(fb.Notes), fb.AppVersion, fb.Source, fb.Referrer, fb.UserAgent, string(fb.Custom)})
		})
		cw.Flush()
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="feedback-`+stamp+`.json"`)
	_, _ = io.WriteString(w, "[")
	first := true
	enc := json.NewEncoder(w)
	_ = s.Store.EachFeedback(f, func(fb *store.Feedback) error {
		if !first {
			_, _ = io.WriteString(w, ",")
		}
		first = false
		return enc.Encode(fb)
	})
	_, _ = io.WriteString(w, "]")
}

// csvSafe neutralises spreadsheet formula injection.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	ks, err := s.Store.APIKeys()
	if err != nil {
		apiErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"keys": ks})
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	if in.Name == "" {
		apiErr(w, 400, "name is required")
		return
	}
	k, err := s.Store.CreateAPIKey(in.Name)
	if err != nil {
		apiErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, k)
}

func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	id, _ := idParam(r)
	if err := s.Store.DeleteAPIKey(id); err != nil {
		apiErr(w, 404, "key not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Settings()
	if err != nil {
		apiErr(w, 500, err.Error())
		return
	}
	delete(st, "ip_salt") // never exposed
	writeJSON(w, 200, st)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		apiErr(w, 400, "invalid JSON body")
		return
	}
	for k, v := range in {
		if err := s.Store.SetSetting(k, v); err != nil {
			apiErr(w, 400, err.Error())
			return
		}
	}
	s.getSettings(w, r)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	b, _ := webFS.ReadFile("web/static/admin.html")
	_, _ = w.Write(b)
}
