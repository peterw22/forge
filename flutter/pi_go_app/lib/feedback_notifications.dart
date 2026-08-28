import 'linux_notifications.dart';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

const _sessionNotificationChannel = MethodChannel('com.tingouw.forge/feedback');

Future<void> initializeSessionNotifications() async {
  if (linuxPortalNotificationsSupported) return;
  if (kIsWeb ||
      (defaultTargetPlatform != TargetPlatform.android &&
          defaultTargetPlatform != TargetPlatform.iOS &&
          defaultTargetPlatform != TargetPlatform.macOS)) {
    return;
  }
  try {
    await _sessionNotificationChannel.invokeMethod<void>('initialize');
  } on PlatformException {
    // The app remains usable when notification permission is unavailable.
  } on MissingPluginException {
    // Widget tests and unsupported embedders do not install this channel.
  }
}

Future<void> showSessionNotification({
  required String connection,
  required String session,
  required bool needsFeedback,
  required bool suppressWhenForeground,
}) async {
  if (linuxPortalNotificationsSupported) {
    // Linux notifications are intentionally available whenever the Flatpak is
    // running. Unlike the native mobile bridges, this Dart layer has no
    // reliable compositor-level window-focus signal, so treating the selected
    // session as foreground would suppress every notification even while the
    // window is minimized or covered.
    final id = _linuxNotificationID(connection, session);
    try {
      if (!needsFeedback) {
        await removeLinuxPortalNotification(id);
      }
      await showLinuxPortalNotification(
        id: id,
        title: needsFeedback
            ? 'Forge needs your feedback'
            : 'Forge session completed',
        body: needsFeedback
            ? 'A connected agent is waiting for approval or input.'
            : 'A connected agent completed its turn.',
        urgent: needsFeedback,
      );
    } catch (error) {
      debugPrint('Forge Linux notification portal failed: $error');
    }
    return;
  }
  if (kIsWeb) return;
  if (defaultTargetPlatform != TargetPlatform.android &&
      defaultTargetPlatform != TargetPlatform.iOS &&
      defaultTargetPlatform != TargetPlatform.macOS) {
    return;
  }
  try {
    await _sessionNotificationChannel.invokeMethod<void>('show', {
      'connection': connection,
      'session': session,
      'needsFeedback': needsFeedback,
      'suppressWhenForeground': suppressWhenForeground,
    });
  } on PlatformException {
    // Session status remains visible in Forge if native notifications fail.
  } on MissingPluginException {
    // Widget tests and unsupported embedders do not install this channel.
  }
}

String _linuxNotificationID(String connection, String session) {
  // D-Bus IDs are not user-visible, but avoid raw endpoint/session text anyway.
  var hash = 0xcbf29ce484222325;
  for (final unit in '$connection\n$session'.codeUnits) {
    hash ^= unit;
    hash = (hash * 0x100000001b3) & 0x7fffffffffffffff;
  }
  return 'forge-$hash';
}

Future<void> closeSessionNotifications() => closeLinuxPortalNotifications();
