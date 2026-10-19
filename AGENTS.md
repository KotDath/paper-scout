# Paper Scout implementation instructions

Read `docs/requirements.md`, `docs/decisions.md`, `docs/architecture.md`,
`docs/api-and-data.md`, and the assigned row of `docs/implementation-dag.md`
before implementing. `docs/execution-protocol.md` governs delegated execution.

- Scope is day 35 only. No support/FAQ/CRM/MCP requirement remains.
- Go API, Flutter client, local laptop. Two accounts with real isolation.
- Own agent harness and one DeepSeek Chat Completions client. No agent libraries.
- Hybrid RAG is mandatory: BM25 + Qwen3 embeddings in existing Ollama + RRF.
- Read full available article text without OCR; distinguish complete, partial,
  and abstract-only analysis. Retrieval is not proof of full reading.
- Unit tests are forbidden. Integration checks are allowed. Do not create or
  retain generated unit/widget tests. Do not substitute mocks for live evidence.
- Device UI interaction must use claude-in-mobile. adb is permitted for build
  installation, reverse networking, screen recording, and artifact transfer.
- Delegated role threads must work only in their assigned worktree and branch.
  Read other source repositories only as references. Role threads must not
  change main, merge other branches, push, or publish.
- The root coordinator may merge approved task branches into main and push
  project commits to origin; the user has authorized push. This authorization
  does not include deployment or publishing releases.
- No extra delegation from role threads; node coordination owns their lifecycle.
- Never read/print secret-bearing dotenv files. App processes may load credentials
  from the shared root `.env` by path; log only presence and success/failure.
- Commit your task changes; leave your worktree clean when handing off.
- Use task-specific migration filenames and avoid unrelated changes to common
  files. Respect existing interfaces; report a concrete conflict rather than
  introducing duplicate core entities.
- Use ephemeral ports and temporary databases for checks so independent nodes
  do not interfere. Keep local runtime files, credentials, PDFs and videos out
  of Git. Record checks and limitations honestly.
