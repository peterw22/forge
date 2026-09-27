// Runs Forge against a demo agent and walks through it, so that
// scripts/take-screenshots.py can capture the README's screenshots.
//
// It tells the host whenever there is something to do: whitelist this device,
// or take a screenshot. In the simulator it prints a line starting with
// "FORGE-DEMO" and the host captures the screen. In a browser it posts the
// message, and the picture itself, to the address in DEMO_REPORT.
//
// The model chooses what to do, and a demonstration must not approve what
// nobody has read. Requests for approval are rejected, with two exceptions
// that can do no harm: a change to a file inside the demo workspace, and
// running the Go tests there.
import 'dart:async';
import 'dart:convert';

import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:pi_go_app/main.dart';
import 'package:pi_go_app/push_identity.dart';

const endpoint = String.fromEnvironment(
  'DEMO_ENDPOINT',
  defaultValue: 'ws://127.0.0.1:17346/ws',
);
const taskPrompt = String.fromEnvironment(
  'DEMO_TASK',
  defaultValue:
      'Add a MostFrequentWord function to this package, with a test, '
      'and run the tests.',
);
const workspace = String.fromEnvironment(
  'DEMO_WORKSPACE',
  defaultValue: '/private/tmp/forge-demo/workspace',
);
const riskyPrompt = String.fromEnvironment(
  'DEMO_RISKY',
  defaultValue: 'notes.txt looks stale. Tidy up the repository.',
);

/// Answers the app's request to set up notifications itself, so that the
/// system's permission prompt does not cover the screen being captured.
class _QuietMessenger implements BinaryMessenger {
  _QuietMessenger(this._messenger);
  final BinaryMessenger _messenger;

  static const _notifications = 'com.tingouw.forge/feedback';

  @override
  Future<ByteData?>? send(String channel, ByteData? message) {
    if (channel == _notifications) {
      return Future.value(
        const StandardMethodCodec().encodeSuccessEnvelope(null),
      );
    }
    return _messenger.send(channel, message);
  }

  @override
  void setMessageHandler(String channel, MessageHandler? handler) =>
      _messenger.setMessageHandler(channel, handler);

  @override
  Future<void> handlePlatformMessage(
    String channel,
    ByteData? data,
    ui.PlatformMessageResponseCallback? callback,
  ) =>
      // ignore: deprecated_member_use
      _messenger.handlePlatformMessage(channel, data, callback);
}

class _DemoBinding extends WidgetsFlutterBinding {
  @override
  BinaryMessenger createBinaryMessenger() =>
      _QuietMessenger(super.createBinaryMessenger());
}

const report = String.fromEnvironment('DEMO_REPORT');

late final LiveWidgetController app;
final surface = GlobalKey();

void say(String message) {
  debugPrint('FORGE-DEMO $message');
  if (report.isNotEmpty) {
    unawaited(
      http
          .post(Uri.parse('$report/event'), body: message)
          .then<void>((_) {}, onError: (Object _) {}),
    );
  }
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
  say('timeout $what');
  return false;
}

Future<void> shot(String name) async {
  await wait(700);
  if (report.isEmpty) {
    say('shot $name');
    // The host captures the screen when it reads the line above.
    await wait(2500);
    return;
  }
  final render =
      surface.currentContext!.findRenderObject()! as RenderRepaintBoundary;
  final image = await render.toImage(pixelRatio: 2);
  final data = await image.toByteData(format: ui.ImageByteFormat.png);
  image.dispose();
  await http.post(
    Uri.parse('$report/shot/$name'),
    body: data!.buffer.asUint8List(),
  );
}

/// A window shows a labelled button where a phone shows an icon.
final sessions = find.byWidgetPredicate(
  (widget) =>
      (widget is Tooltip && widget.message == 'Switch session') ||
      (widget is Text && widget.data == 'Sessions  Ctrl-B S'),
);
final approval = find.byKey(const ValueKey('approval-bottom-sheet'));
final directory = find.byKey(const ValueKey('directory-path'));
final send = find.byKey(const ValueKey('composer-send'));
final running = find.byWidgetPredicate(
  (widget) =>
      widget.key is ValueKey<String> &&
      (widget.key! as ValueKey<String>).value.startsWith('tool-running-'),
);

/// Answers the question where a session should work, keeping the directory
/// that is offered.
Future<void> chooseDirectory({String? capture}) async {
  if (!await until(
    () => present(directory),
    what: 'directories',
    seconds: 20,
  )) {
    return;
  }
  if (capture != null) await shot(capture);
  await app.tap(find.text('Use this directory'));
  await until(() => !present(directory), what: 'directory chosen');
  await wait(1200);
}

/// Whether the pending request is a change to a file of the demo project.
bool harmlessEdit() {
  final edit =
      present(find.byKey(const ValueKey('approval-write-preview'))) ||
      present(find.byKey(const ValueKey('approval-replace-preview')));
  return edit &&
      present(
        find.descendant(
          of: approval,
          matching: find.textContaining('$workspace/'),
        ),
      );
}

