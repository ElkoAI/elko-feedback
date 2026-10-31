# Deploying Elko Feedback

Example host used below: `feedback.elko.ai`. Substitute your own.

## Shape

- One static Go binary in a distroless image, running as non-root.
- SQLite in WAL mode at `/data/feedback.db`, which is a bind mount.
- All config and policy (channels, rate limits, CORS origins, Turnstile keys, session length) live in the DB and are edited through the API or dashboard. Env vars cover only process wiring:

| Env | Default | Meaning |
|---|---|---|
| `ELKO_FEEDBACK_DB` | `./data/feedback.db` (`/data/feedback.db` in the image) | DB path |
| `ELKO_FEEDBACK_ADDR` | `:8080` | listen address |
| `ELKO_FEEDBACK_TRUST_PROXY` | unset | `1` = take the client IP from `CF-Connecting-IP` / `X-Forwarded-For` (set this behind Traefik/Cloudflare) |
| `ELKO_FEEDBACK_SECURE_COOKIE` | unset | `1` = Secure session cookie (set under HTTPS) |

## Local

```bash
./run.sh test                 # vet + tests
./run.sh admin admin          # prompts for password (min 10 chars)
./run.sh                      # seeds elko.mcp.news / elko.mcp.market / elko.project-wizard, serves :8080
# form:      http://localhost:8080/f/elko.mcp.news
# dashboard: http://localhost:8080/admin
```

## Server (Docker Compose)

```bash
# 1. code
sudo mkdir -p /opt/elko-feedback && cd /opt/elko-feedback
git clone https://github.com/ElkoAI/elko-feedback.git .
mkdir -p data && sudo chown 65532:65532 data            # distroless nonroot uid

# 2. build + start (Traefik must share the external "proxy" network; adjust the
#    network/entrypoint names and Host rule in docker-compose.yml)
docker compose up -d --build

# 3. first admin + channels
docker compose run --rm elko-feedback admin create -user admin  # type the password when prompted
docker compose run --rm elko-feedback seed
```

## Routing for feedback.elko.ai

- **Direct DNS + Traefik**: create an `A` record `feedback` pointing at your server, keep the `websecure` router with TLS, and let Traefik's ACME resolver issue the certificate.
- **Cloudflare Tunnel**: add public hostname `feedback.elko.ai` pointing at `http://traefik:80` (or straight to `http://elko-feedback:8080`).

## Admin password

Keep the first admin's password out of git. By convention it lives in `data/.admin-pass`, which is gitignored (as are `data/`, `*.db` and `.env`). Create it with `umask 077` and never commit it.

## After go-live

1. Sign in at `/admin`.
2. Optionally set `public_base_url` = `https://feedback.elko.ai` in Settings.
3. Optional Turnstile: create a widget for `feedback.elko.ai` in Cloudflare, then paste the site key and secret into the channel (the secret field is masked and has an eye toggle).
4. Point your "Give Feedback" links at `https://feedback.elko.ai/f/<channel>`.
5. Back up by copying `data/feedback.db` after `sqlite3 data/feedback.db ".backup data/backup.db"`, or stop the container and copy the files.
