import 'push_identity.dart';

bool get webPushSupported => false;
String get webPushPermission => 'denied';
Future<bool> requestWebPushPermission() async => false;
Future<Map<String, Object?>?> webPushSubscription(
  String applicationServerKey,
) async => null;
Future<void> storeWebPushContentKey({
  required String agentId,
  required String deviceId,
  required String keyId,
  required String key,
}) => Future.error(UnsupportedError('Web Push is unavailable'));
Stream<PushEvent> get webPushEvents => const Stream<PushEvent>.empty();
