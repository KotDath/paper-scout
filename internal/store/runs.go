package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrActiveRun   = errors.New("conversation already has an active run")
	ErrIdempotency = errors.New("client request ID has different content")
	ErrRunCapacity = errors.New("server run capacity reached")
	ErrRunState    = errors.New("run is not in the required state")
)

func timestamp() string          { return time.Now().UTC().Format(time.RFC3339Nano) }
func newID(prefix string) string { return prefix + rand.Text() }

type Conversation struct {
	ID        string `json:"id"`
	UserID    string `json:"-"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) CreateConversation(ctx context.Context, owner, title string) (Conversation, error) {
	c := Conversation{ID: newID("conv_"), UserID: owner, Title: title, CreatedAt: timestamp()}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO conversations(id,user_id,title,created_at) VALUES (?,?,?,?)`, c.ID, owner, title, c.CreatedAt)
	return c, err
}

func (s *Store) Conversation(ctx context.Context, owner, id string) (Conversation, error) {
	var c Conversation
	err := s.DB.QueryRowContext(ctx, `SELECT id,user_id,title,created_at FROM conversations WHERE id=? AND user_id=?`, id, owner).Scan(&c.ID, &c.UserID, &c.Title, &c.CreatedAt)
	return c, notFound(err)
}

// Cursor is the last opaque ID from a page, ordered by ID (not creation time).
func (s *Store) Conversations(ctx context.Context, owner, cursor string, limit int) ([]Conversation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,user_id,title,created_at FROM conversations WHERE user_id=? AND id>? ORDER BY id LIMIT ?`, owner, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.UserID, &c.Title, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type Run struct {
	ID              string          `json:"id"`
	UserID          string          `json:"-"`
	ConversationID  string          `json:"conversation_id"`
	ClientRequestID string          `json:"client_request_id"`
	RequestMessage  string          `json:"-"`
	Status          string          `json:"status"`
	CancelRequested bool            `json:"cancel_requested"`
	CreatedAt       string          `json:"created_at"`
	StartedAt       *string         `json:"started_at"`
	FinishedAt      *string         `json:"finished_at"`
	ErrorCode       string          `json:"error_code,omitempty"`
	Budget          json.RawMessage `json:"budget"`
	Usage           json.RawMessage `json:"usage"`
	ReportID        *string         `json:"report_id"`
}

const runColumns = `id,user_id,conversation_id,client_request_id,request_message,status,cancel_requested,created_at,started_at,finished_at,error_code,budget_json,usage_json,report_id`

type scanner interface{ Scan(...any) error }

func scanRun(row scanner) (Run, error) {
	var r Run
	var budget, usage string
	err := row.Scan(&r.ID, &r.UserID, &r.ConversationID, &r.ClientRequestID, &r.RequestMessage, &r.Status, &r.CancelRequested, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.ErrorCode, &budget, &usage, &r.ReportID)
	r.Budget = json.RawMessage(budget)
	r.Usage = json.RawMessage(usage)
	return r, notFound(err)
}

func (s *Store) Run(ctx context.Context, owner, id string) (Run, error) {
	return scanRun(s.DB.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=? AND user_id=?`, id, owner))
}