/// Whether the pending request only runs the Go tests of the demo project.
bool harmlessTests() {
  final code = find.descendant(
    of: find.byKey(const ValueKey('approval-bash-code')),
    matching: find.byType(SelectableText),
  );
  if (!present(code)) return false;
  final command = (code.evaluate().first.widget as SelectableText).textSpan!
      .toPlainText()
      .trim();
  final tests = RegExp(
    r'^(?:cd (\S+) && )?go test [\w./ -]*$',
  ).firstMatch(command);
  if (tests == null) return false;
  final target = tests.group(1);
  return target == null || target == workspace;
}

Future<void> prompt(String text) async {
  final field = find.byType(TextField).last.evaluate().single.widget;
  (field as TextField).controller!.text = text;
  await wait();
  await app.tap(send);
  await wait(1200);
}

/// Waits for the turn to end, capturing the first of each moment of interest.
Future<void> followTurn(String name, {int seconds = 420}) async {
  var sawRunning = false;
  var sawApproval = false;
  final deadline = DateTime.now().add(Duration(seconds: seconds));
  while (DateTime.now().isBefore(deadline)) {
    if (present(approval)) {
      if (!sawApproval) {
        sawApproval = true;
        await shot('$name-approval');
      }
      if (harmlessTests()) {
        say('approving the Go tests of the demo workspace');
        await app.tap(find.byKey(const ValueKey('approval-approve')));
      } else if (harmlessEdit()) {
        say('approving a file change in the demo workspace');
        await app.tap(find.byKey(const ValueKey('approval-approve')));
      } else {
        say('rejecting an approval request');
        await app.tap(find.byKey(const ValueKey('approval-reject')));
      }
      await wait(1500);
      continue;
    }
    if (!sawRunning && present(running)) {
      sawRunning = true;
      await shot('$name-running');
      continue;
    }
    if (present(send)) {
      await wait(1500);
      if (present(send) && !present(approval)) return;
    }
    await wait(400);
  }
  say('timeout turn $name');
}

Future<void> openPanel(String title) async {
  final panel = find.textContaining(title);
  if (!present(panel)) {
    say('missing panel $title');
    return;
  }
  await Scrollable.ensureVisible(
    panel.evaluate().last,
    alignment: .08,
    duration: const Duration(milliseconds: 300),
  );
  await wait(600);
  await app.tap(panel.last);
  await wait(900);
  await Scrollable.ensureVisible(
    panel.evaluate().last,
    alignment: .08,
    duration: const Duration(milliseconds: 300),
  );
  await wait(600);
}

Future<void> walkThrough() async {
  await wait(2500);

  final identity = await getDeviceIdentity();
  if (identity == null) {
    say('failed no device identity');
    return;
  }
  say(
    'identity ${jsonEncode({'name': 'Demo device', 'deviceId': identity.deviceId, 'publicKey': identity.publicKey, 'fingerprint': identity.fingerprint})}',
  );
  // The host adds the device to the agent's whitelist and restarts the agent.
  await wait(9000);

  final address = find.byWidgetPredicate(
    (widget) =>
        widget is TextField &&
        widget.decoration?.labelText == 'Agent server address',
  );
  (address.evaluate().single.widget as TextField).controller!.text = endpoint;
  await wait();
  await shot('connect');
  await app.tap(find.text('Connect'));

  final trust = find.text('Trust this server');
  await until(
    () => present(trust) || !present(find.text('Connect')),
    what: 'trust prompt',
    seconds: 40,
  );
  if (present(trust)) {
    await shot('trust');
    await app.tap(trust);
  }
  if (!await until(
    () => !present(find.text('Connect')) && present(sessions),
    what: 'connection',
    seconds: 40,
  )) {
    await shot('could-not-connect');
    say('failed could not connect');
    return;
  }
  await wait(1500);
  await chooseDirectory(capture: 'directory');

  await prompt(taskPrompt);
  await followTurn('task');
  await shot('conversation');

  for (final title in const ['Tool · replace', 'Tool · write']) {
    if (present(find.textContaining(title))) {
      await openPanel(title);
      await shot('change');
      await app.tap(find.textContaining(title).last);
      await wait(600);
      break;
    }
  }

  await app.tap(find.byTooltip('Model, thinking, and providers'));
  await wait(900);
  await shot('settings');
  // Tapping outside the menu closes it.
  await app.tapAt(const Offset(12, 400));
  await wait(900);

  // The second request runs in a session of its own.
  await app.tap(sessions.first);
  await until(() => present(find.text('New session')), what: 'sessions');
  await app.tap(find.text('New session'));
  await chooseDirectory();
  await wait(1500);

  await prompt(riskyPrompt);
  await followTurn('risky', seconds: 240);

  await app.tap(sessions.first);
  await until(() => present(find.text('New session')), what: 'sessions');
  await shot('sessions');
  await app.tap(find.text('Cancel'));
  await wait(900);

  say('done');
}

void main() {
  _DemoBinding();
  runApp(RepaintBoundary(key: surface, child: const PiGoApp()));
  app = LiveWidgetController(WidgetsBinding.instance);
  unawaited(
    walkThrough().catchError((Object error, StackTrace stack) {
      say('failed $error\n$stack');
    }),
  );
}
