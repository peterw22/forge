import 'package:web_socket_channel/web_socket_channel.dart';

import 'transport_base.dart';

bool get nativeSocketsSupported => false;
bool get webSocketOnlyClient => true;
String get defaultUnixAddress => '';
String get defaultWebSocketAddress => 'ws://192.168.50.50:7346/ws';

Future<AgentTransport> connectUnixTransport(String path) =>
    Future.error(UnsupportedError('Unix sockets require the native macOS app'));
Future<AgentTransport> connectTcpTransport(String value) =>
    Future.error(UnsupportedError('Raw TCP requires the native macOS app'));

Future<AgentTransport> connectWebSocketTransport(String value) async {
  final channel = WebSocketChannel.connect(Uri.parse(value));
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
