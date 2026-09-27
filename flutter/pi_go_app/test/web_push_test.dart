@TestOn('browser')
library;

import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';
import 'dart:js_interop_unsafe';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/push_identity.dart';
import 'package:pi_go_app/web_push.dart';
import 'package:web/web.dart' as web;

// What web/forge_push.js opens: the names and the stores are the same there.
Future<web.IDBDatabase> _open() {
  final completer = Completer<web.IDBDatabase>();
  final request = web.window.indexedDB.open('forge-push-content', 1);
  request.onupgradeneeded = ((web.Event _) {
    final database = request.result! as web.IDBDatabase;
    for (final store in ['keys', 'state']) {
      if (!database.objectStoreNames.contains(store)) {
        database.createObjectStore(store);
      }
    }
  }).toJS;
  request.onsuccess = ((web.Event _) {
    completer.complete(request.result! as web.IDBDatabase);
  }).toJS;
  request.onerror = ((web.Event _) {
    completer.completeError(StateError('Could not open IndexedDB'));
  }).toJS;
  return completer.future;
}

Future<JSAny?> _run(web.IDBRequest request) {
  final completer = Completer<JSAny?>();
  request.onsuccess = ((web.Event _) {
    completer.complete(request.result);
  }).toJS;
  request.onerror = ((web.Event _) {
    completer.completeError(StateError('IndexedDB request failed'));
  }).toJS;
  return completer.future;
}

Future<void> _leaveTap(Map<String, Object?> tap) async {
  final database = await _open();
  await _run(
    database
        .transaction('state'.toJS, 'readwrite')
        .objectStore('state')
        .put(tap.jsify(), 'tap'.toJS),
  );
  database.close();
}

Uint8List _decode(String value) => Uint8List.fromList(
  base64Url.decode(value.padRight((value.length + 3) ~/ 4 * 4, '=')),
);

void main() {
  test('this browser can receive a push', () {
    expect(webPushSupported, isTrue);
    expect(pushPlatform, 'web');
  });

  test('a stored key reads a notification that the agent encrypted', () async {
    await storeWebPushContentKey(
      agentId: 'agent-identifier-12345',
      deviceId: 'device-identifier-123',
      keyId: 'key-identifier-123456',
      key: base64UrlEncode([
        for (var index = 1; index <= 32; index++) index,
      ]).replaceAll('=', ''),
    );
    final database = await _open();
    final record =
        await _run(
              database
                  .transaction('keys'.toJS, 'readonly')
                  .objectStore('keys')
                  .get('key-identifier-123456'.toJS),
            )
            as JSObject;
    database.close();
    expect(
      record.getProperty<JSString>('agentId'.toJS).toDart,
      'agent-identifier-12345',
    );
    expect(
      record.getProperty<JSString>('deviceId'.toJS).toDart,
      'device-identifier-123',
    );
    // The bytes, and no key object, which Safari cannot read while locked.
    expect(record.has('key'), isFalse);
    final raw = record.getProperty<JSUint8Array>('raw'.toJS);
    expect(raw.toDart, [for (var index = 1; index <= 32; index++) index]);
    final key = await web.window.crypto.subtle
        .importKey(
          'raw',
          raw,
          {'name': 'AES-GCM'}.jsify()!,
          false,
          ['decrypt'].map((value) => value.toJS).toList().toJS,
        )
        .toDart;

    // The envelope of cmd/pi-go-agent/push_content_crypto_test.go.
    final associated = [
      'FORGE-PUSH-CONTENT-V1',
      'agent-identifier-12345',
      'device-identifier-123',
      'event-identifier-1234',
      'key-identifier-123456',
    ].join('\n');
    final plaintext = await web.window.crypto.subtle
        .decrypt(
          {
            'name': 'AES-GCM',
            'iv': _decode('QypfuyMwe3ZrsA93').toJS,
            'additionalData': Uint8List.fromList(utf8.encode(associated)).toJS,
          }.jsify()!,
          key,
          _decode(
            'O32vvImZoAcD6DzxUlC4cluUAJFVCsWGVPGNR0AZmzUNKmKXXY_DI3Mkq-oPfh0I43dsZzxgrr0HyQzgDrqv'
            'CJEmk1DMyxaiSYS8KT3keKtaS4IuRROHmoh4oRt0GX8epnqF0AfYHJCdltDjlI4sOhOuMzdSIS1tI5oRkoVZ'
            'mFfo5FPydjd32QssKhAgoTGvR-NIw4OEJ6NolVLKSj8Xio4ghB5J',
          ).toJS,
        )
        .toDart;
    final content =
        jsonDecode(
              utf8.decode(Uint8List.view((plaintext! as JSArrayBuffer).toDart)),
            )
            as Map<String, dynamic>;
    expect(content['sessionId'], 'private-session-name');
    expect(content['title'], 'Forge approval required');
  });

  test('a tap that the service worker left is delivered once', () async {
    await _leaveTap({
      'agentId': 'agent-identifier-12345',
      'sessionId': 'private-session-name',
      'at': DateTime.now().millisecondsSinceEpoch,
    });
    final first = await webPushEvents.first.timeout(const Duration(seconds: 5));
    expect(first.type, 'notificationTap');
    expect(first.values['agentId'], 'agent-identifier-12345');
    expect(first.values['sessionId'], 'private-session-name');
    await expectLater(
      webPushEvents.first.timeout(const Duration(seconds: 1)),
      throwsA(isA<TimeoutException>()),
    );
  });

  test('a tap from long ago is dropped', () async {
    await _leaveTap({
      'agentId': 'agent-identifier-12345',
      'sessionId': 'private-session-name',
      'at': DateTime.now()
          .subtract(const Duration(minutes: 10))
          .millisecondsSinceEpoch,
    });
    await expectLater(
      webPushEvents.first.timeout(const Duration(seconds: 1)),
      throwsA(isA<TimeoutException>()),
    );
  });
}
