import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:cryptography/cryptography.dart';
import 'package:flutter/foundation.dart';
import 'package:pointycastle/export.dart' as pc;

class ClientSecureSession {
  ClientSecureSession._({
    required this.connectionId,
    required this._keyPair,
    required this._linuxPrivateKey,
    required this.publicJwk,
  });

  final String connectionId;
  final EcKeyPair? _keyPair;
  final pc.ECPrivateKey? _linuxPrivateKey;
  final Map<String, String> publicJwk;
  final _aes = AesGcm.with256bits();
  SecretKey? _sendKey;
  SecretKey? _receiveKey;
  Uint8List? _sendPrefix;
  Uint8List? _receivePrefix;
  int _sendSequence = 0;
  int _receiveSequence = 0;

  static Future<ClientSecureSession> create() async {
    if (!kIsWeb && Platform.isLinux) {
      final domain = pc.ECDomainParameters('secp256r1');
      final random = Random.secure();
      BigInt d;
      do {
        d = BigInt.zero;
        for (var index = 0; index < 32; index++) {
          d = (d << 8) | BigInt.from(random.nextInt(256));
        }
      } while (d <= BigInt.zero || d >= domain.n);
      final point = (domain.G * d)!;
      final x = _fixedBigInt(point.x!.toBigInteger()!, 32);
      final y = _fixedBigInt(point.y!.toBigInteger()!, 32);
      return ClientSecureSession._(
        connectionId: '',
        keyPair: null,
        linuxPrivateKey: pc.ECPrivateKey(d, domain),
        publicJwk: {
          'kty': 'EC',
          'crv': 'P-256',
          'x': _base64Url(x),
          'y': _base64Url(y),
        },
      );
    }
    final algorithm = Ecdh.p256(length: 32);
    final keyPair = await algorithm.newKeyPair();
    final publicKey = await keyPair.extractPublicKey();
    return ClientSecureSession._(
      connectionId: '',
      keyPair: keyPair,
      linuxPrivateKey: null,
      publicJwk: {
        'kty': 'EC',
        'crv': 'P-256',
        'x': _base64Url(_canonicalCoordinate(publicKey.x)),
        'y': _base64Url(_canonicalCoordinate(publicKey.y)),
      },
    );
  }

  Future<ClientSecureSession> derive({
    required String connectionId,
    required Map<String, dynamic> serverPublicJwk,
    required String clientNonce,
    required String serverNonce,
    required String deviceFingerprint,
    required String serverFingerprint,
  }) async {
    SecretKey shared;
    if (_linuxPrivateKey != null) {
      final domain = pc.ECDomainParameters('secp256r1');
      final point = domain.curve.createPoint(
        _bytesToBigInt(_decodeBase64Url('${serverPublicJwk['x'] ?? ''}')),
        _bytesToBigInt(_decodeBase64Url('${serverPublicJwk['y'] ?? ''}')),
      );
      final agreement = pc.ECDHBasicAgreement()..init(_linuxPrivateKey);
      shared = SecretKey(
        _fixedBigInt(
          agreement.calculateAgreement(pc.ECPublicKey(point, domain)),
          32,
        ),
      );
    } else {
      final algorithm = Ecdh.p256(length: 32);
      List<int> platformCoordinate(String value) {
        final bytes = _decodeBase64Url(value);
        // cryptography_flutter's Android bridge constructs java.math.BigInteger
        // without an explicit positive sign, so high-bit coordinates need a
        // leading zero there. Apple CryptoKit expects canonical 32-byte P-256
        // coordinates and rejects that Android-only sign byte.
        if (!kIsWeb &&
            defaultTargetPlatform == TargetPlatform.android &&
            bytes.isNotEmpty &&
            (bytes.first & 0x80) != 0) {
          return <int>[0, ...bytes];
        }
        return bytes;
      }

      final serverPublic = EcPublicKey(
        x: platformCoordinate('${serverPublicJwk['x'] ?? ''}'),
        y: platformCoordinate('${serverPublicJwk['y'] ?? ''}'),
        type: KeyPairType.p256,
      );
      shared = await algorithm.sharedSecretKey(
        keyPair: _keyPair!,
        remotePublicKey: serverPublic,
      );
    }
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
      final result = await Hkdf(
        hmac: Hmac.sha256(),
        outputLength: length,
      ).deriveKey(secretKey: shared, nonce: salt, info: utf8.encode(label));
      return Uint8List.fromList(await result.extractBytes());
    }

