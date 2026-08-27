import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:http/http.dart' as http;

import 'push_identity.dart';

class PushRelayException implements Exception {
  const PushRelayException(this.message);
  final String message;
  @override
  String toString() => message;
}

class PushRelayClient {
  PushRelayClient({http.Client? httpClient})
    : _http = httpClient ?? http.Client();

  final http.Client _http;
  PushIdentity? identity;
  bool registered = false;

  Future<PushIdentity> initialize() async {
    final current = await getPushIdentity();
    if (current == null) throw StateError('push identity is unavailable');
    identity = current;
    await _registerIdentity(current);
    await _registerCurrentEndpoint(current);
    registered = true;
    return current;
  }

  Future<void> refreshIdentityAndToken() async {
    final current = await getPushIdentity();
    if (current == null) return;
    identity = current;
    if (!registered) await _registerIdentity(current);
    await _registerCurrentEndpoint(current);
    registered = true;
  }

  Future<void> _registerCurrentEndpoint(PushIdentity current) async {
    if (!kIsWeb && defaultTargetPlatform == TargetPlatform.android) {
      if (current.apnsToken.isNotEmpty) {
        await registerPushEndpoint(current.apnsToken, 'production');
      }
    } else if (current.apnsToken.isNotEmpty) {
      await registerPushEndpoint(current.apnsToken, current.apnsEnvironment);
    }
  }

  Future<void> _registerIdentity(PushIdentity value) async {
    final challenge = await _postPublic('/v1/registration-challenges', {
      'principalType': 'device',
      'principalId': value.deviceId,
      'platform': pushPlatform,
      'publicKey': value.publicKey,
      'displayName': switch (pushPlatform) {
        'android' => 'Forge Android',
        'macos' => 'Forge macOS',
        _ => 'Forge iOS',
      },
    });
    final challengeId = '${challenge['challengeId'] ?? ''}';
    final signingPayload = '${challenge['signingPayload'] ?? ''}';
    final signature = await signPushPayload(signingPayload);
    await _postPublic('/v1/registrations', {
      'challengeId': challengeId,
      'signature': signature,
    });
  }

  Future<void> registerPushEndpoint(String token, String environment) async {
    final value = identity;
    if (value == null || token.isEmpty) return;
    final android = !kIsWeb && defaultTargetPlatform == TargetPlatform.android;
    await signedRequest(
      principalType: 'device',
      principalId: value.deviceId,
      method: 'PUT',
      path: '/v1/devices/${value.deviceId}/push-endpoint',
      body: {
        'provider': android ? 'fcm' : 'apns',
        'environment': android ? 'production' : environment,
        'topic': 'com.tingouw.forge',
        'token': token,
      },
    );
  }

  Future<List<Map<String, dynamic>>> listAuthorizations() async {
    final value = identity;
    if (value == null) throw StateError('push identity is unavailable');
    final response = await signedRequest(
      principalType: 'device',
      principalId: value.deviceId,
      method: 'GET',
      path: '/v1/devices/${value.deviceId}/authorizations',
    );
    return (response['authorizations'] as List? ?? const [])
        .whereType<Map>()
        .map((item) => item.map((key, value) => MapEntry('$key', value)))
        .toList();
  }

  Future<void> revokeAuthorization(String agentId) async {
    final value = identity;
    if (value == null) throw StateError('push identity is unavailable');
    if (agentId.isEmpty) throw ArgumentError.value(agentId, 'agentId');
    await signedRequest(
      principalType: 'device',
      principalId: value.deviceId,
      method: 'DELETE',
      path: '/v1/devices/${value.deviceId}/authorizations/$agentId',
    );
  }

  Future<Map<String, dynamic>> getPairing(String pairingId) async {
    final value = identity;
    if (value == null) throw StateError('push identity is unavailable');
    return signedRequest(
      principalType: 'device',
      principalId: value.deviceId,
      method: 'GET',
      path: '/v1/devices/${value.deviceId}/pairings/$pairingId',
    );
  }

  Future<void> approvePairing({
    required String pairingId,
    required String signingPayload,
    required String verificationCode,
  }) async {
    final value = identity;
    if (value == null) throw StateError('push identity is unavailable');
    final signature = await signPushPayload(signingPayload);
    await signedRequest(
      principalType: 'device',
      principalId: value.deviceId,
      method: 'POST',
      path: '/v1/devices/${value.deviceId}/pairings/$pairingId/approve',
      body: {'signature': signature, 'verificationCode': verificationCode},
    );
  }

  Future<Map<String, dynamic>> signedRequest({
    required String principalType,
    required String principalId,
    required String method,
    required String path,
    Map<String, Object?>? body,
  }) async {
    final bodyBytes = body == null
        ? Uint8List(0)
        : Uint8List.fromList(utf8.encode(jsonEncode(body)));
    final timestamp = DateTime.now().millisecondsSinceEpoch ~/ 1000;
    final nonce = await deviceIdentityRandomNonce();
    final bodyHash = await deviceIdentitySHA256(bodyBytes);
    final signingPayload = [
      'FORGE-REQUEST-V1',
      method,
      path,
      '$timestamp',
      nonce,
      bodyHash,
    ].join('\n');
    final signature = await signPushPayload(signingPayload);
    final response = await _http.send(
      http.Request(method, Uri.parse('$pushRelayBaseURL$path'))
        ..headers.addAll({
          if (body != null) 'content-type': 'application/json',
          'X-Forge-Principal-Type': principalType,
          'X-Forge-Principal-ID': principalId,
          'X-Forge-Timestamp': '$timestamp',
          'X-Forge-Nonce': nonce,
          'X-Forge-Signature': signature,
        })
        ..bodyBytes = bodyBytes,
    );
    final responseBody = await response.stream.bytesToString();
    return _decode(response.statusCode, responseBody);
  }

  Future<Map<String, dynamic>> _postPublic(
    String path,
    Map<String, Object?> body,
  ) async {
    final response = await _http.post(
      Uri.parse('$pushRelayBaseURL$path'),
      headers: const {'content-type': 'application/json'},
      body: jsonEncode(body),
    );
    return _decode(response.statusCode, response.body);
  }

  Map<String, dynamic> _decode(int statusCode, String body) {
    final decoded = body.isEmpty ? <String, dynamic>{} : jsonDecode(body);
    final value = decoded is Map<String, dynamic>
        ? decoded
        : <String, dynamic>{};
    if (statusCode < 200 || statusCode >= 300) {
      final error = value['error'];
      final message = error is Map
          ? '${error['message'] ?? error['code'] ?? 'Push relay error'}'
          : 'Push relay returned HTTP $statusCode';
      throw PushRelayException(message);
    }
    return value;
  }

  Future<void> dispose() async {
    _http.close();
  }
}
