// Command elko-feedback runs the Elko Feedback service and its admin CLI.
//
//	elko-feedback serve   [-db path] [-addr :8080]
//	elko-feedback admin create -user NAME      (password read from ELKO_FEEDBACK_PASSWORD or stdin)
//	elko-feedback key create -name NAME
//	elko-feedback seed                          (creates the default Elko channels)
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ElkoAI/elko-feedback/internal/server"
	"github.com/ElkoAI/elko-feedback/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  elko-feedback serve [-db PATH] [-addr ADDR] [-trust-proxy] [-secure-cookie]
  elko-feedback admin create -user NAME   (password from $ELKO_FEEDBACK_PASSWORD or stdin)
  elko-feedback key create -name NAME     (prints the key once)
  elko-feedback seed                      (default Elko channels)
env: ELKO_FEEDBACK_DB (default ./data/feedback.db), ELKO_FEEDBACK_ADDR (default :8080)`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	if os.Args[1] == "health" { // container healthcheck, no DB access
		resp, err := http.Get("http://127.0.0.1" + env("ELKO_FEEDBACK_ADDR", ":8080") + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		os.Exit(0)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if (cmd == "admin" || cmd == "key") && len(args) > 0 {
		cmd, args = cmd+" "+args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dbPath := fs.String("db", env("ELKO_FEEDBACK_DB", "./data/feedback.db"), "SQLite database path")
	addr := fs.String("addr", env("ELKO_FEEDBACK_ADDR", ":8080"), "listen address")
	trust := fs.Bool("trust-proxy", env("ELKO_FEEDBACK_TRUST_PROXY", "") == "1", "trust X-Forwarded-For / CF-Connecting-IP")
	secure := fs.Bool("secure-cookie", env("ELKO_FEEDBACK_SECURE_COOKIE", "") == "1", "mark session cookie Secure (behind HTTPS)")
	user := fs.String("user", "", "admin username")
	name := fs.String("name", "", "API key name")
	_ = fs.Parse(args)

	if i := strings.LastIndex(*dbPath, "/"); i > 0 {
		_ = os.MkdirAll((*dbPath)[:i], 0o750)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer st.DB.Close()

	switch cmd {
	case "serve":
		srv := server.New(st)
		srv.TrustProxy, srv.SecureCookie = *trust, *secure
		hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
		if st.AdminCount() == 0 {
			log.Printf("no admin yet: run `elko-feedback admin create -user NAME`")
		}
		go func() {
			log.Printf("elko-feedback listening on %s (db %s)", *addr, *dbPath)
			if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal(err)
			}
		}()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(ctx)
	case "admin create":
		if *user == "" {
			usage()
		}
		pw := os.Getenv("ELKO_FEEDBACK_PASSWORD")
		if pw == "" {
			fmt.Fprint(os.Stderr, "password (min 10 chars): ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			pw = strings.TrimRight(line, "\r\n")
		}
		if err := st.CreateAdmin(*user, pw); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("admin %q saved\n", *user)
	case "key create":
		if *name == "" {
			usage()
		}
		k, err := st.CreateAPIKey(*name)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(k.Key)
	case "seed":
		n, err := seed(st)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("seeded %d channel(s)\n", n)
	default:
		usage()
	}
}

func seed(st *store.Store) (int, error) {
	fields := store.Fields{Rating: true, Email: true, AppVersion: true, Category: []string{"bug", "idea", "data quality", "praise", "other"}, MessageMax: 5000}
	chans := []store.Channel{
		{ID: "elko.mcp.news", Title: "Elko News MCP", Description: "Tell us what's working, what's broken, and what news sources or tools you want next.", AccentColor: "#f5b400", Fields: fields, Enabled: true, AllowedOrigins: "https://hub.docker.com,https://elko.ai"},
		{ID: "elko.mcp.market", Title: "Elko Market MCP", Description: "Feedback on market data coverage, accuracy and tools.", AccentColor: "#22c55e", Fields: fields, Enabled: true, AllowedOrigins: "https://hub.docker.com,https://elko.ai"},
		{ID: "elko.project-wizard", Title: "Elko Project Wizard", Description: "How did the wizard do? Ideas for templates and steps welcome.", AccentColor: "#8b5cf6", Fields: fields, Enabled: true, AllowedOrigins: "https://hub.docker.com,https://elko.ai"},
	}
	n := 0
	for i := range chans {
		if _, err := st.Channel(chans[i].ID); err == nil {
			continue // never overwrite admin edits
		}
		if err := st.UpsertChannel(&chans[i]); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
