#!/usr/bin/env bash
# Local dev runner: build, seed the default channels, serve on :8080.
#   ./run.sh                  build + seed + serve
#   ./run.sh test             go vet + go test
#   ./run.sh admin NAME       create/update an admin (prompts for password)
set -euo pipefail
cd "$(dirname "$0")"
export ELKO_FEEDBACK_DB="${ELKO_FEEDBACK_DB:-./data/feedback.db}"
export ELKO_FEEDBACK_ADDR="${ELKO_FEEDBACK_ADDR:-:8080}"
go build -o ./elko-feedback ./cmd/elko-feedback
case "${1:-serve}" in
  test)  go vet ./... && go test ./... ;;
  admin) ./elko-feedback admin create -user "${2:?usage: ./run.sh admin NAME}" ;;
  serve) ./elko-feedback seed; exec ./elko-feedback serve ;;
  *)     exec ./elko-feedback "$@" ;;
esac