func (s *Store) ConversationRuns(ctx context.Context, owner, conversation string) ([]Run, error) {
	if _, err := s.Conversation(ctx, owner, conversation); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE user_id=? AND conversation_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, owner, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateRun commits the user turn, queue event and run together. Idempotency is
// checked before capacity/active checks, including for already terminal runs.
func (s *Store) CreateRun(ctx context.Context, owner, conversation, requestID, message string) (Run, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id=? AND user_id=?`, conversation, owner).Scan(&exists)
	if err != nil {
		return Run{}, false, notFound(err)
	}
	previous, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE user_id=? AND conversation_id=? AND client_request_id=?`, owner, conversation, requestID))
	if err == nil {
		if previous.RequestMessage != message {
			return Run{}, false, ErrIdempotency
		}
		return previous, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Run{}, false, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE conversation_id=? AND status IN ('queued','running')`, conversation).Scan(&active); err != nil {
		return Run{}, false, err
	}
	if active > 0 {
		return Run{}, false, ErrActiveRun
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE status IN ('queued','running')`).Scan(&active); err != nil {
		return Run{}, false, err
	}
	if active >= 2 {
		return Run{}, false, ErrRunCapacity
	}
	r := Run{ID: newID("run_"), UserID: owner, ConversationID: conversation, ClientRequestID: requestID, RequestMessage: message, Status: "queued", CreatedAt: timestamp(), Budget: json.RawMessage(`{"tool_calls":24,"deepseek_requests":32,"tokens":250000,"output_tokens":4096,"duration_seconds":900}`), Usage: json.RawMessage(`{}`)}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,created_at,budget_json) VALUES (?,?,?,?,?,?,?,?)`, r.ID, owner, conversation, requestID, message, r.Status, r.CreatedAt, string(r.Budget))
	if err != nil {
		return Run{}, false, err
	}
	text := message
	if _, err = appendMessage(ctx, tx, r, Message{Role: "user", Content: &text}); err != nil {
		return Run{}, false, err
	}
	if err = appendEvent(ctx, tx, r.ID, "run_queued", json.RawMessage(`{"status":"queued"}`), nil); err != nil {
		return Run{}, false, err
	}
	return r, true, tx.Commit()
}

// Message is private server history. JSON deliberately hides provider fields;
// PublicMessagesPage is the only projection intended for HTTP clients.
type Message struct {
	ID               string          `json:"id"`
	ConversationID   string          `json:"conversation_id"`
	RunID            string          `json:"run_id"`
	Seq              int64           `json:"seq"`
	Role             string          `json:"role"`
	Content          *string         `json:"content"`
	ReasoningContent *string         `json:"-"`
	ToolCalls        json.RawMessage `json:"-"`
	ToolCallID       string          `json:"-"`
	ProviderJSON     json.RawMessage `json:"-"`
	CreatedAt        string          `json:"created_at"`
}

func appendMessage(ctx context.Context, tx *sql.Tx, r Run, m Message) (Message, error) {
	m.ID = newID("msg_")
	m.ConversationID = r.ConversationID
	m.RunID = r.ID
	m.CreatedAt = timestamp()
	if len(m.ToolCalls) == 0 {
		m.ToolCalls = json.RawMessage(`[]`)
	}
	if len(m.ProviderJSON) == 0 {
		m.ProviderJSON = json.RawMessage(`{}`)
	}
	err := tx.QueryRowContext(ctx, `INSERT INTO messages(id,user_id,conversation_id,run_id,seq,role,content,reasoning_content,tool_calls_json,tool_call_id,provider_json,created_at)
        SELECT ?,?,?,?,COALESCE(MAX(seq),0)+1,?,?,?,?,?,?,? FROM messages WHERE conversation_id=? RETURNING seq`, m.ID, r.UserID, r.ConversationID, r.ID, m.Role, m.Content, m.ReasoningContent, string(m.ToolCalls), m.ToolCallID, string(m.ProviderJSON), m.CreatedAt, r.ConversationID).Scan(&m.Seq)
	return m, err
}

func (s *Store) AppendMessage(ctx context.Context, owner, runID string, m Message) (Message, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	r, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=? AND user_id=?`, runID, owner))
	if err != nil {
		return Message{}, err
	}
	if r.Status != "running" {
		return Message{}, ErrRunState
	}
	m, err = appendMessage(ctx, tx, r, m)
	if err != nil {
		return Message{}, err
	}
	return m, tx.Commit()
}

// Messages preserves every original provider turn, even interrupted turns.
// The future context builder must select coherent completed turns explicitly.
func (s *Store) Messages(ctx context.Context, owner, conversation string) ([]Message, error) {
	if _, err := s.Conversation(ctx, owner, conversation); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,conversation_id,run_id,seq,role,content,reasoning_content,tool_calls_json,tool_call_id,provider_json,created_at FROM messages WHERE user_id=? AND conversation_id=? ORDER BY seq`, owner, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var calls, provider string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.RunID, &m.Seq, &m.Role, &m.Content, &m.ReasoningContent, &calls, &m.ToolCallID, &provider, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ToolCalls = json.RawMessage(calls)
		m.ProviderJSON = json.RawMessage(provider)
		out = append(out, m)
	}
	return out, rows.Err()
}

// PublicMessagesPage selects only UI fields in SQL, without loading private
// reasoning, tool results or provider payloads into the HTTP projection.
func (s *Store) PublicMessagesPage(ctx context.Context, owner, conversation string, after int64, limit int) ([]Message, error) {
	if _, err := s.Conversation(ctx, owner, conversation); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,conversation_id,run_id,seq,role,content,created_at FROM messages WHERE user_id=? AND conversation_id=? AND seq>? AND role IN ('user','assistant') AND content IS NOT NULL AND content!='' ORDER BY seq LIMIT ?`, owner, conversation, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.RunID, &m.Seq, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type RunEvent struct {
	RunID     string          `json:"run_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

func appendEvent(ctx context.Context, tx *sql.Tx, runID, kind string, public, private json.RawMessage) error {
	if len(public) == 0 {
		public = json.RawMessage(`{}`)
	}
	if len(private) == 0 {
		private = json.RawMessage(`{}`)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO run_events(run_id,seq,event_type,public_payload_json,private_payload_json,created_at) SELECT ?,COALESCE(MAX(seq),0)+1,?,?,?,? FROM run_events WHERE run_id=?`, runID, kind, string(public), string(private), timestamp(), runID)
	return err
}

