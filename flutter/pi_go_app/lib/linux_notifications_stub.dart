bool get linuxPortalNotificationsSupported => false;

Future<void> showLinuxPortalNotification({
  required String id,
  required String title,
  required String body,
  required bool urgent,
}) async {}

Future<void> removeLinuxPortalNotification(String id) async {}

Future<void> closeLinuxPortalNotifications() async {}
