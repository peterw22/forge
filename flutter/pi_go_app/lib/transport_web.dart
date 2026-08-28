import 'package:web_socket_channel/web_socket_channel.dart';

import 'transport_base.dart';

bool get flatpakFrontend => false;
bool get nativeSocketsSupported => false;
bool get webSocketOnlyClient => true;
bool get localAgentSupported => false;
String get defaultLocalAddress => '';
String get defaultUnixAddress => '';
String get defaultWebSocketAddress => 'wss://forge-agent.example/ws';

Future<AgentTransport> connectLocalTransport() => Future.error(
  UnsupportedError('The bundled local agent requires the native macOS app'),
);
Future<AgentTransport> connectUnixTransport(String path) =>
    Future.error(UnsupportedError('Unix sockets require the native macOS app'));
Future<AgentTransport> connectTcpTransport(String value) =>
    Future.error(UnsupportedError('Raw TCP requires the native macOS app'));

Future<AgentTransport> connectWebSocketTransport(String value) async {
  final uri = Uri.parse(value.trim());
  if (uri.scheme != 'wss' || uri.host.isEmpty) {
    throw const FormatException('Forge Web requires a wss:// URL');
  }
  final channel = WebSocketChannel.connect(uri);
  await channel.ready;
  return _WebSocketTransport(channel);
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
