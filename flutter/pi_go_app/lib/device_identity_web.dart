@JS()
library;

import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';
import 'dart:typed_data';

import 'package:web/web.dart' as web;

import 'push_identity.dart';

bool get softwareDeviceIdentitySupported => true;

const _databaseName = 'forge-device-identity';
const _storeName = 'identity';
const _privateKeyName = 'privateKey';
const _deviceIDName = 'deviceId';
// The identifier that every browser was given while random bytes stayed
// empty under WebAssembly. An identity that has it is replaced.
final _emptyDeviceID = 'A' * 24;

_WebIdentityState? _cached;

@JS()
extension type _CryptoKeyPair._(JSObject _) implements JSObject {
  external web.CryptoKey get privateKey;
  external web.CryptoKey get publicKey;
}

Future<DeviceIdentity?> getSoftwareDeviceIdentity() async =>
    (await _loadOrCreate()).identity;

Future<String> softwareIdentityRandomNonce() async {
  // The browser fills an array of its own. Compiled to WebAssembly, a list of
  // Dart is copied on its way to the browser, and would stay empty.
  final bytes = Uint8List(32).toJS;
  web.window.crypto.getRandomValues(bytes);
  return _base64Url(bytes.toDart);
}

Future<String> signSoftwareDeviceIdentityPayload(String payload) async {
  final state = await _loadOrCreate();
  final signature = await web.window.crypto.subtle
      .sign(
        {'name': 'ECDSA', 'hash': 'SHA-256'}.jsify()!,
        state.privateKey,
        Uint8List.fromList(utf8.encode(payload)).toJS,
      )
      .toDart;
  return _base64Url(_arrayBufferBytes(signature! as JSArrayBuffer));
}

Future<bool> verifySoftwareDeviceIdentitySignature({
  required Map<String, dynamic> publicKey,
  required String payload,
  required String signature,
}) async {
  try {
    final imported = await web.window.crypto.subtle
        .importKey(
          'jwk',
          {
                'kty': 'EC',
                'crv': 'P-256',
                'x': '${publicKey['x'] ?? ''}',
                'y': '${publicKey['y'] ?? ''}',
                'ext': true,
                'key_ops': ['verify'],
              }.jsify()
              as JSObject,
          {'name': 'ECDSA', 'namedCurve': 'P-256'}.jsify()!,
          false,
          ['verify'].map((value) => value.toJS).toList().toJS,
        )
        .toDart;
    final verified = await web.window.crypto.subtle
        .verify(
          {'name': 'ECDSA', 'hash': 'SHA-256'}.jsify()!,
          imported,
          _decodeBase64Url(signature).toJS,
          Uint8List.fromList(utf8.encode(payload)).toJS,
        )
        .toDart;
    return (verified! as JSBoolean).toDart;
  } catch (_) {
    return false;
  }
}

Future<String> softwareDeviceIdentitySHA256(Uint8List bytes) async {
  final digest = await web.window.crypto.subtle
      .digest('SHA-256'.toJS, bytes.toJS)
      .toDart;
  return _base64Url(_arrayBufferBytes(digest! as JSArrayBuffer));
}

class _WebIdentityState {
  const _WebIdentityState(this.identity, this.privateKey);
  final DeviceIdentity identity;
  final web.CryptoKey privateKey;
}

Future<_WebIdentityState> _loadOrCreate() async {
  if (_cached case final state?) return state;
  if (!web.window.isSecureContext) {
    throw StateError('Forge Web requires an HTTPS secure context');
  }
  final database = await _openDatabase();
  try {
    var privateKeyValue = await _get(database, _privateKeyName);
    var publicKeyValue = await _get(database, 'publicKey');
    var deviceID = (await _get(database, _deviceIDName) as JSString?)?.toDart;
    if (privateKeyValue == null ||
        publicKeyValue == null ||
        deviceID == null ||
        deviceID.isEmpty ||
        deviceID == _emptyDeviceID) {
      final generated = await web.window.crypto.subtle
          .generateKey(
            {'name': 'ECDSA', 'namedCurve': 'P-256'}.jsify()!,
            false,
            ['sign', 'verify'].map((value) => value.toJS).toList().toJS,
          )
          .toDart;
      final pair = _CryptoKeyPair._(generated! as JSObject);
      privateKeyValue = pair.privateKey;
      publicKeyValue = pair.publicKey;
      deviceID = await softwareIdentityRandomNonce();
      // Keep the identifier compact while retaining 144 random bits.
      deviceID = _base64Url(_decodeBase64Url(deviceID).sublist(0, 18));
      await _put(database, _privateKeyName, privateKeyValue);
      await _put(database, 'publicKey', publicKeyValue);
      await _put(database, _deviceIDName, deviceID.toJS);
    }
    final privateKey = privateKeyValue as web.CryptoKey;
    final publicKey = publicKeyValue as web.CryptoKey;
    final exported = await web.window.crypto.subtle
        .exportKey('jwk', publicKey)
        .toDart;
    final jwk = (exported! as JSObject).dartify() as Map;
    final x = '${jwk['x'] ?? ''}';
    final y = '${jwk['y'] ?? ''}';
    final fingerprint = await softwareDeviceIdentitySHA256(
      Uint8List.fromList(utf8.encode('P-256.$x.$y')),
    );
    return _cached = _WebIdentityState(
      DeviceIdentity(
        deviceId: deviceID,
        publicKey: {'kty': 'EC', 'crv': 'P-256', 'x': x, 'y': y},
        fingerprint: fingerprint,
      ),
      privateKey,
    );
  } finally {
    database.close();
  }
}

Future<web.IDBDatabase> _openDatabase() {
  final completer = Completer<web.IDBDatabase>();
  final request = web.window.indexedDB.open(_databaseName, 1);
  request.onupgradeneeded = ((web.Event _) {
    final database = request.result! as web.IDBDatabase;
    if (!database.objectStoreNames.contains(_storeName)) {
      database.createObjectStore(_storeName);
    }
  }).toJS;
  request.onsuccess = ((web.Event _) {
    completer.complete(request.result! as web.IDBDatabase);
  }).toJS;
  request.onerror = ((web.Event _) {
    completer.completeError(
      StateError(request.error?.message ?? 'Could not open IndexedDB'),
    );
  }).toJS;
  return completer.future;
}

Future<JSAny?> _get(web.IDBDatabase database, String key) {
  final transaction = database.transaction(_storeName.toJS, 'readonly');
  final request = transaction.objectStore(_storeName).get(key.toJS);
  return _request(request);
}

Future<void> _put(web.IDBDatabase database, String key, JSAny value) async {
  final transaction = database.transaction(_storeName.toJS, 'readwrite');
  final request = transaction.objectStore(_storeName).put(value, key.toJS);
  await _request(request);
}

Future<JSAny?> _request(web.IDBRequest request) {
  final completer = Completer<JSAny?>();
  request.onsuccess = ((web.Event _) {
    completer.complete(request.result);
  }).toJS;
  request.onerror = ((web.Event _) {
    completer.completeError(
      StateError(request.error?.message ?? 'IndexedDB request failed'),
    );
  }).toJS;
  return completer.future;
}

Uint8List _arrayBufferBytes(JSArrayBuffer buffer) =>
    Uint8List.view(buffer.toDart);
String _base64Url(List<int> bytes) =>
    base64UrlEncode(bytes).replaceAll('=', '');
Uint8List _decodeBase64Url(String value) => Uint8List.fromList(
  base64Url.decode(value.padRight((value.length + 3) ~/ 4 * 4, '=')),
);
