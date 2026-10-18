# Paper Scout Android client (T13)

Flutter 3.41.4 / Dart 3.11.1. Android app ID: `dev.paperscout.paper_scout`.
This node provides the app shell, configurable Go API transport and real account
login/logout. Research screens are the dependent T14 node.

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
without rebuilding. Use an ephemeral API port for independent node checks.
Local HTTP is enabled only in the debug Android manifest. Release builds require
HTTPS; publishing and release signing are outside this local prototype.

The bearer token is held in memory and sent only to the configured Go origin.
Redirects are disabled. No password/token persistence, logging, agent libraries,
provider calls or background research exists in this client. Restarting the app
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
