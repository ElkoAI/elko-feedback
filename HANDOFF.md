# HANDOFF: Elko Feedback

**What:** a self-hosted feedback inbox for Elko products. Each product posts to a dotted *channel* (`elko.mcp.news`, …).

- Users submit through a hosted no-JS form (`/f/{channel}`) or the JSON API (`POST /v1/feedback`). MCP servers can use `pkg/client`.
- Admins triage in `/admin`, which is a vanilla-JS client of the REST API.

**State (2026-10-31):** code and tests complete; MIT licensed; prepared for public release. Example production host: `feedback.elko.ai`. The admin password is kept in gitignored `data/.admin-pass`.

## Layout

| Path | What |
|---|---|
| `cmd/elko-feedback` | CLI: `serve`, `admin create`, `key create`, `seed`, `health` |
| `internal/store` | SQLite (modernc, pure Go, WAL) schema + queries; config/policy in tables `settings`, `channels` |
| `internal/server` | HTTP: public form/API, admin API, auth, CORS, rate limit, honeypot, Turnstile, `Notifier` hook |
| `internal/server/web` | `form.html`, `thanks.html`, `static/admin.{html,css,js}` (embedded) |
| `pkg/client` | Go submit client |
| `examples/mcp/submit_feedback.go` | MCP tool snippet |
| `docs/API.md`, `docs/DEPLOY.md` | reference + deploy runbook |
| `run.sh` | `./run.sh` (seed+serve), `./run.sh test`, `./run.sh admin NAME` |

## Decisions

- **API-first.** The dashboard has no special access; everything it does is a documented endpoint.
- **Privacy.** IPs are never stored, only a salted hash.
- **Auth.** Passwords use bcrypt; sessions and API keys are stored as sha256 hashes.
- **Rate limit.** Counters are in memory, so they reset on restart, which is acceptable at this scale. The policy is stored per channel in the DB.
- **Abuse controls.** Honeypot submissions get a fake 201 and are stored nowhere.

## Next ideas

- Email/Slack notifier via the `Notifier` interface.
- MCP admin tool (list/triage over MCP using an API key).