    final session = ClientSecureSession._(
      connectionId: connectionId,
      keyPair: null,
      linuxPrivateKey: null,
      publicJwk: publicJwk,
    );
    session._sendKey = SecretKey(
      await expand('forge-v1 client-to-server key', 32),
    );
    session._receiveKey = SecretKey(
      await expand('forge-v1 server-to-client key', 32),
    );
    session._sendPrefix = await expand('forge-v1 client nonce', 4);
    session._receivePrefix = await expand('forge-v1 server nonce', 4);
    return session;
  }

  Future<Map<String, Object?>> encrypt(Map<String, Object?> value) async {
    final sequence = _sendSequence++;
    final nonce = _nonce(_sendPrefix!, sequence);
    final aad = utf8.encode(
      'FORGE-ENCRYPTED-V1\n$connectionId\nclient-to-server\n$sequence',
    );
    final box = await _aes.encrypt(
      utf8.encode(jsonEncode(value)),
      secretKey: _sendKey!,
      nonce: nonce,
      aad: aad,
    );
    return {
      'type': 'encrypted',
      'version': 1,
      'sequence': sequence,
      'ciphertext': _base64Url([...box.cipherText, ...box.mac.bytes]),
    };
  }

  Future<Map<String, dynamic>> decrypt(Map<String, dynamic> envelope) async {
    final sequence = envelope['sequence'];
    if (envelope['type'] != 'encrypted' ||
        envelope['version'] != 1 ||
        sequence is! int ||
        sequence != _receiveSequence) {
      throw StateError(
        'Invalid encrypted message sequence: got $sequence, expected $_receiveSequence',
      );
    }
    final combined = _decodeBase64Url('${envelope['ciphertext'] ?? ''}');
    if (combined.length < 16) {
      throw StateError('Encrypted message is too short');
    }
    final nonce = _nonce(_receivePrefix!, sequence);
    final aad = utf8.encode(
      'FORGE-ENCRYPTED-V1\n$connectionId\nserver-to-client\n$sequence',
    );
    final clear = await _aes.decrypt(
      SecretBox(
        combined.sublist(0, combined.length - 16),
        nonce: nonce,
        mac: Mac(combined.sublist(combined.length - 16)),
      ),
      secretKey: _receiveKey!,
      aad: aad,
    );
    _receiveSequence++;
    final decoded = jsonDecode(utf8.decode(clear));
    if (decoded is! Map<String, dynamic>) {
      throw StateError('Encrypted payload is not an object');
    }
    return decoded;
  }

  static Uint8List _nonce(Uint8List prefix, int sequence) {
    if (sequence < 0 || sequence > 0x1fffffffffffff) {
      throw StateError(
        'Encrypted message sequence exceeds Web-safe integer range',
      );
    }
    // dart2js does not implement ByteData.setUint64. Split the sequence into
    // two 32-bit words so this nonce encoding is identical on Web and native.
    final value = ByteData(8)
      ..setUint32(0, sequence ~/ 0x100000000, Endian.big)
      ..setUint32(4, sequence % 0x100000000, Endian.big);
    return Uint8List.fromList([...prefix, ...value.buffer.asUint8List()]);
  }

  static List<int> _canonicalCoordinate(List<int> value) {
    final bytes = value.skipWhile((byte) => byte == 0).toList();
    if (bytes.length > 32) {
      throw StateError('P-256 coordinate exceeds 32 bytes');
    }
    return <int>[...List<int>.filled(32 - bytes.length, 0), ...bytes];
  }

  static List<int> _fixedBigInt(BigInt value, int length) {
    final result = List<int>.filled(length, 0);
    var remaining = value;
    for (var index = length - 1; index >= 0; index--) {
      result[index] = (remaining & BigInt.from(255)).toInt();
      remaining >>= 8;
    }
    if (remaining != BigInt.zero) {
      throw StateError('integer exceeds fixed width');
    }
    return result;
  }

  static BigInt _bytesToBigInt(List<int> bytes) {
    var result = BigInt.zero;
    for (final byte in bytes) {
      result = (result << 8) | BigInt.from(byte);
    }
    return result;
  }

  static String _base64Url(List<int> bytes) =>
      base64UrlEncode(bytes).replaceAll('=', '');

  static Uint8List _decodeBase64Url(String value) => Uint8List.fromList(
    base64Url.decode(value.padRight((value.length + 3) ~/ 4 * 4, '=')),
  );
}
