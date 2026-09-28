import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:web_socket_channel/web_socket_channel.dart';

import 'transport_base.dart';

bool get flatpakFrontend =>
    Platform.isLinux &&
    (Platform.environment.containsKey('FLATPAK_ID') ||
        File('/.flatpak-info').existsSync());
bool get nativeSocketsSupported =>
    (Platform.isMacOS || Platform.isLinux) && !flatpakFrontend;
bool get webSocketOnlyClient => Platform.isIOS || flatpakFrontend;
bool get localAgentSupported => nativeSocketsSupported;
String get defaultLocalAddress =>
    Platform.environment['HOME'] ?? 'Home directory';
String get defaultWebSocketAddress =>
    Platform.isAndroid || Platform.isIOS || flatpakFrontend
    ? 'ws://192.168.50.50:7346/ws'
    : 'ws://127.0.0.1:7346/ws';
String get defaultUnixAddress =>
    '${Platform.environment['HOME'] ?? ''}/.pi-go/agent.sock';

/// Never search the current workspace or PATH for executable helpers. A project
/// can contain an untrusted pi-go-agent. Development uses an explicit override.
Future<String> resolveAgentExecutable() async {
  final override = Platform.environment['PI_GO_AGENT_BIN']?.trim();
  final executableDirectory = File(Platform.resolvedExecutable).parent;
  final bundled = Platform.isMacOS
      ? '${executableDirectory.parent.path}/Helpers/pi-go-agent'
      : '${executableDirectory.path}/helpers/pi-go-agent';
  final executable = override != null && override.isNotEmpty
      ? override
      : bundled;
  if (!executable.startsWith('/')) {
    throw StateError('PI_GO_AGENT_BIN must be an absolute path');
  }
  if (!await File(executable).exists()) {
    throw StateError(
      'pi-go-agent was not found at $executable. '
      'Use scripts/build-${Platform.isMacOS ? 'macos' : 'linux'}-app.sh, '
      'or build the agent and set PI_GO_AGENT_BIN to its absolute path '
      'before flutter run.',
    );
  }
  return executable;
}

Map<String, String> _agentEnvironment(String home) {
  final environment = Map<String, String>.from(Platform.environment);
  final pathParts = <String>[
    '$home/.bun/bin',
    '$home/.local/bin',
    '$home/.cargo/bin',
    '$home/go/bin',
    '$home/.volta/bin',
    ..._nvmNodeBinDirectories(home),
    if (Platform.isMacOS) '/opt/homebrew/bin',
    '/usr/local/bin',
    environment['PATH'] ?? '',
    '/usr/bin',
    '/bin',
  ].where((value) => value.isNotEmpty).toSet().toList();
  environment['PATH'] = pathParts.join(':');
  return environment;
}

Future<AgentTransport> connectLocalTransport({String? workingDirectory}) async {
  if (!localAgentSupported) {
    throw UnsupportedError(
      'Local agents require an unsandboxed macOS or Linux app',
    );
  }
  final home = Platform.environment['HOME']?.trim() ?? '';
  if (home.isEmpty) throw StateError('HOME is not set');
  final workspace = workingDirectory?.trim();
  final cwd = workspace == null || workspace.isEmpty ? home : workspace;
  if (!cwd.startsWith('/') || !await Directory(cwd).exists()) {
    throw StateError('Local workspace must be an existing absolute directory');
  }
  final process = await Process.start(
    await resolveAgentExecutable(),
    ['--serve', '--cwd', cwd],
    workingDirectory: cwd,
    environment: _agentEnvironment(home),
  );
  return _ProcessTransport(process, ownsAgent: true);
}

List<String> _nvmNodeBinDirectories(String home) {
  final versions = Directory('$home/.nvm/versions/node');
  try {
    return versions
        .listSync()
        .whereType<Directory>()
        .map((directory) => '${directory.path}/bin')
        .toList()
      ..sort((left, right) => right.compareTo(left));
  } on FileSystemException {
    return const [];
  }
}

