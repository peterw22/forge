@TestOn('browser')
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/device_identity.dart';
import 'package:pi_go_app/transport_web.dart';

void main() {
  test('WebCrypto identity persists and signs P-256 payloads', () async {
    expect(softwareDeviceIdentitySupported, isTrue);
    final first = await getSoftwareDeviceIdentity();
    final second = await getSoftwareDeviceIdentity();
    expect(first, isNotNull);
    expect(second?.deviceId, first?.deviceId);
    expect(first?.publicKey['crv'], 'P-256');
    final signature = await signSoftwareDeviceIdentityPayload('forge-web-test');
    expect(
      await verifySoftwareDeviceIdentitySignature(
        publicKey: first!.publicKey,
        payload: 'forge-web-test',
        signature: signature,
      ),
      isTrue,
    );
    expect(
      await verifySoftwareDeviceIdentitySignature(
        publicKey: first.publicKey,
        payload: 'tampered',
        signature: signature,
      ),
      isFalse,
    );
  });

  test('web transport rejects non-WSS endpoints', () async {
    await expectLater(
      connectWebSocketTransport('ws://example.com/ws'),
      throwsA(isA<FormatException>()),
    );
    await expectLater(
      connectWebSocketTransport('https://example.com/ws'),
      throwsA(isA<FormatException>()),
    );
  });
}
