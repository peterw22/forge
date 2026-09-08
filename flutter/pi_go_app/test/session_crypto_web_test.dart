@TestOn('browser')
library;

import 'dart:convert';
import 'dart:typed_data';

import 'package:cryptography/cryptography.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/session_crypto.dart';
import 'package:pointycastle/export.dart' as pc;

void main() {
  test(
    'browser P-256 session decrypts Go-compatible key confirmation',
    () async {
      final client = await ClientSecureSession.create();
      final domain = pc.ECDomainParameters('secp256r1');
      final serverPrivate = BigInt.from(7);
      final serverPoint = (domain.G * serverPrivate)!;
      final serverJwk = {
        'kty': 'EC',
        'crv': 'P-256',
        'x': _base64Url(_fixed(serverPoint.x!.toBigInteger()!, 32)),
        'y': _base64Url(_fixed(serverPoint.y!.toBigInteger()!, 32)),
      };

      final clientPoint = domain.curve.createPoint(
        _bigInt(_decode('${client.publicJwk['x']}')),
        _bigInt(_decode('${client.publicJwk['y']}')),
      );
      final agreement = pc.ECDHBasicAgreement()
        ..init(pc.ECPrivateKey(serverPrivate, domain));
      final shared = _fixed(
        agreement.calculateAgreement(pc.ECPublicKey(clientPoint, domain)),
        32,
      );

      const connectionId = 'browser-session-test';
      const clientNonce = 'client-nonce';
      const serverNonce = 'server-nonce';
      const deviceFingerprint = 'device-fingerprint';
      const serverFingerprint = 'server-fingerprint';
      final derived = await client.derive(
        connectionId: connectionId,
        serverPublicJwk: serverJwk,
        clientNonce: clientNonce,
        serverNonce: serverNonce,
        deviceFingerprint: deviceFingerprint,
        serverFingerprint: serverFingerprint,
      );

      final saltText = [
        'FORGE-SESSION-SALT-V1',
        connectionId,
        clientNonce,
        serverNonce,
        deviceFingerprint,
        serverFingerprint,
      ].join('\n');
      final salt = (await Sha256().hash(utf8.encode(saltText))).bytes;
      Future<Uint8List> expand(String label, int length) async {
        final key = await Hkdf(hmac: Hmac.sha256(), outputLength: length)
            .deriveKey(
              secretKey: SecretKey(shared),
              nonce: salt,
              info: utf8.encode(label),
            );
        return Uint8List.fromList(await key.extractBytes());
      }

      final key = SecretKey(await expand('forge-v1 server-to-client key', 32));
      final prefix = await expand('forge-v1 server nonce', 4);
      final nonce = ByteData(8)
        ..setUint32(0, 0, Endian.big)
        ..setUint32(4, 0, Endian.big);
      final nonceBytes = Uint8List.fromList([
        ...prefix,
        ...nonce.buffer.asUint8List(),
      ]);
      final aad = utf8.encode(
        'FORGE-ENCRYPTED-V1\n$connectionId\nserver-to-client\n0',
      );
      final box = await AesGcm.with256bits().encrypt(
        utf8.encode(
          jsonEncode({
            'type': 'key_confirmation',
            'connectionId': connectionId,
            'success': true,
          }),
        ),
        secretKey: key,
        nonce: nonceBytes,
        aad: aad,
      );
      final clear = await derived.decrypt({
        'type': 'encrypted',
        'version': 1,
        'sequence': 0,
        'ciphertext': _base64Url([...box.cipherText, ...box.mac.bytes]),
      });
      expect(clear['type'], 'key_confirmation');
      expect(clear['connectionId'], connectionId);
    },
  );
}

String _base64Url(List<int> bytes) =>
    base64UrlEncode(bytes).replaceAll('=', '');
Uint8List _decode(String value) => Uint8List.fromList(
  base64Url.decode(value.padRight((value.length + 3) ~/ 4 * 4, '=')),
);
BigInt _bigInt(List<int> bytes) {
  var result = BigInt.zero;
  for (final byte in bytes) {
    result = (result << 8) | BigInt.from(byte);
  }
  return result;
}

List<int> _fixed(BigInt value, int length) {
  final result = List<int>.filled(length, 0);
  for (var index = length - 1; index >= 0; index--) {
    result[index] = (value & BigInt.from(255)).toInt();
    value >>= 8;
  }
  return result;
}
