// Package store is the SQLite persistence layer for Elko Feedback.
// All configuration and policy (channels, limits, CORS, keys, settings)
// lives in the database; nothing is hardcoded in the binary.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// ChannelIDRe validates dotted, hierarchical channel ids such as
// "elko.mcp.news" or "elko.cruz.beta".
var ChannelIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)*$`)

type Store struct{ DB *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS channels (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  accent_color TEXT NOT NULL DEFAULT '#f5b400',
  logo_url TEXT NOT NULL DEFAULT '',
  fields_json TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1,
  rate_per_hour INTEGER NOT NULL DEFAULT 10,
  max_bytes INTEGER NOT NULL DEFAULT 8000,
  allowed_origins TEXT NOT NULL DEFAULT '',
  turnstile_sitekey TEXT NOT NULL DEFAULT '',
  turnstile_secret TEXT NOT NULL DEFAULT '',
  thanks_message TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS feedback (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel TEXT NOT NULL,
  message TEXT NOT NULL,
  rating INTEGER,
  category TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  custom_json TEXT NOT NULL DEFAULT '{}',
  source TEXT NOT NULL DEFAULT 'web',
  app_version TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  referrer TEXT NOT NULL DEFAULT '',
  ip_hash TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'new',
  tags TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_feedback_channel ON feedback(channel, created_at);
CREATE INDEX IF NOT EXISTS idx_feedback_created ON feedback(created_at);
CREATE TABLE IF NOT EXISTS admins (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  pw_hash TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  admin_id INTEGER NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  last_used TEXT NOT NULL DEFAULT ''
);
`

// Default settings; editable at runtime via the admin API.
var defaultSettings = map[string]string{
	"session_hours":       "168",
	"max_rating":          "5",
	"default_rate_per_hr": "10",
	"public_base_url":     "",
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{DB: db}
	for k, v := range defaultSettings {
		_, _ = db.Exec(`INSERT OR IGNORE INTO settings(key,value) VALUES(?,?)`, k, v)
	}
	if s.Setting("ip_salt") == "" {
		_, _ = db.Exec(`INSERT OR IGNORE INTO settings(key,value) VALUES('ip_salt',?)`, RandomToken(16))
	}
	return s, nil
}

func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func Hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// ---- settings ----

func (s *Store) Setting(k string) string {
	var v string
	_ = s.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, k).Scan(&v)
	return v
}

func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT key,value FROM settings WHERE key <> 'ip_salt' ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (s *Store) SetSetting(k, v string) error {
	if k == "ip_salt" {
		return errors.New("ip_salt is not editable")
	}
	_, err := s.DB.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v)
	return err
}

func (s *Store) IPHash(ip string) string { return Hash(s.Setting("ip_salt") + "|" + ip)[:32] }

// ---- channels ----

// Fields describes the per-channel form schema.
type Fields struct {
	Rating     bool          `json:"rating"`
	Category   []string      `json:"category,omitempty"` // empty = no category field
	Email      bool          `json:"email"`
	AppVersion bool          `json:"app_version"`
	Custom     []CustomField `json:"custom,omitempty"`
	MessageMin int           `json:"message_min,omitempty"`
	MessageMax int           `json:"message_max,omitempty"`
}

type CustomField struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text | textarea | select
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
}