Future<AgentTransport> connectUnixTransport(String path) async {
  if (!nativeSocketsSupported) {
    throw UnsupportedError('Unix sockets require an unsandboxed desktop app');
  }
  final process = await Process.start(await resolveAgentExecutable(), [
    '--connect-unix',
    path,
  ]);
  return _ProcessTransport(process, ownsAgent: false);
}

Future<AgentTransport> connectTcpTransport(String value) async {
  if (flatpakFrontend) {
    throw UnsupportedError('Raw TCP connections are disabled in Flatpak');
  }
  final separator = value.lastIndexOf(':');
  if (separator < 1) throw const FormatException('use host:port');
  final host = value.substring(0, separator);
  final port = int.parse(value.substring(separator + 1));
  return _SocketTransport(await Socket.connect(host, port));
}

Future<AgentTransport> connectWebSocketTransport(String value) async {
  final uri = Uri.parse(value);
  if (flatpakFrontend && _isLoopbackHost(uri.host)) {
    throw UnsupportedError(
      'Loopback Forge servers are disabled in the Flatpak frontend',
    );
  }
  final channel = WebSocketChannel.connect(uri);
  await channel.ready;
  return _WebSocketTransport(channel);
}

bool _isLoopbackHost(String host) {
  final normalized = host.toLowerCase();
  return normalized == 'localhost' ||
      normalized == '127.0.0.1' ||
      normalized == '::1' ||
      normalized == '0.0.0.0' ||
      normalized == '::';
}

class _ProcessTransport implements AgentTransport, TrustedLocalTransport {
  _ProcessTransport(this.process, {required this.ownsAgent}) {
    _stderr = process.stderr
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen((line) {
          stderr.writeln('pi-go-agent: $line');
          _recentErrors.add(line);
          if (_recentErrors.length > 12) _recentErrors.removeAt(0);
        });
  }

  final Process process;
  final bool ownsAgent;
  final List<String> _recentErrors = [];
  late final StreamSubscription<String> _stderr;
  bool _closing = false;

  @override
  Stream<String> get messages async* {
    yield* process.stdout
        .transform(utf8.decoder)
        .transform(const LineSplitter());
    final code = await process.exitCode;
    if (!_closing && code != 0) {
      throw StateError(
        'pi-go-agent exited ($code): ${_recentErrors.join('\n')}',
      );
    }
  }

  @override
  void send(String message) {
    if (!_closing) process.stdin.writeln(message);
  }

  @override
  Future<void> close() async {
    if (_closing) return;
    _closing = true;
    try {
      if (ownsAgent) {
        process.stdin.writeln(
          jsonEncode({'id': 'flutter-local-shutdown', 'type': 'shutdown'}),
        );
      }
      await process.stdin.flush();
      await process.stdin.close();
    } catch (_) {
      // The helper may already have exited.
    }
    // Give protocol shutdown time to abort active work and close session files,
    // then escalate so a local helper never outlives its connection.
    try {
      await process.exitCode.timeout(const Duration(seconds: 2));
    } on TimeoutException {
      process.kill(ProcessSignal.sigterm);
      try {
        await process.exitCode.timeout(const Duration(seconds: 1));
      } on TimeoutException {
        process.kill(ProcessSignal.sigkill);
        await process.exitCode;
      }
    }
    await _stderr.cancel();
    try {
      await process.stdin.close();
    } catch (_) {
      // The process can close stdin first during shutdown.
    }
  }
}

class _SocketTransport implements AgentTransport {
  _SocketTransport(this.socket);
  final Socket socket;

  @override
  Stream<String> get messages => socket
      .cast<List<int>>()
      .transform(utf8.decoder)
      .transform(const LineSplitter());

  @override
  void send(String message) => socket.write('$message\n');

  @override
  Future<void> close() => socket.close();
}

class _WebSocketTransport implements AgentTransport {
  _WebSocketTransport(this.channel);
  final WebSocketChannel channel;

  @override
  Stream<String> get messages =>
      channel.stream.map((value) => value.toString());

  @override
  void send(String message) => channel.sink.add(message);

  @override
  Future<void> close() async => channel.sink.close();
}
