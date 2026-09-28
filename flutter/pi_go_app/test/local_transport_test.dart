import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/main.dart';
import 'package:pi_go_app/transport.dart';

// Run with PI_GO_AGENT_BIN pointing to a freshly built helper. These tests use
// isolated configuration and no provider calls or personal credentials.
void main() {
  final helper = Platform.environment['PI_GO_AGENT_BIN'];
  final enabled = helper != null && localAgentSupported;

  test('native desktop exposes local agents and socket modes', () {
    if (Platform.isMacOS || Platform.isLinux && !flatpakFrontend) {
      expect(localAgentSupported, isTrue);
      expect(nativeSocketsSupported, isTrue);
      expect(webSocketOnlyClient, isFalse);
      expect(defaultWebSocketAddress, 'ws://127.0.0.1:7346/ws');
    }
  });

  test(
    'owned local helper connects without device pairing in chosen workspace',
    () async {
      final workspace = await Directory.systemTemp.createTemp('forge-local-');
      final connection = AgentConnection();
      try {
        await connection.connect(ConnectionKind.local, workspace.path);
        expect(connection.connected, isTrue, reason: connection.status);
        expect(connection.currentCWD, workspace.path);
        await connection.disconnect();
        expect(connection.connected, isFalse);
      } finally {
        await connection.disconnect();
        connection.dispose();
        await workspace.delete(recursive: true);
      }
    },
    skip: !enabled,
  );

  test(
    'verified Unix connection skips pairing and disconnect keeps server alive',
    () async {
      final workspace = await Directory.systemTemp.createTemp('forge-unix-');
      final socket = '${workspace.path}/agent.sock';
      final server = await Process.start(helper!, [
        '--listen',
        'unix://$socket',
        '--unix-peer-auth',
        '--cwd',
        workspace.path,
      ]);
      final ready = Completer<void>();
      final errors = server.stderr.transform(utf8.decoder).listen((text) {
        if (text.contains('listening on') && !ready.isCompleted) {
          ready.complete();
        }
      });
      final output = server.stdout.drain<void>();
      final connection = AgentConnection();
      try {
        await ready.future.timeout(const Duration(seconds: 10));
        for (var attempt = 0; attempt < 2; attempt++) {
          await connection.connect(ConnectionKind.unix, socket);
          expect(connection.connected, isTrue, reason: connection.status);
          expect(connection.currentCWD, workspace.path);
          await connection.disconnect();
        }
      } finally {
        await connection.disconnect();
        connection.dispose();
        server.kill();
        await server.exitCode.timeout(const Duration(seconds: 5));
        await errors.cancel();
        await output;
        await workspace.delete(recursive: true);
      }
    },
    skip: !enabled,
  );

  test('owned agent exits when its parent closes stdin', () async {
    final workspace = await Directory.systemTemp.createTemp('forge-eof-');
    final process = await Process.start(helper!, [
      '--serve',
      '--cwd',
      workspace.path,
    ]);
    final output = process.stdout.drain<void>();
    final errors = process.stderr.drain<void>();
    try {
      await process.stdin.close();
      expect(await process.exitCode.timeout(const Duration(seconds: 5)), 0);
      await output;
      await errors;
    } finally {
      process.kill();
      await workspace.delete(recursive: true);
    }
  }, skip: !enabled);

  test('Unix helper reports connection errors instead of hanging', () async {
    final connection = AgentConnection();
    await connection.connect(
      ConnectionKind.unix,
      '/tmp/forge-nonexistent-${DateTime.now().microsecondsSinceEpoch}.sock',
    );
    expect(connection.connected, isFalse);
    expect(connection.status, contains('pi-go-agent exited'));
    connection.dispose();
  }, skip: !enabled);

  for (final message in [
    {'type': 'response', 'command': 'get_state'},
    {'type': 'auth_success'},
  ]) {
    test('TCP rejects unverified ${message['type']}', () async {
      final server = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
      final sockets = <Socket>[];
      final subscription = server.listen((socket) {
        sockets.add(socket);
        socket.writeln(jsonEncode(message));
      });
      final connection = AgentConnection();
      try {
        await connection.connect(
          ConnectionKind.tcp,
          '127.0.0.1:${server.port}',
        );
        expect(connection.connected, isFalse);
      } finally {
        await connection.disconnect();
        connection.dispose();
        for (final socket in sockets) {
          socket.destroy();
        }
        await subscription.cancel();
        await server.close();
      }
    });
  }

  test('missing local workspace reports an actionable failure', () async {
    final connection = AgentConnection();
    await connection.connect(
      ConnectionKind.local,
      '/forge-missing-workspace-${DateTime.now().microsecondsSinceEpoch}',
    );
    expect(connection.connected, isFalse);
    expect(connection.status, contains('existing absolute directory'));
    connection.dispose();
  }, skip: !localAgentSupported);
}
