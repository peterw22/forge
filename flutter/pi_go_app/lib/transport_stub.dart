import 'transport_base.dart';

bool get nativeSocketsSupported => false;
bool get webSocketOnlyClient => true;
String get defaultUnixAddress => '';
String get defaultWebSocketAddress => 'ws://127.0.0.1:7346/ws';
Future<AgentTransport> connectUnixTransport(String path) =>
    Future.error(UnsupportedError('Unix sockets are unavailable'));
Future<AgentTransport> connectTcpTransport(String value) =>
    Future.error(UnsupportedError('TCP sockets are unavailable'));
Future<AgentTransport> connectWebSocketTransport(String value) =>
    Future.error(UnsupportedError('WebSockets are unavailable'));