// Public payloads must be constructed from safe progress fields by server code,
// never by forwarding a provider response/error. Private data is not projected.
func (s *Store) AppendEvent(ctx context.Context, owner, runID, kind string, public, private json.RawMessage) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=? AND user_id=?`, runID, owner))
	if err != nil {
		return err
	}
	if r.Status != "running" {
		return ErrRunState
	}
	if err = appendEvent(ctx, tx, runID, kind, public, private); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RunEvents(ctx context.Context, owner, runID string, after int64, limit int) ([]RunEvent, error) {
	if _, err := s.Run(ctx, owner, runID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT e.run_id,e.seq,e.event_type,e.public_payload_json,e.created_at FROM run_events e JOIN runs r ON r.id=e.run_id WHERE r.user_id=? AND e.run_id=? AND e.seq>? ORDER BY e.seq LIMIT ?`, owner, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunEvent{}
	for rows.Next() {
		var e RunEvent
		var payload string
		if err := rows.Scan(&e.RunID, &e.Seq, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func activeStatus(status string) bool { return status == "queued" || status == "running" }

// transitionRun updates state and its event atomically. Cancellation wins a
// race with completion once cancel_requested has committed. Terminal runs are
// immutable, including on repeated cancel and recovery calls.
func (s *Store) transitionRun(ctx context.Context, owner, id, target, code string, usage json.RawMessage) (Run, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=? AND user_id=?`, id, owner))
	if err != nil {
		return Run{}, err
	}
	if !activeStatus(r.Status) {
		if target == "running" {
			return Run{}, ErrRunState
		}
		return r, nil
	}
	kind := "run_finished"
	now := timestamp()
	if target == "running" {
		if r.Status != "queued" || r.CancelRequested {
			return Run{}, ErrRunState
		}
		r.StartedAt = &now
		kind = "run_started"
	} else if target == "cancel_requested" {
		if r.CancelRequested {
			return r, nil
		}
		r.CancelRequested = true
		if r.Status == "running" {
			target = "running"
			kind = "cancel_requested"
		} else {
			target = "cancelled"
			r.FinishedAt = &now
		}
	} else {
		if target != "interrupted" && r.CancelRequested {
			target = "cancelled"
			code = ""
		}
		if target != "interrupted" && r.Status != "running" {
			return Run{}, ErrRunState
		}
		r.FinishedAt = &now
	}
	if len(usage) > 0 {
		r.Usage = usage
	}
	r.Status = target
	r.ErrorCode = code
	_, err = tx.ExecContext(ctx, `UPDATE runs SET status=?,cancel_requested=?,started_at=?,finished_at=?,error_code=?,usage_json=? WHERE id=? AND user_id=?`, r.Status, r.CancelRequested, r.StartedAt, r.FinishedAt, r.ErrorCode, string(r.Usage), id, owner)
	if err != nil {
		return Run{}, err
	}
	public, _ := json.Marshal(map[string]any{"status": r.Status, "error_code": r.ErrorCode, "cancel_requested": r.CancelRequested})
	if err = appendEvent(ctx, tx, id, kind, public, nil); err != nil {
		return Run{}, err
	}
	return r, tx.Commit()
}

func (s *Store) StartRun(ctx context.Context, owner, id string) (Run, error) {
	return s.transitionRun(ctx, owner, id, "running", "", nil)
}
func (s *Store) CancelRun(ctx context.Context, owner, id string) (Run, error) {
	return s.transitionRun(ctx, owner, id, "cancel_requested", "", nil)
}
func (s *Store) FinishRun(ctx context.Context, owner, id, status, code string, usage json.RawMessage) (Run, error) {
	switch status {
	case "completed", "partial", "failed", "cancelled":
	default:
		return Run{}, ErrRunState
	}
	return s.transitionRun(ctx, owner, id, status, code, usage)
}

// RecoverRuns is invoked once before listening, never during normal requests.
// It does not restart work or issue any provider calls.
func (s *Store) RecoverRuns(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,user_id FROM runs WHERE status IN ('queued','running')`)
	if err != nil {
		return err
	}
	type key struct{ id, owner string }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.owner); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, k := range keys {
		if _, err := s.transitionRun(ctx, k.owner, k.id, "interrupted", "run_interrupted", nil); err != nil {
			return err
		}
	}
	return nil
}
