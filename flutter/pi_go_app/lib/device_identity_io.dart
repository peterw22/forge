import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:cryptography/cryptography.dart';
import 'package:flutter/foundation.dart';
import 'package:pointycastle/export.dart';

import 'push_identity.dart';

bool get softwareDeviceIdentitySupported =>
    !kIsWeb && defaultTargetPlatform == TargetPlatform.linux;

final _domain = ECDomainParameters('secp256r1');
_DeviceIdentityState? _cached;

class _DeviceIdentityState {
  const _DeviceIdentityState(this.identity, this.privateKey);
  final DeviceIdentity identity;
  final ECPrivateKey privateKey;
}

Future<DeviceIdentity?> getSoftwareDeviceIdentity() async {
  if (!softwareDeviceIdentitySupported) return null;
  return (await _loadOrCreate()).identity;
}

Future<String> softwareIdentityRandomNonce() async =>
    _base64Url(_secureBytes(32));

Future<String> signSoftwareDeviceIdentityPayload(String payload) async {
  final state = await _loadOrCreate();
  final signer = Signer('SHA-256/DET-ECDSA') as ECDSASigner;
  signer.init(true, PrivateKeyParameter<ECPrivateKey>(state.privateKey));
  final signature =
      signer.generateSignature(Uint8List.fromList(utf8.encode(payload)))
          as ECSignature;
  return _base64Url([
    ..._fixedBigInt(signature.r, 32),
    ..._fixedBigInt(signature.s, 32),
  ]);
}

Future<bool> verifySoftwareDeviceIdentitySignature({
  required Map<String, dynamic> publicKey,
  required String payload,
  required String signature,
}) async {
  try {
    final x = _bytesToBigInt(_decodeBase64Url('${publicKey['x'] ?? ''}'));
    final y = _bytesToBigInt(_decodeBase64Url('${publicKey['y'] ?? ''}'));
    final point = _domain.curve.createPoint(x, y);
    final raw = _decodeBase64Url(signature);
    if (raw.length != 64) return false;
    final verifier = Signer('SHA-256/DET-ECDSA') as ECDSASigner;
    verifier.init(
      false,
      PublicKeyParameter<ECPublicKey>(ECPublicKey(point, _domain)),
    );
    return verifier.verifySignature(
      Uint8List.fromList(utf8.encode(payload)),
      ECSignature(
        _bytesToBigInt(raw.sublist(0, 32)),
        _bytesToBigInt(raw.sublist(32)),
      ),
    );
  } catch (_) {
    return false;
  }
}

Future<String> softwareDeviceIdentitySHA256(Uint8List bytes) async =>
    _base64Url((await Sha256().hash(bytes)).bytes);

Future<_DeviceIdentityState> _loadOrCreate() async {
  if (_cached case final state?) return state;
  final config = Platform.environment['XDG_CONFIG_HOME']?.trim();
  final home = Platform.environment['HOME']?.trim() ?? '';
  if (home.isEmpty && (config == null || config.isEmpty)) {
    throw StateError('HOME and XDG_CONFIG_HOME are unavailable');
  }
  final directory = Directory(
    '${config != null && config.isNotEmpty ? config : '$home/.config'}/forge',
  );
  final file = File('${directory.path}/device-identity.json');
  BigInt d;
  if (await file.exists()) {
    final info = await file.stat();
    if (info.type != FileSystemEntityType.file || info.mode & 0x3f != 0) {
      throw StateError('Linux device identity must be an owner-only file');
    }
    final value = jsonDecode(await file.readAsString());
    if (value is! Map || value['version'] != 1) {
      throw StateError('Linux device identity is invalid');
    }
    d = _bytesToBigInt(_decodeBase64Url('${value['privateD'] ?? ''}'));
  } else {
    await directory.create(recursive: true);
    await Process.run('chmod', ['700', directory.path]);
    do {
      d = _bytesToBigInt(_secureBytes(32));
    } while (d <= BigInt.zero || d >= _domain.n);
    final temporary = File('${file.path}.tmp');
    await temporary.writeAsString(
      '${jsonEncode({'version': 1, 'privateD': _base64Url(_fixedBigInt(d, 32))})}\n',
      flush: true,
    );
    await Process.run('chmod', ['600', temporary.path]);
    await temporary.rename(file.path);
  }
  if (d <= BigInt.zero || d >= _domain.n) {
    throw StateError('Linux device identity scalar is invalid');
  }
  final public = (_domain.G * d)!;
  final x = _fixedBigInt(public.x!.toBigInteger()!, 32);
  final y = _fixedBigInt(public.y!.toBigInteger()!, 32);
  final digest = await Sha256().hash(
    utf8.encode('P-256.${_base64Url(x)}.${_base64Url(y)}'),
  );
  final idDigest = await Sha256().hash([...x, ...y]);
  final identity = DeviceIdentity(
    deviceId: _base64Url(idDigest.bytes.sublist(0, 18)),
    publicKey: {
      'kty': 'EC',
      'crv': 'P-256',
      'x': _base64Url(x),
      'y': _base64Url(y),
    },
    fingerprint: _base64Url(digest.bytes),
  );
  return _cached = _DeviceIdentityState(identity, ECPrivateKey(d, _domain));
}

Uint8List _secureBytes(int length) {
  final random = Random.secure();
  return Uint8List.fromList(
    List<int>.generate(length, (_) => random.nextInt(256)),
  );
}

List<int> _fixedBigInt(BigInt value, int length) {
  final bytes = <int>[];
  var remaining = value;
  while (remaining > BigInt.zero) {
    bytes.add((remaining & BigInt.from(255)).toInt());
    remaining >>= 8;
  }
  final forward = bytes.reversed.toList();
  if (forward.length > length) throw StateError('integer is too large');
  return [...List<int>.filled(length - forward.length, 0), ...forward];
}

BigInt _bytesToBigInt(List<int> bytes) {
  var result = BigInt.zero;
  for (final byte in bytes) {
    result = (result << 8) | BigInt.from(byte);
  }
  return result;
}

String _base64Url(List<int> bytes) =>
    base64UrlEncode(bytes).replaceAll('=', '');
Uint8List _decodeBase64Url(String value) => Uint8List.fromList(
  base64Url.decode(value.padRight((value.length + 3) ~/ 4 * 4, '=')),
);
