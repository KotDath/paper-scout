package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"paper-scout/internal/deepseek"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

type sourceTransport struct{ base string }

func (s sourceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	url := *r.URL
	check(url.Scheme == "https" && url.Host == "arxiv.org", "source escaped arXiv URL boundary")
	base, err := http.NewRequestWithContext(r.Context(), r.Method, s.base+url.Path, nil)
	if err != nil {
		return nil, err
	}
	copy.URL = base.URL
	copy.Host = base.Host
	return http.DefaultTransport.RoundTrip(copy)
}
func fixtures(db *store.Store) (string, *http.Client, deepseek.Config, func()) {
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/query" {
			http.NotFound(w, r)
			return
		}
		id := r.URL.Query().Get("id_list")
		if id == "" {
			id = "2301.00001v1"
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprintf(w, `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>https://arxiv.org/abs/%s</id><title>Original fixture: agent memory consolidation</title><summary>Original integration fixture: memory consolidation uses episodic storage; evaluation is limited to synthetic tasks. This abstract does not provide numerical experimental evidence.</summary><author><name>Fixture Author</name></author><published>2023-01-01T00:00:00Z</published><updated>2023-01-01T00:00:00Z</updated></entry></feed>`, id)
	}))
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "2301.00002") || strings.HasPrefix(r.URL.Path, "/pdf/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		sentence := "This original fixture describes memory consolidation for AI agents. Episodic memory is summarized into reusable retrieval notes. Evaluation uses synthetic tasks and does not establish real world generalization. "
		fmt.Fprint(w, `<html><article><h1>Memory integration fixture</h1><h2>Methods</h2>`)
		for range 3 {
			fmt.Fprint(w, "<p>"+strings.Repeat(sentence, 28)+"</p>")
		}
		fmt.Fprint(w, "</article></html>")
	}))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var input struct {
			Messages []deepseek.Message `json:"messages"`
			Tools    []deepseek.Tool    `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(400)
			return
		}
		message := deepseek.Text("assistant", "T11_OK")
		finish := "stop"
		if len(input.Tools) == 0 {
			var packet struct {
				Data struct {
					Passages []research.Evidence `json:"passages"`
				} `json:"article_data"`
				Observations []research.Observations `json:"observations"`
			}
			decode(*input.Messages[len(input.Messages)-1].Content, &packet)
			var id string
			if len(packet.Data.Passages) > 0 {
				id = packet.Data.Passages[0].ID
			} else {
				id = packet.Observations[0].Methods[0].EvidenceIDs[0]
			}
			observations := research.Observations{Methods: []research.Claim{{Text: "Авторы описывают консолидацию эпизодической памяти.", EvidenceIDs: []string{id}}}, Results: []research.Claim{}, Limitations: []research.Claim{}}
			body, _ := json.Marshal(observations)
			message = deepseek.Text("assistant", string(body))
		} else {
			prompt := ""
			outputs := []string{}
			for _, m := range input.Messages {
				if m.Role == "user" && m.Content != nil {
					prompt = *m.Content
					outputs = nil
				}
				if m.Role == "tool" && m.Content != nil {
					outputs = append(outputs, *m.Content)
				}
			}
			call := func(name string, args any) deepseek.ToolCall {
				raw, _ := json.Marshal(args)
				return deepseek.ToolCall{ID: fmt.Sprintf("call-%s-%d", name, len(outputs)), Type: "function", Function: deepseek.FunctionCall{Name: name, Arguments: string(raw)}}
			}
			var calls []deepseek.ToolCall
			switch {
			case strings.HasPrefix(prompt, "T11_PRIMARY"):
				switch len(outputs) {
				case 0:
					calls = []deepseek.ToolCall{call("search_papers", map[string]any{"query": "ti:memory", "limit": 1})}
				case 1:
					calls = []deepseek.ToolCall{call("import_paper", map[string]string{"arxiv_id": "2301.00001v1", "selection_reason": "memory mechanism"})}
				case 2:
					var out struct {
						Paper struct {
							ID string `json:"id"`
						} `json:"paper"`
					}
					decode(outputs[1], &out)
					calls = []deepseek.ToolCall{call("analyze_paper", map[string]string{"paper_version_id": out.Paper.ID, "question": "Memory methods and limitations?"})}
				default:
					message = deepseek.Text("assistant", "Полный анализ complete. "+firstEvidence(outputs[2]))
				}
			case strings.HasPrefix(prompt, "T11_FOLLOWUP"):
				switch len(outputs) {
				case 0:
					calls = []deepseek.ToolCall{call("search_library", map[string]any{"query": "Как работает консолидация памяти?", "lexical_terms": "memory consolidation", "limit": 2})}
				case 1:
					calls = []deepseek.ToolCall{call("read_passages", map[string]any{"evidence_ids": []string{firstEvidence(outputs[0])}})}
				default:
					message = deepseek.Text("assistant", "Фрагмент описывает консолидацию эпизодической памяти: "+firstEvidence(outputs[1]))
				}
			case strings.HasPrefix(prompt, "T11_BUDGET"):
				if len(outputs) == 0 {
					calls = []deepseek.ToolCall{call("import_paper", map[string]string{"arxiv_id": "2301.00001v1"})}
				} else {
					var out struct {
						Paper struct {
							ID string `json:"id"`
						} `json:"paper"`
					}
					decode(outputs[0], &out)
					calls = []deepseek.ToolCall{call("analyze_paper", map[string]string{"paper_version_id": out.Paper.ID, "question": "Memory mechanism?"})}
				}
			case strings.HasPrefix(prompt, "T11_INVALID"):
				if len(outputs) == 0 {
					calls = []deepseek.ToolCall{
						call("search_papers", map[string]any{"query": "memory", "owner": "bob"}),
						call("search_papers", map[string]any{"query": "memory", "from": "2024-04-01", "to": "2023-01-01"}),
						call("search_papers", map[string]any{"query": "memory", "limit": 11}),
						call("import_paper", map[string]any{"arxiv_id": "http://127.0.0.1/private"}),
						call("analyze_paper", map[string]any{"paper_version_id": strings.TrimPrefix(prompt, "T11_INVALID "), "question": "unselected public cache"}),
						call("read_passages", map[string]any{"evidence_ids": []string{"foreign-ev"}}),
					}
				}
			case strings.HasPrefix(prompt, "T11_FOREIGN"):
				if len(outputs) == 0 {
					ids := strings.Fields(prompt)
					calls = []deepseek.ToolCall{call("read_passages", map[string]any{"evidence_ids": []string{ids[1]}}), call("analyze_paper", map[string]any{"paper_version_id": ids[2], "question": "other user's paper"}), call("search_library", map[string]any{"query": "memory", "paper_version_ids": []string{ids[2]}})}
				}
			case strings.HasPrefix(prompt, "T11_IMPORTS"):
				if len(outputs) == 0 {
					for i := 1; i <= 4; i++ {
						c := call("import_paper", map[string]string{"arxiv_id": fmt.Sprintf("2301.%05dv1", i)})
						c.ID = fmt.Sprintf("import-%d", i)
						calls = append(calls, c)
					}
				} else if len(outputs) == 4 {
					var out struct {
						Paper struct {
							ID string `json:"id"`
						} `json:"paper"`
					}
					decode(outputs[1], &out)
					calls = []deepseek.ToolCall{call("analyze_paper", map[string]string{"paper_version_id": out.Paper.ID, "question": "What does the abstract claim?"})}
				}
			case strings.HasPrefix(prompt, "T11_DEGRADED"):
				if len(outputs) == 0 {
					calls = []deepseek.ToolCall{call("import_paper", map[string]string{"arxiv_id": "2301.00001v1"})}
				} else if len(outputs) == 1 {
					calls = []deepseek.ToolCall{call("search_library", map[string]string{"query": "memory"})}
				}
			}
			for i := range calls {
				calls[i].ID = fmt.Sprintf("call-%d-%d", len(outputs), i)
			}
			if len(calls) > 0 {
				message = deepseek.Message{Role: "assistant", ToolCalls: calls}
				finish = "tool_calls"
			}
		}
		must(json.NewEncoder(w).Encode(map[string]any{"id": "fixture-researchtools", "model": "fixture", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 30, "total_tokens": 130}}))
	}))
	return catalog.URL + "/api/query", &http.Client{Transport: sourceTransport{source.URL}}, deepseek.Config{BaseURL: provider.URL, Model: "fixture", APIKey: "integration-only", Thinking: "disabled"}, func() { provider.Close(); source.Close(); catalog.Close() }
}
func firstEvidence(raw string) string {
	var value any
	must(json.Unmarshal([]byte(raw), &value))
	var walk func(any) string
	walk = func(v any) string {
		switch x := v.(type) {
		case map[string]any:
			if id, ok := x["evidence_id"].(string); ok {
				return id
			}
			if ids, ok := x["evidence_ids"].([]any); ok && len(ids) > 0 {
				return ids[0].(string)
			}
			for _, item := range x {
				if id := walk(item); id != "" {
					return id
				}
			}
		case []any:
			for _, item := range x {
				if id := walk(item); id != "" {
					return id
				}
			}
		}
		return ""
	}
	id := walk(value)
	check(id != "", "no evidence ID in result")
	return id
}

// Ensure source transport implements the HTTP boundary, not a documents mock.
var _ http.RoundTripper = sourceTransport{}
