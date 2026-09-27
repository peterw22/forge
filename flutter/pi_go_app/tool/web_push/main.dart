// Runs Forge in a browser against a demo agent and a demo relay. It attaches
// a picked image, and walks through notifications: turning them on, the
// pairing, a turn that ends, and the notification that the service worker
// shows for it.
//
// scripts/test-web-push.cjs starts everything and is told here what to do:
// whitelist this device, choose a file, answer the browser's question for
// permission, tap the notification. It receives each message at the address
// in DEMO_REPORT.
import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:pi_go_app/main.dart';
import 'package:pi_go_app/push_identity.dart';
import 'package:web/web.dart' as web;

const endpoint = String.fromEnvironment('DEMO_ENDPOINT');
const report = String.fromEnvironment('DEMO_REPORT');
const taskPrompt = String.fromEnvironment(
  'DEMO_TASK',
  defaultValue: 'Reply with the single word "finished" and use no tool.',
);

late final LiveWidgetController app;

Future<void> say(String message) async {
  debugPrint('FORGE-DEMO $message');
  try {
    await http.post(Uri.parse('$report/event'), body: message);
  } catch (_) {}
}

Future<void> wait([int milliseconds = 400]) =>
    app.pump(Duration(milliseconds: milliseconds));

bool present(Finder finder) => finder.evaluate().isNotEmpty;

Future<bool> until(
  bool Function() condition, {
  int seconds = 60,
  String what = '',
}) async {
  final deadline = DateTime.now().add(Duration(seconds: seconds));
  while (DateTime.now().isBefore(deadline)) {
    if (condition()) return true;
    await wait(300);
  }
  await say('failed timeout: $what');
  return false;
}

final sessions = find.byWidgetPredicate(
  (widget) =>
      (widget is Tooltip && widget.message == 'Switch session') ||
      (widget is Text && widget.data == 'Sessions  Ctrl-B S'),
);
final directory = find.byKey(const ValueKey('directory-path'));
final send = find.byKey(const ValueKey('composer-send'));
final settings = find.byTooltip('Model, thinking, and providers');
final turnOn = find.text('Turn on notifications');

Future<bool> chooseDirectory() async {
  if (!await until(() => present(directory), what: 'directories')) {
    return false;
  }
  await app.tap(find.text('Use this directory'));
  final chosen = await until(
    () => !present(directory),
    what: 'directory chosen',
  );
  await wait(1200);
  return chosen;
}

/// The notifications that the service worker is showing.
Future<List<Map<String, Object?>>> shown() async {
  final registration = await web.window.navigator.serviceWorker.ready.toDart;
  final notifications = await registration.getNotifications().toDart;
  return [
    for (final notification in notifications.toDart)
      {
        'title': notification.title,
        'body': notification.body,
        'tag': notification.tag,
        'data': notification.data.dartify(),
      },
  ];
}

Future<bool> connect() async {
  final address = find.byWidgetPredicate(
    (widget) =>
        widget is TextField &&
        widget.decoration?.labelText == 'Agent server address',
  );
  (address.evaluate().single.widget as TextField).controller!.text = endpoint;
  await wait();
  await app.tap(find.text('Connect'));
  final trust = find.text('Trust this server');
  await until(
    () => present(trust) || !present(find.text('Connect')),
    what: 'trust prompt',
    seconds: 40,
  );
  if (present(trust)) await app.tap(trust);
  if (!await until(
    () => !present(find.text('Connect')) && present(sessions),
    what: 'connection',
    seconds: 40,
  )) {
    return false;
  }
  await wait(1500);
  return chooseDirectory();
}

