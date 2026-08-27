import 'dart:convert';

import 'device_identity.dart';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

const pushRelayBaseURL = 'https://forge-push.tingouw.com';
const _pushIdentityChannel = MethodChannel('com.tingouw.forge/push_identity');
const _pushIdentityEvents = EventChannel(
  'com.tingouw.forge/push_identity_events',
);

bool get deviceIdentitySupported =>
    softwareDeviceIdentitySupported ||
    (!kIsWeb &&
        (defaultTargetPlatform == TargetPlatform.iOS ||
            defaultTargetPlatform == TargetPlatform.android ||
            defaultTargetPlatform == TargetPlatform.macOS));

bool get deviceIdentityExportSupported =>
    deviceIdentitySupported && !softwareDeviceIdentitySupported;

bool get pushSupported =>
    !kIsWeb &&
    (defaultTargetPlatform == TargetPlatform.iOS ||
        defaultTargetPlatform == TargetPlatform.android ||
        defaultTargetPlatform == TargetPlatform.macOS);

String get pushPlatform => switch (defaultTargetPlatform) {
  TargetPlatform.android => 'android',
  TargetPlatform.macOS => 'macos',
  _ => 'ios',
};

class DeviceIdentity {
  const DeviceIdentity({
    required this.deviceId,
    required this.publicKey,
    required this.fingerprint,
  });
  final String deviceId;
  final Map<String, dynamic> publicKey;
  final String fingerprint;

  factory DeviceIdentity.fromMap(Map<dynamic, dynamic> value) {
    final publicKeyValue = value['publicKey'];
    return DeviceIdentity(
      deviceId: '${value['deviceId'] ?? ''}',
      publicKey: publicKeyValue is Map
          ? publicKeyValue.map((key, value) => MapEntry('$key', value))
          : const {},
      fingerprint: '${value['fingerprint'] ?? ''}',
    );
  }
}

class PushIdentity extends DeviceIdentity {
  const PushIdentity({
    required super.deviceId,
    required super.publicKey,
    required super.fingerprint,
    required this.apnsToken,
    required this.apnsEnvironment,
  });

  final String apnsToken;
  final String apnsEnvironment;

  factory PushIdentity.fromMap(Map<dynamic, dynamic> value) {
    final identity = DeviceIdentity.fromMap(value);
    return PushIdentity(
      deviceId: identity.deviceId,
      publicKey: identity.publicKey,
      fingerprint: identity.fingerprint,
      apnsToken: '${value['apnsToken'] ?? ''}',
      apnsEnvironment: '${value['apnsEnvironment'] ?? 'development'}',
    );
  }
}

class PushEvent {
  const PushEvent({required this.type, required this.values});
  final String type;
  final Map<String, dynamic> values;

  factory PushEvent.fromDynamic(dynamic value) {
    final map = value is Map
        ? value.map((key, value) => MapEntry('$key', value))
        : <String, dynamic>{};
    return PushEvent(type: '${map['type'] ?? ''}', values: map);
  }
}

Future<DeviceIdentity?> getDeviceIdentity() async {
  if (!deviceIdentitySupported) return null;
  if (softwareDeviceIdentitySupported) return getSoftwareDeviceIdentity();
  final value = await _pushIdentityChannel.invokeMethod<Map<dynamic, dynamic>>(
    'getIdentity',
  );
  return value == null ? null : DeviceIdentity.fromMap(value);
}

Future<PushIdentity?> getPushIdentity() async {
  if (!pushSupported) return null;
  final value = await _pushIdentityChannel.invokeMethod<Map<dynamic, dynamic>>(
    'getIdentity',
  );
  return value == null ? null : PushIdentity.fromMap(value);
}

Future<String> deviceIdentityRandomNonce() async {
  if (softwareDeviceIdentitySupported) return softwareIdentityRandomNonce();
  final nonce = await _pushIdentityChannel.invokeMethod<String>('randomNonce');
  if (nonce == null || nonce.isEmpty) {
    throw StateError('Device identity did not return random bytes');
  }
  return nonce;
}

Future<String> signDeviceIdentityPayload(String payload) async {
  if (softwareDeviceIdentitySupported) {
    return signSoftwareDeviceIdentityPayload(payload);
  }
  final signature = await _pushIdentityChannel.invokeMethod<String>('sign', {
    'payload': payload,
  });
  if (signature == null || signature.isEmpty) {
    throw StateError('Device identity did not return a signature');
  }
  return signature;
}

Future<bool> verifyDeviceIdentitySignature({
  required Map<String, dynamic> publicKey,
  required String payload,
  required String signature,
}) async {
  if (softwareDeviceIdentitySupported) {
    return verifySoftwareDeviceIdentitySignature(
      publicKey: publicKey,
      payload: payload,
      signature: signature,
    );
  }
  final verified = await _pushIdentityChannel.invokeMethod<bool>('verify', {
    'publicKey': publicKey,
    'payload': payload,
    'signature': signature,
  });
  return verified == true;
}

Future<String> deviceIdentitySHA256(Uint8List bytes) async {
  if (softwareDeviceIdentitySupported) {
    return softwareDeviceIdentitySHA256(bytes);
  }
  final hash = await _pushIdentityChannel.invokeMethod<String>('sha256', {
    'bytes': bytes,
  });
  if (hash == null || hash.isEmpty) {
    throw StateError('Device identity did not return a SHA-256 hash');
  }
  return hash;
}

Future<bool> verifyPushSignature({
  required Map<String, dynamic> publicKey,
  required String payload,
  required String signature,
}) => verifyDeviceIdentitySignature(
  publicKey: publicKey,
  payload: payload,
  signature: signature,
);

Future<String> signPushPayload(String payload) =>
    signDeviceIdentityPayload(payload);

Future<void> storePushContentKey({
  required String agentId,
  required String deviceId,
  required String keyId,
  required String key,
}) async {
  if (!pushSupported) return;
  await _pushIdentityChannel.invokeMethod<void>('storePushKey', {
    'agentId': agentId,
    'deviceId': deviceId,
    'keyId': keyId,
    'key': key,
  });
}

Future<void> setVisiblePushSession({
  required String agentId,
  required String sessionId,
}) async {
  if (!pushSupported) return;
  await _pushIdentityChannel.invokeMethod<void>('setVisibleSession', {
    'agentId': agentId,
    'sessionId': sessionId,
  });
}

Stream<PushEvent> get nativePushEvents =>
    !kIsWeb &&
        (defaultTargetPlatform == TargetPlatform.iOS ||
            defaultTargetPlatform == TargetPlatform.android ||
            defaultTargetPlatform == TargetPlatform.macOS)
    ? _pushIdentityEvents.receiveBroadcastStream().map(PushEvent.fromDynamic)
    : const Stream<PushEvent>.empty();

String canonicalJSON(Map<String, Object?> value) => jsonEncode(value);
