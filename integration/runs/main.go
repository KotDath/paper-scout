// T07 integration of the actual SQLite store, lifecycle service and HTTP API.
// Generated protocol records exercise storage, not a claimed model execution.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"paper-scout/internal/auth"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
func text(s string) *string { return &s }

func main() {
	if len(os.Args) == 4 && os.Args[1] == "serve-running" {
		// A real worker process that the Python harness kills after persistence.
		ctx := context.Background()
		db, err := store.Open(ctx, os.Args[2])
		must(err)
		defer db.Close()
		service := research.New(ctx, db)
		defer service.Close()
		c, err := db.CreateConversation(ctx, os.Args[3], "Abrupt worker stop")
		must(err)
		r, _, err := service.Create(ctx, os.Args[3], c.ID, "abrupt", "Persist before SIGKILL")
		must(err)
		_, _, err = service.Start(ctx, os.Args[3], r.ID)
		must(err)
		_, err = db.AppendMessage(ctx, os.Args[3], r.ID, store.Message{Role: "assistant", Content: text("Persisted before interruption")})
		must(err)
		must(json.NewEncoder(os.Stdout).Encode(map[string]string{"run_id": r.ID, "conversation_id": c.ID}))
		time.Sleep(30 * time.Second)
		return
	}
	check(len(os.Args) == 2, "provide runtime directory")
	dir, err := os.MkdirTemp(os.Args[1], "lifecycle-")
	must(err)
	defer os.RemoveAll(dir)
	ctx := context.Background()
	path := filepath.Join(dir, "runs.sqlite")
	db, err := store.Open(ctx, path)
	must(err)
	password := rand.Text()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	must(err)
	must(db.SeedAccounts(ctx, []store.SeedAccount{
		{User: store.User{ID: "owner-a", Login: "a", CreatedAt: time.Now().UTC().Format(time.RFC3339)}, PasswordHash: string(hash)},
		{User: store.User{ID: "owner-b", Login: "b", CreatedAt: time.Now().UTC().Format(time.RFC3339)}, PasswordHash: string(hash)},
	}))
	credentials, err := auth.New(db).Login(ctx, "a", password)
	must(err)
	lifetime, stop := context.WithCancel(ctx)
	service := research.New(lifetime, db)
	server := httptest.NewServer(httpapi.NewWithRuns(db, service))
	defer server.Close()
	get := func(path string) []byte {
		req, err := http.NewRequest("GET", server.URL+path, nil)
		must(err)
		req.Header.Set("Authorization", "Bearer "+credentials.SessionToken)
		res, err := server.Client().Do(req)
		must(err)
		defer res.Body.Close()
		check(res.StatusCode == 200, "HTTP read failed")
		body, err := io.ReadAll(res.Body)
		must(err)
		return body
	}
	conversation, err := db.CreateConversation(ctx, "owner-a", "Lifecycle integration")
	must(err)
	run, created, err := service.Create(ctx, "owner-a", conversation.ID, "first", "Research question")
	must(err)
	check(created, "new run not created")
	requestCtx, endRequest := context.WithCancel(ctx)
	runCtx, started, err := service.Start(requestCtx, "owner-a", run.ID)
	must(err)
	endRequest()
	check(started.Status == "running" && started.StartedAt != nil, "start not persisted")
	check(runCtx.Err() == nil, "request context cancelled background work")
	deadline, ok := runCtx.Deadline()
	check(ok && time.Until(deadline) > 14*time.Minute, "missing run deadline")
	_, _, err = service.Start(ctx, "owner-a", run.ID)
	check(errors.Is(err, store.ErrRunState), "duplicate start accepted")
	_, _, err = service.Start(ctx, "owner-b", run.ID)
	check(errors.Is(err, store.ErrNotFound), "foreign start accepted")
	_, err = service.Cancel(ctx, "owner-b", run.ID)
	check(errors.Is(err, store.ErrNotFound) && runCtx.Err() == nil, "foreign cancel signalled worker")
	reasoning := "PRIVATE_REASONING_SENTINEL"
	provider := json.RawMessage(`{"private":"PRIVATE_PROVIDER_SENTINEL"}`)
	calls := json.RawMessage(`[{"id":"call_integration","type":"function","function":{"name":"read_passages","arguments":"{}"}}]`)
	_, err = db.AppendMessage(ctx, "owner-a", run.ID, store.Message{Role: "assistant", ReasoningContent: &reasoning, ToolCalls: calls, ProviderJSON: provider})
	must(err)
	_, err = db.AppendMessage(ctx, "owner-a", run.ID, store.Message{Role: "tool", Content: text("PRIVATE_TOOL_SENTINEL"), ToolCallID: "call_integration"})
	must(err)
	_, err = db.AppendMessage(ctx, "owner-a", run.ID, store.Message{Role: "assistant", Content: text("Visible answer"), ReasoningContent: &reasoning})
	must(err)
	must(db.AppendEvent(ctx, "owner-a", run.ID, "analysis_progress", json.RawMessage(`{"segments_analyzed":1}`), json.RawMessage(`{"private":"PRIVATE_EVENT_SENTINEL"}`)))
	// Concurrent writes traverse real transactions and allocate durable sequence.
	var wg sync.WaitGroup
	failures := make(chan error, 24)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- db.AppendEvent(ctx, "owner-a", run.ID, "index_progress", json.RawMessage(`{"chunks_indexed":1}`), nil)
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		must(err)
	}
	messages, err := db.Messages(ctx, "owner-a", conversation.ID)
	must(err)
	check(len(messages) == 4 && messages[1].ReasoningContent != nil && *messages[1].ReasoningContent == reasoning && bytes.Equal(messages[1].ToolCalls, calls) && bytes.Equal(messages[1].ProviderJSON, provider) && messages[2].ToolCallID == "call_integration", "provider history did not round trip")
	for i, m := range messages {
		check(m.Seq == int64(i+1), "message sequence gap")
	}
	public := get("/api/v1/conversations/" + conversation.ID + "?limit=1")
	var page struct {
		Messages []store.Message `json:"messages"`
		Next     *int64          `json:"next_after_seq"`
	}
	must(json.Unmarshal(public, &page))
	check(len(page.Messages) == 1 && page.Next != nil && *page.Next == 1, "history pagination failed")
	public = append(public, get("/api/v1/conversations/"+conversation.ID+"?after_seq=1")...)
	public = append(public, get("/api/v1/runs/"+run.ID+"/events")...)
	for _, marker := range []string{reasoning, "PRIVATE_PROVIDER_SENTINEL", "PRIVATE_TOOL_SENTINEL", "PRIVATE_EVENT_SENTINEL", "reasoning_content", "provider_json", "private_payload_json", `"tool_calls":[`, `"tool_call_id":`} {
		check(!bytes.Contains(public, []byte(marker)), "private field leaked to HTTP")
	}
	check(bytes.Contains(public, []byte("Visible answer")), "assistant answer missing")
	cancelled, err := service.Cancel(ctx, "owner-a", run.ID)
	must(err)
	check(cancelled.CancelRequested && cancelled.Status == "running" && runCtx.Err() == context.Canceled, "cancellation not durable or not propagated")
	events, err := db.RunEvents(ctx, "owner-a", run.ID, 0, 100)
	must(err)
	count := len(events)
	_, err = service.Cancel(ctx, "owner-a", run.ID)
	must(err)
	after, err := db.RunEvents(ctx, "owner-a", run.ID, 0, 100)
	must(err)
	check(len(after) == count, "duplicate cancellation event")
	finished, err := service.Finish(ctx, "owner-a", run.ID, "completed", "", json.RawMessage(`{"requests":1,"usage_unknown":true}`))
	must(err)
	check(finished.Status == "cancelled" && finished.FinishedAt != nil, "completion overwrote committed cancellation")
	_, err = db.AppendMessage(ctx, "owner-a", run.ID, store.Message{Role: "assistant", Content: text("late")})
	check(errors.Is(err, store.ErrRunState), "late message accepted")
	_, err = service.Cancel(ctx, "owner-a", run.ID)
	must(err)
	for i, e := range after {
		check(e.Seq == int64(i+1), "event sequence gap")
	}
	fmt.Println("PASS: real SQLite + HTTP projection, private protocol round trip, concurrent event sequence, detached context and durable cancellation")

	// Preserve a successful run and two different unfinished states on reopen.
	complete, _, err := service.Create(ctx, "owner-a", conversation.ID, "complete", "Second question")
	must(err)
	_, _, err = service.Start(ctx, "owner-a", complete.ID)
	must(err)
	_, err = db.AppendMessage(ctx, "owner-a", complete.ID, store.Message{Role: "assistant", Content: text("Persisted answer")})
	must(err)
	_, err = service.Finish(ctx, "owner-a", complete.ID, "completed", "", json.RawMessage(`{"total_tokens":12}`))
	must(err)
	_, err = service.Cancel(ctx, "owner-a", complete.ID)
	must(err)
	terminalEvents, err := db.RunEvents(ctx, "owner-a", complete.ID, 0, 100)
	must(err)
	current, _, err := service.Create(ctx, "owner-a", conversation.ID, "unfinished", "Interrupted question")
	must(err)
	currentCtx, _, err := service.Start(ctx, "owner-a", current.ID)
	must(err)
	second, err := db.CreateConversation(ctx, "owner-b", "Queued")
	must(err)
	queued, _, err := service.Create(ctx, "owner-b", second.ID, "queued", "Queued question")
	must(err)
	// Broken references and active uniqueness are rejected by SQLite itself.
	_, err = db.DB.ExecContext(ctx, `UPDATE runs SET user_id='owner-b' WHERE id=?`, current.ID)
	check(err != nil, "foreign conversation/run accepted")
	_, err = db.DB.ExecContext(ctx, `UPDATE messages SET user_id='owner-b' WHERE run_id=?`, complete.ID)
	check(err != nil, "foreign run/message accepted")
	_, err = db.DB.ExecContext(ctx, `UPDATE runs SET status='queued' WHERE id=?`, complete.ID)
	check(err != nil, "two active runs accepted by schema")
	stop()
	check(currentCtx.Err() == context.Canceled, "server lifetime did not stop worker")
	service.Close()
	server.Close()
	must(db.Close())
	db, err = store.Open(ctx, path)
	must(err)
	defer db.Close()
	must(db.RecoverRuns(ctx))
	must(db.RecoverRuns(ctx))
	for _, item := range []struct{ owner, id string }{{"owner-a", current.ID}, {"owner-b", queued.ID}} {
		r, err := db.Run(ctx, item.owner, item.id)
		must(err)
		check(r.Status == "interrupted" && r.ErrorCode == "run_interrupted" && r.FinishedAt != nil, "unfinished run not recovered")
		events, err := db.RunEvents(ctx, item.owner, item.id, 0, 100)
		must(err)
		check(events[len(events)-1].Type == "run_finished", "recovery event absent")
		n := 0
		for _, event := range events {
			if event.Type == "run_finished" {
				n++
			}
		}
		check(n == 1, "recovery repeated terminal event")
	}
	saved, err := db.Run(ctx, "owner-a", complete.ID)
	must(err)
	check(saved.Status == "completed" && string(saved.Usage) == `{"total_tokens":12}`, "completed state changed")
	savedEvents, err := db.RunEvents(ctx, "owner-a", complete.ID, 0, 100)
	must(err)
	check(len(savedEvents) == len(terminalEvents), "terminal cancellation/recovery changed events")
	messages, err = db.Messages(ctx, "owner-a", conversation.ID)
	must(err)
	check(len(messages) == 7 && messages[1].ReasoningContent != nil, "history lost on reopen")
	service = research.New(ctx, db)
	defer service.Close()
	replay, created, err := service.Create(ctx, "owner-a", conversation.ID, "unfinished", "Interrupted question")
	must(err)
	check(!created && replay.ID == current.ID && replay.Status == "interrupted", "replayed interrupted run")
	// Other terminal paths use the same persisted state machine.
	for _, status := range []string{"partial", "failed"} {
		r, _, err := service.Create(ctx, "owner-a", conversation.ID, status, status)
		must(err)
		_, _, err = service.Start(ctx, "owner-a", r.ID)
		must(err)
		r, err = service.Finish(ctx, "owner-a", r.ID, status, "budget_exceeded", nil)
		must(err)
		check(r.Status == status, "terminal path failed")
	}
	fmt.Println("PASS: completed history/usage preserved, FK and active constraints, queued/running recovery, repeated recovery and idempotency, partial/failed states")
}