type Channel struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	AccentColor      string `json:"accent_color"`
	LogoURL          string `json:"logo_url"`
	Fields           Fields `json:"fields"`
	Enabled          bool   `json:"enabled"`
	RatePerHour      int    `json:"rate_per_hour"`
	MaxBytes         int    `json:"max_bytes"`
	AllowedOrigins   string `json:"allowed_origins"` // comma-separated, "*" allowed
	TurnstileSiteKey string `json:"turnstile_sitekey"`
	TurnstileSecret  string `json:"turnstile_secret,omitempty"`
	ThanksMessage    string `json:"thanks_message"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

func (c *Channel) Origins() []string {
	var out []string
	for _, o := range strings.Split(c.AllowedOrigins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

func (c *Channel) OriginAllowed(origin string) bool {
	for _, o := range c.Origins() {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

const chCols = `id,title,description,accent_color,logo_url,fields_json,enabled,rate_per_hour,max_bytes,allowed_origins,turnstile_sitekey,turnstile_secret,thanks_message,created_at,updated_at`

func scanChannel(sc interface{ Scan(...any) error }) (*Channel, error) {
	var c Channel
	var fj string
	var en int
	if err := sc.Scan(&c.ID, &c.Title, &c.Description, &c.AccentColor, &c.LogoURL, &fj, &en, &c.RatePerHour, &c.MaxBytes,
		&c.AllowedOrigins, &c.TurnstileSiteKey, &c.TurnstileSecret, &c.ThanksMessage, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Enabled = en == 1
	_ = json.Unmarshal([]byte(fj), &c.Fields)
	return &c, nil
}

func (s *Store) Channel(id string) (*Channel, error) {
	return scanChannel(s.DB.QueryRow(`SELECT `+chCols+` FROM channels WHERE id=?`, id))
}

func (s *Store) Channels(prefix string) ([]Channel, error) {
	q := `SELECT ` + chCols + ` FROM channels`
	var args []any
	if prefix != "" {
		q += ` WHERE id = ? OR id LIKE ?`
		args = append(args, prefix, prefix+".%")
	}
	rows, err := s.DB.Query(q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) AnyOriginAllowed(origin string) bool {
	chs, err := s.Channels("")
	if err != nil {
		return false
	}
	for _, c := range chs {
		if c.Enabled && c.OriginAllowed(origin) {
			return true
		}
	}
	return false
}

// UpsertChannel creates or replaces a channel. Zero values get defaults.
func (s *Store) UpsertChannel(c *Channel) error {
	if !ChannelIDRe.MatchString(c.ID) {
		return fmt.Errorf("invalid channel id %q (use dotted lowercase, e.g. elko.mcp.news)", c.ID)
	}
	if c.AccentColor == "" {
		c.AccentColor = "#f5b400"
	}
	if c.RatePerHour == 0 {
		fmt.Sscan(s.Setting("default_rate_per_hr"), &c.RatePerHour)
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 8000
	}
	if c.Title == "" {
		c.Title = c.ID
	}
	fj, _ := json.Marshal(c.Fields)
	t := now()
	en := 0
	if c.Enabled {
		en = 1
	}
	_, err := s.DB.Exec(`INSERT INTO channels(`+chCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(id) DO UPDATE SET title=excluded.title, description=excluded.description, accent_color=excluded.accent_color,
	 logo_url=excluded.logo_url, fields_json=excluded.fields_json, enabled=excluded.enabled, rate_per_hour=excluded.rate_per_hour,
	 max_bytes=excluded.max_bytes, allowed_origins=excluded.allowed_origins, turnstile_sitekey=excluded.turnstile_sitekey,
	 turnstile_secret=excluded.turnstile_secret, thanks_message=excluded.thanks_message, updated_at=excluded.updated_at`,
		c.ID, c.Title, c.Description, c.AccentColor, c.LogoURL, string(fj), en, c.RatePerHour, c.MaxBytes, c.AllowedOrigins,
		c.TurnstileSiteKey, c.TurnstileSecret, c.ThanksMessage, t, t)
	return err
}

