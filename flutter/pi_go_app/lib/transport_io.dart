import 'dart:convert';
import 'dart:io';

import 'package:web_socket_channel/web_socket_channel.dart';

import 'transport_base.dart';

bool get nativeSocketsSupported => Platform.isMacOS || Platform.isLinux;
bool get webSocketOnlyClient => Platform.isMacOS;
String get defaultWebSocketAddress => Platform.isAndroid
    ? 'ws://192.168.50.50:7346/ws'
    : 'ws://127.0.0.1:7346/ws';
String get defaultUnixAddress =>
    '${Platform.environment['HOME'] ?? ''}/.pi-go/agent.sock';

Future<AgentTransport> connectUnixTransport(String path) async {
  final address = InternetAddress(path, type: InternetAddressType.unix);
  return _SocketTransport(await Socket.connect(address, 0));
}

Future<AgentTransport> connectTcpTransport(String value) async {
  final separator = value.lastIndexOf(':');
  if (separator < 1) throw const FormatException('use host:port');
  final host = value.substring(0, separator);
  final port = int.parse(value.substring(separator + 1));
  return _SocketTransport(await Socket.connect(host, port));
}

Future<AgentTransport> connectWebSocketTransport(String value) async {
  final channel = WebSocketChannel.connect(Uri.parse(value));
  await channel.ready;
  return _WebSocketTransport(channel);
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
