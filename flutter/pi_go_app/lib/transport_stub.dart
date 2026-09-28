import 'transport_base.dart';

bool get flatpakFrontend => false;
bool get nativeSocketsSupported => false;
bool get webSocketOnlyClient => true;
bool get localAgentSupported => false;
String get defaultLocalAddress => '';
String get defaultUnixAddress => '';
String get defaultWebSocketAddress => 'ws://127.0.0.1:7346/ws';
Future<AgentTransport> connectLocalTransport({String? workingDirectory}) =>
    Future.error(UnsupportedError('The bundled local agent is unavailable'));
Future<AgentTransport> connectUnixTransport(String path) =>
    Future.error(UnsupportedError('Unix sockets are unavailable'));
Future<AgentTransport> connectTcpTransport(String value) =>
    Future.error(UnsupportedError('TCP sockets are unavailable'));
Future<AgentTransport> connectWebSocketTransport(String value) =>
    Future.error(UnsupportedError('WebSockets are unavailable'));
