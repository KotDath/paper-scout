package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/bcrypt"
	"paper-scout/internal/agent"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/research"
	"paper-scout/internal/researchapp"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

// T14 reuses the bounded integration composition and external fixtures. In UI
// live mode its existing --live flag selects official sources/provider instead.
// These credentials are public, disposable integration inputs, never dotenv.
func verifyFlutterClient(mode string, db *store.Store, runs *research.Service, runner *agent.AgentRunner, search *retrieval.Service) {
	check(mode == "dart" || mode == "ui", "client-check must be dart or ui")
	hash, err := bcrypt.GenerateFromPassword([]byte("api-integration-password"), bcrypt.DefaultCost)
	must(err)
	_, err = db.DB.Exec(`UPDATE users SET password_hash=? WHERE id IN ('alice','bob')`, string(hash))
	must(err)
	researchapp.Attach(db, runs, runner, search)
	server := httptest.NewServer(httpapi.NewWithRuns(db, runs, search))
	defer server.Close()
	defer func() { runs.Close(); runs.Wait() }()
	if mode == "ui" {
		fmt.Println("T14 UI API:", server.URL)
		fmt.Println("Temporary accounts ready; bounded runs; no auto-replay. Stop with SIGTERM.")
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
		return
	}
	command := exec.Command("dart", "run", "integration/flutter_research.dart")
	command.Env = append(os.Environ(), "SCOUT_BASE="+server.URL)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	must(command.Run())
}
