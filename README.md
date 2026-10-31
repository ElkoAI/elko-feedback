# Elko Feedback

A tiny, self-hosted feedback inbox. One Go binary, one SQLite file, no email service and no third-party SaaS.

Point a "Give Feedback" link at it from a Docker Hub page, a CLI, an MCP server or a web app. Users get a clean hosted form (or you post JSON), and you triage everything from one admin dashboard.

## Features

- **Dotted channel paths.** Every feedback target is a channel such as `elko.mcp.news` or `acme.app.billing`. Channels are rows in the DB, so you add them without redeploying, and the dashboard can filter by prefix (`elko.mcp` shows all MCP servers).
- **Hosted form** at `/f/{channel}`. It works without JavaScript, has no required email field, and includes a honeypot plus optional Cloudflare Turnstile.
- **REST API** with `POST /v1/feedback` for submissions and a full admin API (list, filter, triage, channels, settings, API keys). See [docs/API.md](docs/API.md).
- **Admin dashboard** at `/admin`. It is a plain vanilla-JS client of the same API, with no hidden endpoints.
- **Privacy by default.** IPs are stored only as salted hashes. Passwords use bcrypt, and sessions and API keys are stored as hashes.
- **Config in the DB.** Rate limits, CORS origins, Turnstile keys and session length are edited live. Env vars cover only process wiring.
- **Go client** (`pkg/client`) and an MCP tool example (`examples/mcp`).

## Quick start

```bash
git clone https://github.com/ElkoAI/elko-feedback.git && cd elko-feedback
./run.sh admin admin   # create the first admin (prompts for a password, min 10 chars)
./run.sh               # build, seed example channels, serve on :8080
```

- Form: http://localhost:8080/f/elko.mcp.news
- Dashboard: http://localhost:8080/admin
- Tests: `./run.sh test`

Submit from the command line:

```bash
curl -X POST http://localhost:8080/v1/feedback \
  -H 'Content-Type: application/json' \
  -d '{"channel":"elko.mcp.news","message":"Love it, but add RSS filters"}'
```

## Docker

```bash
docker compose up -d --build
docker compose run --rm elko-feedback admin create -user admin
docker compose run --rm elko-feedback seed
```

The image is distroless and runs as non-root, with the DB on a `./data` bind mount. The included `docker-compose.yml` assumes Traefik on an external `proxy` network; edit the host rule for your domain.

## Configuration

| Env | Default | Meaning |
|---|---|---|
| `ELKO_FEEDBACK_DB` | `./data/feedback.db` | SQLite path |
| `ELKO_FEEDBACK_ADDR` | `:8080` | listen address |
| `ELKO_FEEDBACK_TRUST_PROXY` | unset | `1` = trust `CF-Connecting-IP` / `X-Forwarded-For` |
| `ELKO_FEEDBACK_SECURE_COOKIE` | unset | `1` = Secure session cookie (HTTPS) |

Everything else is set in the dashboard or through the API.

## Docs

- [docs/API.md](docs/API.md): REST reference
- [docs/DEPLOY.md](docs/DEPLOY.md): production deploy
- [HANDOFF.md](HANDOFF.md): architecture and decisions

## License

[MIT](LICENSE) © 2026 Elko AI / John Soprych
