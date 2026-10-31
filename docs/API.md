# Elko Feedback — REST API

Base URL (planned): `https://feedback.elko.ai`. All JSON. Errors are `{"error":"…"}` with a 4xx/5xx status.

## Concepts

- **Channel**: a DB-defined feedback destination with a dotted path id, e.g. `elko.mcp.news`.
  Ids match `^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)*$`.
  A **prefix filter** `elko.mcp` matches `elko.mcp` and `elko.mcp.*` (not `elko.mcpx`).
- **Status**: `new` → `read` → `triaged` → `archived`.
- **Privacy**: the client IP is never stored. Only `ip_hash = sha256(salt|ip)[:32]` is kept. The salt is generated per database and is not editable or exposed.

## Public endpoints (no auth)

| Method | Path | Purpose |
|---|---|---|
| GET | `/f/{channel}` | Hosted HTML form (works with JS disabled). Query `?app_version=` / `?v=` and `?referrer=` are carried along. |
| POST | `/f/{channel}` | Form post target. Renders a thank-you page, or re-renders the form with an error. |
| GET | `/v1/channels/{channel}` | Public channel info: title, branding, field schema, Turnstile site key. |
| POST | `/v1/feedback` | Submit. Accepts JSON or `application/x-www-form-urlencoded`. CORS is limited to the channel's `allowed_origins`. |
| GET | `/healthz` | Liveness. |

### POST /v1/feedback

```json
{
  "channel": "elko.mcp.news",
  "message": "required text",
  "rating": 4,
  "category": "bug",
  "email": "me@example.com",
  "custom": {"plan": "pro"},
  "app_version": "1.4.0",
  "source": "mcp",
  "referrer": "https://hub.docker.com/r/…",
  "website": "",
  "cf-turnstile-response": "…"
}
```

- `rating`, `category`, `email`, `custom` and `app_version` are optional.
- `rating` is ignored unless the channel enables it.
- `website` is a honeypot. If it is filled in, the API answers 201 but stores nothing.
- `cf-turnstile-response` is required only when the channel has a Turnstile secret.
- In form encoding, custom fields are `custom.<name>`.

Responses:
- `201 {"ok":true,"id":12,"created_at":"…"}`
- `400` validation error
- `404` unknown or disabled channel
- `413` over the channel's `max_bytes`
- `429` rate limit (per channel + IP hash, `rate_per_hour`; invalid attempts also count)

Captured server-side: User-Agent, Referer (header or `referrer` field), and the IP hash.

## Admin authentication

You can authenticate in either of two ways:
- **Session**: `POST /v1/admin/login` with `{"username","password"}`. It sets the HttpOnly cookie `ef_session` (SameSite=Lax, lifetime = setting `session_hours`). The response also includes `token`, which can be sent as a cookie by non-browser clients. Cookie-authenticated writes with a foreign `Origin` header are rejected (403).
- **API key**: `Authorization: Bearer efk_…`. Create keys with `POST /v1/admin/keys` or `elko-feedback key create -name NAME`. Only a hash is stored, and the key is shown once.

Login is rate limited to 20 attempts per hour per IP.

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/admin/login` | JSON or form |
| POST | `/v1/admin/logout` | |
| GET | `/v1/admin/me` | `{"principal":"admin:admin"}` or `"key:mcp"` |
| GET | `/v1/admin/stats` | per-channel `total` / `new` counts |
| GET | `/v1/admin/channels?prefix=` | Turnstile secrets are masked as `********` |
| POST | `/v1/admin/channels` | create/update (body includes `id`) |
| GET/PUT/DELETE | `/v1/admin/channels/{id}` | PUT = upsert. Sending the masked secret back keeps the old one. DELETE keeps the channel's feedback. |
| GET | `/v1/admin/feedback` | filters below; returns `{items,total,limit,offset}` |
| GET/PATCH/DELETE | `/v1/admin/feedback/{id}` | PATCH `{"status","tags":[…],"notes"}` (all optional; tags are lowercased) |
| GET | `/v1/admin/export?format=csv\|json&…filters` | Streams a download. CSV cells starting with `= + - @` are prefixed with `'`. |
| GET/POST | `/v1/admin/keys` | POST `{"name"}` returns `key` once |
| DELETE | `/v1/admin/keys/{id}` | revoke |
| GET/PUT | `/v1/admin/settings` | PUT a partial map. Keys: `session_hours`, `max_rating`, `default_rate_per_hr`, `public_base_url` |

### Feedback filters

These query parameters work on both list and export:
- `prefix`: channel prefix
- `q`: substring search over message, email and notes
- `status`
- `tag`
- `rating`: exact match
- `min_rating`, `max_rating`
- `since`, `until`: `YYYY-MM-DD` or RFC3339
- `limit` (default 50, max 1000) and `offset`

### Channel object

```json
{
  "id": "elko.mcp.news", "title": "Elko News MCP", "description": "…",
  "accent_color": "#f5b400", "logo_url": "", "enabled": true,
  "rate_per_hour": 10, "max_bytes": 8000,
  "allowed_origins": "https://hub.docker.com,https://elko.ai",
  "turnstile_sitekey": "", "turnstile_secret": "", "thanks_message": "",
  "fields": {
    "rating": true, "email": true, "app_version": true,
    "category": ["bug","idea","data quality","praise","other"],
    "message_min": 0, "message_max": 5000,
    "custom": [{"name":"plan","label":"Plan","type":"select","options":["free","pro"],"required":false}]
  }
}
```

Custom field `type` is `text`, `textarea` or `select`.

## Go client / MCP

`github.com/ElkoAI/elko-feedback/pkg/client`:

```go
id, err := client.New("https://feedback.elko.ai").Submit(ctx, client.Feedback{
    Channel: "elko.mcp.news", Message: msg, AppVersion: version,
})
```

A ready-to-paste `submit_feedback` MCP tool (mcp-go) is in `examples/mcp/submit_feedback.go`. The `source` field defaults to `mcp`.

An MCP-readable admin endpoint (read-only feedback listing over MCP using an API key) is planned. Today, the admin REST API with a Bearer key already covers this.

## Notifications

`server.Notifier` has a single method, `FeedbackCreated(ctx, channel, feedback)`. It is called after every stored submission. The default is a no-op; email, Slack or webhook notifiers plug in here.
