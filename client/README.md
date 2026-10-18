# Paper Scout Android client (T13–T14)

Flutter 3.41.4 / Dart 3.11.1. Android app ID: `dev.paperscout.paper_scout`.
The app includes configurable Go API transport, account login/logout, interests,
conversation history, research progress, reports/evidence and a personal library.

Start the Go API and seed two accounts as described in [accounts](../docs/accounts.md).
Do not pass server dotenv files, provider credentials or passwords to Flutter.

From `client/`:

```sh
flutter pub get
flutter analyze
flutter build apk --debug
adb -s <emulator-id> install -r build/app/outputs/flutter-apk/app-debug.apk
adb -s <emulator-id> reverse tcp:<api-port> tcp:<api-port>
flutter run -d <emulator-id> --dart-define=API_BASE_URL=http://127.0.0.1:<api-port>
```

`API_BASE_URL` is a server origin with no API path, credentials, query or fragment;
default: `http://127.0.0.1:8080`. The login screen also accepts a different address
without rebuilding. This field is kept only in memory; after process restart it
returns to the compile-time `API_BASE_URL`. Use an ephemeral API port for
independent node checks.
Local HTTP is enabled only in the debug Android manifest. Release builds require
HTTPS; publishing and release signing are outside this local prototype.

The bearer token is held in memory and sent only to the configured Go origin.
Redirects are disabled. No password/token persistence, logging, agent libraries,
direct provider calls exist in this client. Research is executed by Go; the
client polls public events once a second while a run is active and stops polling
in the background. Restarting the app
requires login again; server-side data remains intact. Logout clears the local
session after server revocation; a network failure keeps the session so logout
can be retried. Protected 401 clears the account and returns to login, including
when checking the session after the app resumes. Login 401 shows a credential
error. Account-keyed screen state is discarded on logout/account change.

From the repository root, run the production Dart transport integration against
real Go HTTP and temporary SQLite (no mocks or unit/widget tests):

```sh
python3 integration/flutter_client.py
```

Device interactions during verification must use `claude-in-mobile`; adb is used
only for installation, reverse networking and artifact operations.


Verified API 35 emulator startup on this laptop (from the T13 worktree root,
for the existing worktree-local `PaperScout_T13` AVD):

```sh
ANDROID_AVD_HOME="$PWD/.bb/runtime/t13/avd" QT_QPA_PLATFORM=xcb \
  /home/kotdath/Android/Sdk/emulator/emulator -avd PaperScout_T13 \
  -gpu host -feature -Vulkan -no-snapshot -no-audio -no-boot-anim \
  -camera-back none -camera-front none -port 5582 -memory 2048 -cores 2
```

Keep an already running emulator available to other node roles. On this host,
SwiftShader JIT triggered a SELinux `execheap` denial and emulator boot crashes;
the host renderer works with SELinux Enforcing. Wait until `claude-in-mobile`
reports `emulator-5582` as `device`, then install the APK and reverse the actual
Go port. Use mobile app launch/restart and input/UI tools for the verification
steps. The passed scenarios and exact artifact hash are in
[T13 verification](../docs/verification/T13.md).


## Research UI (T14)

Create a named conversation in **Диалоги**, enter a question and start research.
The screen shows actual run state, public events, source coverage and cancellation.
Messages are expandable so long answers do not hide status and controls. Open a
completed report, its source paper or a cited evidence fragment. Evidence displays
original text, section/page/Unicode range and an HTTPS link opened in the Android
browser. Reports preserve the server Markdown text; citations are separate buttons
with exact evidence IDs and coordinates. Unresolved citations stay limitations.

Use the same question field for follow-up. A transport failure retains the original
message/request ID for a safe retry. One active run blocks additional submissions;
failed/cancelled/interrupted states do not fabricate a report. During a follow-up,
you can select and open a previous report while polling continues. The separate
active-run panel keeps cancellation available and additional submissions blocked. The source's analysis,
extraction and index statuses remain separate. The **Библиотека** tab lists imported
papers, exposes explicit saved/read marks and personal notes, and searches passages
with actual BM25/dense/RRF ranks and degradation reasons. Note/mark edits require
**Сохранить заметку и отметки** and show success only after the server responds.
The **Профиль** tab edits interests and switches accounts. Account-specific screens,
resource navigation and polling are disposed on logout/401. Login after an app
restart reloads server history; tokens remain in memory only.

