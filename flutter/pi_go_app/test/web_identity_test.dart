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

  test('random values differ, compiled to JavaScript or WebAssembly', () async {
    final first = await softwareIdentityRandomNonce();
    final second = await softwareIdentityRandomNonce();
    expect(first, hasLength(43));
    expect(first, isNot('A' * 43));
    expect(first, isNot(second));
    final identity = await getSoftwareDeviceIdentity();
    expect(identity?.deviceId, hasLength(24));
    expect(identity?.deviceId, isNot('A' * 24));
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