func (s *Store) DeleteChannel(id string) error {
	r, err := s.DB.Exec(`DELETE FROM channels WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- feedback ----

type Feedback struct {
	ID         int64           `json:"id"`
	Channel    string          `json:"channel"`
	Message    string          `json:"message"`
	Rating     *int            `json:"rating"`
	Category   string          `json:"category"`
	Email      string          `json:"email"`
	Custom     json.RawMessage `json:"custom"`
	Source     string          `json:"source"`
	AppVersion string          `json:"app_version"`
	UserAgent  string          `json:"user_agent"`
	Referrer   string          `json:"referrer"`
	IPHash     string          `json:"ip_hash"`
	Status     string          `json:"status"`
	Tags       []string        `json:"tags"`
	Notes      string          `json:"notes"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

var Statuses = []string{"new", "read", "triaged", "archived"}

func ValidStatus(s string) bool {
	for _, v := range Statuses {
		if v == s {
			return true
		}
	}
	return false
}

func normTags(tags []string) string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "," + strings.Join(out, ",") + ","
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) InsertFeedback(f *Feedback) error {
	t := now()
	if len(f.Custom) == 0 {
		f.Custom = json.RawMessage(`{}`)
	}
	if f.Source == "" {
		f.Source = "web"
	}
	res, err := s.DB.Exec(`INSERT INTO feedback(channel,message,rating,category,email,custom_json,source,app_version,user_agent,referrer,ip_hash,status,tags,notes,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,'new','','',?,?)`,
		f.Channel, f.Message, f.Rating, f.Category, f.Email, string(f.Custom), f.Source, f.AppVersion, f.UserAgent, f.Referrer, f.IPHash, t, t)
	if err != nil {
		return err
	}
	f.ID, _ = res.LastInsertId()
	f.Status, f.CreatedAt, f.UpdatedAt, f.Tags = "new", t, t, []string{}
	return nil
}

type Filter struct {
	Prefix    string // channel prefix (dotted)
	Since     string // RFC3339 or YYYY-MM-DD
	Until     string
	MinRating int
	MaxRating int
	Status    string
	Tag       string
	Q         string // substring search over message/email/notes
	Limit     int
	Offset    int
}

func (f Filter) where() (string, []any) {
	var w []string
	var a []any
	if f.Prefix != "" {
		// substr avoids LIKE wildcards ('_' is legal in channel ids)
		w = append(w, "(channel = ? OR substr(channel, 1, ?) = ?)")
		a = append(a, f.Prefix, len(f.Prefix)+1, f.Prefix+".")
	}
	if f.Since != "" {
		w = append(w, "created_at >= ?")
		a = append(a, f.Since)
	}
	if f.Until != "" {
		u := f.Until
		if len(u) == 10 {
			u += "T23:59:59Z"
		}
		w = append(w, "created_at <= ?")
		a = append(a, u)
	}
	if f.MinRating > 0 {
		w = append(w, "rating >= ?")
		a = append(a, f.MinRating)
	}
	if f.MaxRating > 0 {
		w = append(w, "rating <= ?")
		a = append(a, f.MaxRating)
	}
	if f.Status != "" {
		w = append(w, "status = ?")
		a = append(a, f.Status)
	}
	if f.Tag != "" {
		w = append(w, "tags LIKE ?")
		a = append(a, "%,"+strings.ToLower(f.Tag)+",%")
	}
	if f.Q != "" {
		w = append(w, "(message LIKE ? OR email LIKE ? OR notes LIKE ? OR custom_json LIKE ?)")
		q := "%" + f.Q + "%"
		a = append(a, q, q, q, q)
	}
	if len(w) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(w, " AND "), a
}

const fbCols = `id,channel,message,rating,category,email,custom_json,source,app_version,user_agent,referrer,ip_hash,status,tags,notes,created_at,updated_at`

func scanFeedback(sc interface{ Scan(...any) error }) (*Feedback, error) {
	var f Feedback
	var rating sql.NullInt64
	var custom, tags string
	if err := sc.Scan(&f.ID, &f.Channel, &f.Message, &rating, &f.Category, &f.Email, &custom, &f.Source, &f.AppVersion,
		&f.UserAgent, &f.Referrer, &f.IPHash, &f.Status, &tags, &f.Notes, &f.CreatedAt, &f.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if rating.Valid {
		r := int(rating.Int64)
		f.Rating = &r
	}
	f.Custom = json.RawMessage(custom)
	f.Tags = splitTags(tags)
	return &f, nil
}

func (s *Store) ListFeedback(f Filter) ([]Feedback, int, error) {
	w, a := f.where()
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM feedback`+w, a...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 50
	}
	rows, err := s.DB.Query(`SELECT `+fbCols+` FROM feedback`+w+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(a, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		fb, err := scanFeedback(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *fb)
	}
	return out, total, rows.Err()
}

// EachFeedback streams every row matching f (used by export).
func (s *Store) EachFeedback(f Filter, fn func(*Feedback) error) error {
	w, a := f.where()
	rows, err := s.DB.Query(`SELECT `+fbCols+` FROM feedback`+w+` ORDER BY created_at DESC, id DESC`, a...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		fb, err := scanFeedback(rows)
		if err != nil {
			return err
		}
		if err := fn(fb); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) GetFeedback(id int64) (*Feedback, error) {
	return scanFeedback(s.DB.QueryRow(`SELECT `+fbCols+` FROM feedback WHERE id=?`, id))
}

type FeedbackPatch struct {
	Status *string   `json:"status"`
	Tags   *[]string `json:"tags"`
	Notes  *string   `json:"notes"`
}

func (s *Store) PatchFeedback(id int64, p FeedbackPatch) (*Feedback, error) {
	var sets []string
	var a []any
	if p.Status != nil {
		if !ValidStatus(*p.Status) {
			return nil, fmt.Errorf("invalid status %q (one of %s)", *p.Status, strings.Join(Statuses, ", "))
		}
		sets = append(sets, "status=?")
		a = append(a, *p.Status)
	}
	if p.Tags != nil {
		sets = append(sets, "tags=?")
		a = append(a, normTags(*p.Tags))
	}
	if p.Notes != nil {
		sets = append(sets, "notes=?")
		a = append(a, *p.Notes)
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at=?")
		a = append(a, now(), id)
		r, err := s.DB.Exec(`UPDATE feedback SET `+strings.Join(sets, ",")+` WHERE id=?`, a...)
		if err != nil {
			return nil, err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
	}
	return s.GetFeedback(id)
}

func (s *Store) DeleteFeedback(id int64) error {
	r, err := s.DB.Exec(`DELETE FROM feedback WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type ChannelStat struct {
	Channel string `json:"channel"`
	Total   int    `json:"total"`
	New     int    `json:"new"`
}

func (s *Store) Stats() ([]ChannelStat, error) {
	rows, err := s.DB.Query(`SELECT channel, COUNT(*), SUM(status='new') FROM feedback GROUP BY channel ORDER BY channel`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChannelStat{}
	for rows.Next() {
		var c ChannelStat
		if err := rows.Scan(&c.Channel, &c.Total, &c.New); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- admins & sessions ----

func (s *Store) CreateAdmin(username, password string) error {
	if len(password) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO admins(username,pw_hash,created_at) VALUES(?,?,?)
		ON CONFLICT(username) DO UPDATE SET pw_hash=excluded.pw_hash`, username, string(h), now())
	return err
}

func (s *Store) AdminCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&n)
	return n
}

// Login verifies credentials and returns a new session token.
func (s *Store) Login(username, password string) (string, time.Time, error) {
	var id int64
	var h string
	err := s.DB.QueryRow(`SELECT id,pw_hash FROM admins WHERE username=?`, username).Scan(&id, &h)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(h), []byte(password)) != nil {
		return "", time.Time{}, errors.New("invalid username or password")
	}
	hours := 168
	fmt.Sscan(s.Setting("session_hours"), &hours)
	tok := RandomToken(32)
	exp := time.Now().UTC().Add(time.Duration(hours) * time.Hour)
	_, err = s.DB.Exec(`INSERT INTO sessions(token_hash,admin_id,expires_at) VALUES(?,?,?)`, Hash(tok), id, exp.Format(time.RFC3339))
	return tok, exp, err
}

func (s *Store) SessionAdmin(tok string) (string, bool) {
	var user string
	err := s.DB.QueryRow(`SELECT a.username FROM sessions s JOIN admins a ON a.id=s.admin_id WHERE s.token_hash=? AND s.expires_at > ?`,
		Hash(tok), now()).Scan(&user)
	return user, err == nil
}

func (s *Store) Logout(tok string) {
	_, _ = s.DB.Exec(`DELETE FROM sessions WHERE token_hash=?`, Hash(tok))
}

// ---- API keys ----

type APIKey struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	CreatedAt string `json:"created_at"`
	LastUsed  string `json:"last_used"`
	Key       string `json:"key,omitempty"` // only returned once, at creation
}

func (s *Store) CreateAPIKey(name string) (*APIKey, error) {
	key := "efk_" + RandomToken(24)
	k := &APIKey{Name: name, Prefix: key[:10], CreatedAt: now(), Key: key}
	r, err := s.DB.Exec(`INSERT INTO api_keys(name,prefix,key_hash,created_at) VALUES(?,?,?,?)`, name, k.Prefix, Hash(key), k.CreatedAt)
	if err != nil {
		return nil, err
	}
	k.ID, _ = r.LastInsertId()
	return k, nil
}

func (s *Store) APIKeys() ([]APIKey, error) {
	rows, err := s.DB.Query(`SELECT id,name,prefix,created_at,last_used FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) CheckAPIKey(key string) (string, bool) {
	var name string
	if err := s.DB.QueryRow(`SELECT name FROM api_keys WHERE key_hash=?`, Hash(key)).Scan(&name); err != nil {
		return "", false
	}
	_, _ = s.DB.Exec(`UPDATE api_keys SET last_used=? WHERE key_hash=?`, now(), Hash(key))
	return name, true
}

func (s *Store) DeleteAPIKey(id int64) error {
	r, err := s.DB.Exec(`DELETE FROM api_keys WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
