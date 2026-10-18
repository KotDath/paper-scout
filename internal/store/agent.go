package store

import (
	"context"
	"encoding/json"
)

// AgentMessages selects only completed previous turns and the current run.
// Do not use ConversationRuns' bounded public page to filter private history.
func (s *Store) AgentMessages(ctx context.Context, owner, conversation, run string) ([]Message, error) {
	r, err := s.Run(ctx, owner, run)
	if err != nil {
		return nil, err
	}
	if r.ConversationID != conversation {
		return nil, ErrNotFound
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id,m.run_id,m.seq,m.role,m.content,m.reasoning_content,m.tool_calls_json,m.tool_call_id,m.provider_json,m.created_at FROM messages m JOIN runs r ON r.id=m.run_id WHERE m.user_id=? AND m.conversation_id=? AND (r.status='completed' OR r.id=?) ORDER BY m.seq`, owner, conversation, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m := Message{ConversationID: conversation}
		var calls, provider string
		if err := rows.Scan(&m.ID, &m.RunID, &m.Seq, &m.Role, &m.Content, &m.ReasoningContent, &calls, &m.ToolCallID, &provider, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ToolCalls, m.ProviderJSON = json.RawMessage(calls), json.RawMessage(provider)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveAgentStep durably records accounting and an optional provider message in
// one transaction. A fresh bounded persistence context allows cancellation to
// retain usage and results. Public payloads contain server-generated codes only.
func (s *Store) SaveAgentStep(ctx context.Context, owner, run, kind string, usage, public, private json.RawMessage, message *Message) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=? AND user_id=?`, run, owner))
	if err != nil {
		return err
	}
	if r.Status != "running" {
		return ErrRunState
	}
	if message != nil {
		if _, err = appendMessage(ctx, tx, r, *message); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET usage_json=? WHERE id=? AND user_id=?`, string(usage), run, owner); err != nil {
		return err
	}
	if err = appendEvent(ctx, tx, run, kind, public, private); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveAgentLimits exposes the effective (possibly lowered) execution budget on
// the durable run, rather than showing defaults that this worker will not use.
func (s *Store) SaveAgentLimits(ctx context.Context, owner, run string, limits json.RawMessage) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE runs SET budget_json=? WHERE id=? AND user_id=? AND status='running'`, string(limits), run, owner)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRunState
	}
	return nil
}
