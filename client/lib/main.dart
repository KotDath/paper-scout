import 'package:flutter/material.dart';
import 'auth/auth_controller.dart';
import 'api/api_client.dart';
import 'research/workspace.dart';

void main() => runApp(const PaperScoutApp());

class PaperScoutApp extends StatefulWidget {
  const PaperScoutApp({super.key});
  @override
  State<PaperScoutApp> createState() => _PaperScoutAppState();
}

class _PaperScoutAppState extends State<PaperScoutApp>
    with WidgetsBindingObserver {
  final auth = AuthController();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) auth.refresh();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    auth.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => MaterialApp(
    title: 'Paper Scout',
    debugShowCheckedModeBanner: false,
    theme: ThemeData(
      colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xff245b54)),
      useMaterial3: true,
      inputDecorationTheme: const InputDecorationTheme(
        border: OutlineInputBorder(),
      ),
    ),
    home: ListenableBuilder(
      listenable: auth,
      builder: (context, _) => auth.user == null
          ? LoginScreen(auth: auth)
          : ResearchWorkspace(key: ValueKey(auth.user!.id), auth: auth),
    ),
  );
}

class LoginScreen extends StatefulWidget {
  const LoginScreen({super.key, required this.auth});
  final AuthController auth;
  @override
  State<LoginScreen> createState() => _LoginScreenState();
}

class _LoginScreenState extends State<LoginScreen> {
  final form = GlobalKey<FormState>();
  late final server = TextEditingController(
    text: widget.auth.api.baseUrl.toString(),
  );
  final login = TextEditingController();
  final password = TextEditingController();
  bool obscure = true;

  Future<void> submit() async {
    if (widget.auth.busy || !form.currentState!.validate()) return;
    FocusScope.of(context).unfocus();
    final secret = password.text;
    password.clear();
    await widget.auth.login(server.text, login.text, secret);
  }

  @override
  void dispose() {
    server.dispose();
    login.dispose();
    password.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => Scaffold(
    appBar: AppBar(title: const Text('Paper Scout')),
    body: Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 480),
        child: Form(
          key: form,
          child: ListView(
            padding: const EdgeInsets.all(24),
            shrinkWrap: true,
            children: [
              const Icon(Icons.travel_explore, size: 48),
              const SizedBox(height: 16),
              Text(
                'Вход в аккаунт',
                style: Theme.of(context).textTheme.headlineMedium,
              ),
              const SizedBox(height: 8),
              const Text(
                'Исследуйте научные статьи со своей библиотекой и историей.',
              ),
              const SizedBox(height: 24),
              TextFormField(
                controller: server,
                enabled: !widget.auth.busy,
                decoration: const InputDecoration(labelText: 'Адрес сервера'),
                keyboardType: TextInputType.url,
                autocorrect: false,
                validator: (value) {
                  try {
                    ApiClient.validateBaseUrl(value ?? '');
                    return null;
                  } on ApiException catch (e) {
                    return e.message;
                  }
                },
                textInputAction: TextInputAction.next,
              ),
              const SizedBox(height: 16),
              TextFormField(
                controller: login,
                enabled: !widget.auth.busy,
                decoration: const InputDecoration(labelText: 'Логин'),
                autocorrect: false,
                enableSuggestions: false,
                validator: (value) => value == null || value.trim().isEmpty
                    ? 'Введите логин'
                    : null,
                textInputAction: TextInputAction.next,
              ),
              const SizedBox(height: 16),
              TextFormField(
                controller: password,
                enabled: !widget.auth.busy,
                decoration: InputDecoration(
                  labelText: 'Пароль',
                  suffixIcon: IconButton(
                    tooltip: obscure ? 'Показать пароль' : 'Скрыть пароль',
                    onPressed: () => setState(() => obscure = !obscure),
                    icon: Icon(
                      obscure ? Icons.visibility : Icons.visibility_off,
                    ),
                  ),
                ),
                obscureText: obscure,
                autocorrect: false,
                enableSuggestions: false,
                validator: (value) =>
                    value == null || value.isEmpty ? 'Введите пароль' : null,
                onFieldSubmitted: (_) => submit(),
              ),
              const SizedBox(height: 16),
              if (widget.auth.error != null) ErrorMessage(widget.auth.error!),
              FilledButton(
                onPressed: widget.auth.busy ? null : submit,
                child: Text(widget.auth.busy ? 'Вход…' : 'Войти'),
              ),
              const SizedBox(height: 12),
              const Text(
                'Используйте один из двух аккаунтов, созданных на сервере. После перезапуска потребуется войти снова.',
              ),
            ],
          ),
        ),
      ),
    ),
  );
}

class ErrorMessage extends StatelessWidget {
  const ErrorMessage(this.message, {super.key});
  final String message;
  @override
  Widget build(BuildContext context) => Semantics(
    liveRegion: true,
    child: Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: Text(
        message,
        style: TextStyle(color: Theme.of(context).colorScheme.error),
      ),
    ),
  );
}