Reproducible transport/repository integration (external arXiv/article/DeepSeek
boundaries are HTTP fixtures, internal modules and installed Ollama are real):

```sh
python3 integration/flutter_research.py
```

For device UI verification, start a worktree-local temporary server on port 0:

```sh
DEEPSEEK_MODEL=deepseek-flash DEEPSEEK_THINKING=disabled \
  python3 integration/flutter_research.py --ui --live \
  --env-file /home/kotdath/omp/personal/ai/paper-scout/.env
```

Only Go loads dotenv; the launcher never reads its contents. Use the printed
loopback origin in the app and add only its actual port with `adb reverse`.
The temporary accounts use the same public integration-only credentials as the
T12 harness (see `integration/researchtools/flutter.go`); never use them for real
accounts. All UI actions use `claude-in-mobile`. Stop this server with SIGTERM
when done; the launcher removes its temporary DB/source cache. Without `--live`,
`--ui` explicitly uses the HTTP fixtures. Limits remain 12 requests, 10 tools,
180000 tokens, 2048 output tokens and no retries per run; Analyzer allows 6 calls.
A long final answer can hit `output_limit`; ask for a concise answer and inspect
actual status rather than treating a truncated message as a saved report.

For isolated local builds, point `PUB_CACHE`, `GRADLE_USER_HOME`,
`ANDROID_USER_HOME`, `GOCACHE`, `GOMODCACHE`, `GOPATH` and `TMPDIR` at ignored
subdirectories under this worktree before running commands. Existing SDK and
cached dependencies may be read as inputs; all build outputs stay here. The
installed app and rebuilt APK must use the same debug signing key, or replace the
old disposable app through Android Settings using the mobile UI tools.

Actual scope, commands, results and live verification gaps:
[T14 verification](../docs/verification/T14.md). The day-35 acceptance and video
remain separate DAG tasks T16–T18.

## Research memory (T21)

Reports may include nullable `research_memory` with `retrieval_mode`, `reasons`,
`recalled` and `referenced` (arrays of the T19 Hit contract). Historical reports
without this field still open. **Найдено в прошлых исследованиях** shows the
immutable materials issued to that run; **Ссылки в ответе** shows only server
validated references. A recalled item alone does not establish use in the answer.
Only markers present in `referenced` receive a memory navigation button.

Open an entry to inspect current provenance, coverage, notes, corrections and
revision, then follow its report, paper/note or original evidence. Returning keeps
the selected run, including a manually selected older run. A historical report
opened from an entry also fetches current annotations beside its immutable text.
Correction and exclusion/restore use `expected_revision`; after a failed PATCH,
reload the current revision before explicitly resubmitting the preserved draft.
User corrections are edited through their target, as required by T19. Notes remain
in the existing paper editor with an explanation of their future use as personal
context. Memory previews/navigation live in the account widget tree and are
removed on logout/401.

Run the repository integration, then optionally serve for device interaction:

```sh
python3 integration/flutter_memory.py
python3 integration/flutter_memory.py --ui
```

Use a worktree-local Flutter/Dart and build caches as described above. The launcher
uses port 0 and a temporary SQLite database. It needs the real installed Qwen3 at
`127.0.0.1:11434`, but no dotenv, DeepSeek or arXiv. Research content and the optional
T20 report snapshot are **explicit fixtures**, using real HTTP auth, memory
GET/PATCH/search and SQLite. Fixture accounts are `owner_a` / `owner_b`, with public
disposable input credentials in the harness; never reuse them outside integration.
Add an adb reverse rule for the printed port and enter that origin in Flutter.

In account A, conversation **b** contains a newer failed run and an older completed
run: select the completed run to open the snapshot. Its claim/note point to **a**.
Reports **c/d** deliberately supply empty lexical-only / hybrid-partial snapshots,
with `T21_MODE_HTTP_FIXTURE_NO_REAL_OUTAGE`; **a/e** omit / null the extension.
These fixtures verify presentation, not automatic recall or live scientific
comparison. Device input/navigation uses only claude-in-mobile. Stop with SIGTERM
or Ctrl-C to remove the temporary database. Exact checks and limits:
[T21 verification](../docs/verification/T21.md).
