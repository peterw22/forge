import 'dart:io';

import 'package:dbus/dbus.dart';

bool get linuxPortalNotificationsSupported => Platform.isLinux;

DBusClient? _client;
DBusRemoteObject? _portal;

DBusRemoteObject _notificationPortal() {
  final client = _client ??= DBusClient.session();
  return _portal ??= DBusRemoteObject(
    client,
    name: 'org.freedesktop.portal.Desktop',
    path: DBusObjectPath('/org/freedesktop/portal/desktop'),
  );
}

Future<void> showLinuxPortalNotification({
  required String id,
  required String title,
  required String body,
  required bool urgent,
}) async {
  if (!linuxPortalNotificationsSupported) return;
  final notification = DBusDict.stringVariant({
    'title': DBusString(title),
    'body': DBusString(body),
    'priority': DBusString(urgent ? 'high' : 'normal'),
  });
  await _notificationPortal().callMethod(
    'org.freedesktop.portal.Notification',
    'AddNotification',
    [DBusString(id), notification],
    replySignature: DBusSignature(''),
  );
}

Future<void> removeLinuxPortalNotification(String id) async {
  if (!linuxPortalNotificationsSupported || _portal == null) return;
  await _portal!.callMethod(
    'org.freedesktop.portal.Notification',
    'RemoveNotification',
    [DBusString(id)],
    replySignature: DBusSignature(''),
  );
}

Future<void> closeLinuxPortalNotifications() async {
  final client = _client;
  _portal = null;
  _client = null;
  await client?.close();
}