Future<void> walkThrough() async {
  await wait(2500);

  final identity = await getDeviceIdentity();
  if (identity == null) return say('failed no device identity');
  await say(
    'identity ${jsonEncode({'name': 'Demo browser', 'deviceId': identity.deviceId, 'publicKey': identity.publicKey, 'fingerprint': identity.fingerprint})}',
  );
  // The host adds the device to the agent's whitelist and starts the agent.
  await wait(9000);

  if (!pushSupported) return say('failed this browser cannot receive a push');
  if (pushPermitted) return say('failed notifications were allowed already');
  await say('platform $pushPlatform');
  if (!await connect()) return;
  await say('connected');

  // A browser opens its chooser only for a click of its own, which the host
  // makes where the button is, and answers with a file.
  final attach = find.byTooltip('Attach images');
  final chips = find.byType(InputChip);
  final button = app.getCenter(attach);
  await say('attach ${button.dx.round()} ${button.dy.round()}');
  if (!await until(
    () => present(chips),
    what: 'the image that was picked',
    seconds: 30,
  )) {
    return;
  }
  final chip = chips.evaluate().single.widget as InputChip;
  await say('attached ${(chip.label as Text).data}');
  chip.onDeleted!();
  await wait(900);
  if (present(chips)) return say('failed the image was not removed');

  // Notifications are off until they are asked for.
  if ((await shown()).isNotEmpty) return say('failed a notification is shown');
  await app.tap(settings);
  await wait(900);
  if (!present(turnOn)) return say('failed no "Turn on notifications"');
  if (present(find.text('Push authorization links'))) {
    return say('failed push is offered before it is allowed');
  }
  // The host answers the question that the browser asks now.
  await say('permission');
  await wait(3000);
  await app.tap(turnOn);

  final allow = find.text('Allow notifications');
  if (!await until(() => present(allow), what: 'pairing', seconds: 90)) return;
  final code = find.textContaining('Verification code: ');
  if (!present(code)) return say('failed the pairing shows no code');
  await say('pairing');
  await app.tap(allow);
  if (!await until(
    () => present(find.textContaining('encrypted push enabled')),
    what: 'push enabled',
    seconds: 60,
  )) {
    return;
  }

  final registration = await web.window.navigator.serviceWorker.ready.toDart;
  final subscription = await registration.pushManager.getSubscription().toDart;
  if (subscription == null) return say('failed no subscription');
  await say('subscribed ${Uri.parse(subscription.endpoint).host}');

  await app.tap(settings);
  await wait(900);
  if (present(turnOn) || !present(find.text('Push authorization links'))) {
    return say('failed the menu does not show that push is on');
  }
  await app.tapAt(const Offset(12, 400));
  await wait(900);

  final field = find.byType(TextField).last.evaluate().single.widget;
  (field as TextField).controller!.text = taskPrompt;
  await wait();
  await app.tap(send);
  await wait(1200);
  await say('prompted');

  List<Map<String, Object?>> notifications = const [];
  final arrived = DateTime.now().add(const Duration(seconds: 240));
  while (notifications.isEmpty && DateTime.now().isBefore(arrived)) {
    await wait(1000);
    notifications = await shown();
  }
  if (notifications.isEmpty) return say('failed no notification was shown');
  await say('notification ${jsonEncode(notifications)}');
  if (notifications.first['title'] != 'Forge session completed') {
    return say('failed the notification was not decrypted');
  }

  // A tap leads back to the session of the notification.
  await until(() => present(send), what: 'end of the turn', seconds: 60);
  final conversation = find.textContaining('single word');
  if (!present(conversation)) return say('failed the prompt is not shown');
  await app.tap(sessions.first);
  await until(() => present(find.text('New session')), what: 'sessions');
  await app.tap(find.text('New session'));
  if (!await chooseDirectory()) return;
  await wait(1500);
  if (present(conversation)) return say('failed the session did not change');
  await say('tap');
  if (!await until(
    () => present(conversation),
    what: 'the session of the notification',
    seconds: 30,
  )) {
    return;
  }
  await say('tapped');
  await say('done');
}

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(const PiGoApp());
  app = LiveWidgetController(WidgetsBinding.instance);
  unawaited(
    walkThrough().catchError((Object error, StackTrace stack) {
      return say('failed $error\n$stack');
    }),
  );
}
