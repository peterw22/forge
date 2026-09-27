@JS()
library;

import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';
import 'dart:js_interop_unsafe';
import 'dart:typed_data';

import 'package:web/web.dart' as web;

import 'push_identity.dart';

// web/forge_push.js reads what is stored here; the names are the same there.
const _databaseName = 'forge-push-content';
const _keyStore = 'keys';
const _stateStore = 'state';
const _tapName = 'tap';
const _tapLifetime = Duration(minutes: 2);

/// Whether this browser can receive a push. Safari on an iPhone can only
/// after Forge was added to the Home Screen.
bool get webPushSupported =>
    web.window.isSecureContext &&
    web.window.navigator.has('serviceWorker') &&
    globalContext.has('PushManager') &&
    globalContext.has('Notification');

String get webPushPermission =>
    webPushSupported ? web.Notification.permission : 'denied';

/// Asks for the permission to notify. A browser shows the question only in
/// answer to a tap, so nothing may be awaited before this is called.
Future<bool> requestWebPushPermission() async {
  if (!webPushSupported) return false;
  final permission = await web.Notification.requestPermission().toDart;
  return permission.toDart == 'granted';
}

/// The subscription of this browser, as the relay stores it, or null while
/// notifications are not permitted.
Future<Map<String, Object?>?> webPushSubscription(
  String applicationServerKey,
) async {
  if (webPushPermission != 'granted') return null;
  final serverKey = _decodeBase64Url(applicationServerKey);
  final registration = await web.window.navigator.serviceWorker.ready.toDart;
  var subscription = await registration.pushManager.getSubscription().toDart;
  if (subscription != null) {
    final current = subscription.options.applicationServerKey;
    // A subscription is bound to the key of the relay it was made for.
    if (current == null ||
        _base64Url(_arrayBufferBytes(current)) != _base64Url(serverKey)) {
      await subscription.unsubscribe().toDart;
      subscription = null;
    }
  }
  subscription ??= await registration.pushManager
      .subscribe(
        web.PushSubscriptionOptionsInit(
          userVisibleOnly: true,
          applicationServerKey: serverKey.toJS,
        ),
      )
      .toDart;
  final p256dh = subscription.getKey('p256dh');
  final auth = subscription.getKey('auth');
  if (p256dh == null || auth == null) {
    throw StateError('The browser gave a subscription without keys');
  }
  return {
    'endpoint': subscription.endpoint,
    'keys': {
      'p256dh': _base64Url(_arrayBufferBytes(p256dh)),
      'auth': _base64Url(_arrayBufferBytes(auth)),
    },
  };
}

/// Keeps the key of an agent where the service worker finds it. The browser
/// holds it as a key that decrypts and cannot be exported.
Future<void> storeWebPushContentKey({
  required String agentId,
  required String deviceId,
  required String keyId,
  required String key,
}) async {
  final imported = await web.window.crypto.subtle
      .importKey(
        'raw',
        _decodeBase64Url(key).toJS,
        {'name': 'AES-GCM'}.jsify()!,
        false,
        ['decrypt'].map((value) => value.toJS).toList().toJS,
      )
      .toDart;
  final record = JSObject()
    ..setProperty('agentId'.toJS, agentId.toJS)
    ..setProperty('deviceId'.toJS, deviceId.toJS)
    ..setProperty('key'.toJS, imported);
  final database = await _openDatabase();
  try {
    await _request(
      database
          .transaction(_keyStore.toJS, 'readwrite')
          .objectStore(_keyStore)
          .put(record, keyId.toJS),
    );
  } finally {
    database.close();
  }
}

/// The taps on notifications. The service worker leaves a tap in the database,
/// which also reaches a window that the tap itself opened.
Stream<PushEvent> get webPushEvents {
  if (!webPushSupported) return const Stream<PushEvent>.empty();
  late final StreamController<PushEvent> controller;
  JSFunction? listener;

  Future<void> collect() async {
    final tap = await _takeTap();
    if (tap != null && !controller.isClosed) controller.add(tap);
  }

  controller = StreamController<PushEvent>(
    onListen: () {
      listener = ((web.MessageEvent event) {
        final data = event.data;
        if (data.isA<JSObject>() &&
            (data as JSObject).getProperty<JSAny?>('type'.toJS).dartify() ==
                'forge-notification-tap') {
          unawaited(collect().catchError((Object _) {}));
        }
      }).toJS;
      web.window.navigator.serviceWorker.addEventListener('message', listener);
      unawaited(collect().catchError((Object _) {}));
    },
    onCancel: () {
      web.window.navigator.serviceWorker.removeEventListener(
        'message',
        listener,
      );
    },
  );
  return controller.stream;
}

Future<PushEvent?> _takeTap() async {
  final database = await _openDatabase();
  try {
    final store = database
        .transaction(_stateStore.toJS, 'readwrite')
        .objectStore(_stateStore);
    final value = (await _request(store.get(_tapName.toJS))).dartify();
    if (value is! Map) return null;
    await _request(store.delete(_tapName.toJS));
    final at = value['at'];
    final age = DateTime.now().millisecondsSinceEpoch - (at is num ? at : 0);
    if (age < 0 || age > _tapLifetime.inMilliseconds) return null;
    return PushEvent(
      type: 'notificationTap',
      values: {
        'agentId': '${value['agentId'] ?? ''}',
        'sessionId': '${value['sessionId'] ?? ''}',
      },
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
    for (final store in [_keyStore, _stateStore]) {
      if (!database.objectStoreNames.contains(store)) {
        database.createObjectStore(store);
      }
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
