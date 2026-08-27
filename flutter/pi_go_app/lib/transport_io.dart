import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:web_socket_channel/web_socket_channel.dart';

import 'transport_base.dart';

bool get flatpakFrontend => Platform.isLinux;
bool get nativeSocketsSupported => Platform.isMacOS;
bool get webSocketOnlyClient =>
    Platform.isMacOS || Platform.isIOS || flatpakFrontend;
bool get localAgentSupported => Platform.isMacOS;
String get defaultLocalAddress =>
    Platform.environment['HOME'] ?? 'Home directory';
String get defaultWebSocketAddress =>
    Platform.isAndroid || Platform.isIOS || flatpakFrontend
    ? 'ws://192.168.50.50:7346/ws'
    : 'ws://127.0.0.1:7346/ws';
String get defaultUnixAddress =>
    '${Platform.environment['HOME'] ?? ''}/.pi-go/agent.sock';

Future<AgentTransport> connectLocalTransport() async {
  if (!Platform.isMacOS) {
    throw UnsupportedError(
      'The bundled local agent is available only on macOS',
    );
  }
  final home = Platform.environment['HOME']?.trim() ?? '';
  if (home.isEmpty) throw StateError('HOME is not set');

  final override = Platform.environment['PI_GO_AGENT_BIN']?.trim();
  final contents = File(Platform.resolvedExecutable).parent.parent;
  final bundled = '${contents.path}/Helpers/pi-go-agent';
  final executable = override != null && override.isNotEmpty
      ? override
      : bundled;
  if (!await File(executable).exists()) {
    throw StateError(
      'Bundled pi-go-agent was not found at $executable. '
      'Build Forge with scripts/build-macos-app.sh.',
    );
  }

  final environment = Map<String, String>.from(Platform.environment);
  final pathParts = <String>[
    '$home/.bun/bin',
    '$home/.local/bin',
    '$home/.volta/bin',
    ..._nvmNodeBinDirectories(home),
    '/opt/homebrew/bin',
    '/usr/local/bin',
    environment['PATH'] ?? '',
  ].where((value) => value.isNotEmpty).toSet().toList();
  environment['PATH'] = pathParts.join(':');

  final process = await Process.start(
    executable,
    ['--serve', '--cwd', home],
    workingDirectory: home,
    environment: environment,
  );
  return _ProcessTransport(process);
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
  if (flatpakFrontend) {
    throw UnsupportedError('Unix socket connections are disabled in Flatpak');
  }
  final address = InternetAddress(path, type: InternetAddressType.unix);
  return _SocketTransport(await Socket.connect(address, 0));
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

class _ProcessTransport implements AgentTransport {
  _ProcessTransport(this.process) {
    _stderr = process.stderr
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen((line) => stderr.writeln('pi-go-agent: $line'));
  }

  final Process process;
  late final StreamSubscription<String> _stderr;
  bool _closing = false;

  @override
  Stream<String> get messages =>
      process.stdout.transform(utf8.decoder).transform(const LineSplitter());

  @override
  void send(String message) {
    if (!_closing) process.stdin.writeln(message);
  }

  @override
  Future<void> close() async {
    if (_closing) return;
    _closing = true;
    try {
      process.stdin.writeln(
        jsonEncode({'id': 'flutter-local-shutdown', 'type': 'shutdown'}),
      );
      await process.stdin.flush();
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
