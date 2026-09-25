import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/browser_live.dart';
import 'package:pi_go_app/browser_workspace.dart';

void main() {
  testWidgets(
    'resize live split to PiP and back without restarting subscription',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1400, 850));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final commands = <Map<String, Object?>>[];
      late BrowserLiveController live;
      bool controlled = false;
      live = BrowserLiveController((command) {
        commands.add(command);
        if (command['type'] == 'browser_control_acquire') controlled = true;
        if (command['type'] == 'browser_control_release' ||
            command['type'] == 'browser_view_stop') {
          controlled = false;
        }
        scheduleMicrotask(
          () => live.receive({
            'id': command['id'],
            'command': command['type'],
            'success': true,
            'browser': {
              'open': true,
              'instance': 'one',
              'controlled': controlled,
            },
            if (command['type'] == 'browser_control_acquire')
              'controlToken': 'lease',
          }),
        );
      });
      live.setSession('session');
      live.receive({
        'browser': {'open': true, 'instance': 'one', 'controlled': false},
      });
      final draft = TextEditingController();
      await tester.pumpWidget(
        MaterialApp(
          home: Scaffold(
            body: AdaptiveBrowserWorkspace(
              controller: live,
              chat: Column(
                children: [
                  const Text('Chat'),
                  TextField(key: const ValueKey('draft'), controller: draft),
                ],
              ),
            ),
          ),
        ),
      );
      await tester.tap(find.byTooltip('Browser live view'));
      await tester.pump();
      expect(
        find.byKey(const ValueKey('browser-split-divider')),
        findsOneWidget,
      );
      final state = tester.state(find.byType(BrowserLiveView));
      expect(
        commands.where((c) => c['type'] == 'browser_view_start'),
        hasLength(1),
      );
      await tester.enterText(
        find.byKey(const ValueKey('draft')),
        'draft stays',
      );
      await tester.tap(find.text('Take control'));
      await tester.pump();
      expect(live.canControl, isTrue);

      await tester.binding.setSurfaceSize(const Size(600, 850));
      await tester.pump();
      await tester.pump();
      expect(find.byKey(const ValueKey('browser-split-divider')), findsNothing);
      expect(
        tester.widget<BrowserLiveView>(find.byType(BrowserLiveView)).compact,
        isTrue,
      );
      expect(tester.state(find.byType(BrowserLiveView)), same(state));
      expect(live.canControl, isFalse);
      expect(
        commands.where((c) => c['type'] == 'browser_control_release'),
        hasLength(1),
      );
      expect(draft.text, 'draft stays');
      expect(find.byTooltip('Expand browser'), findsOneWidget);

      await tester.binding.setSurfaceSize(const Size(1300, 850));
      await tester.pump();
      expect(
        tester.widget<BrowserLiveView>(find.byType(BrowserLiveView)).compact,
        isFalse,
      );
      expect(tester.state(find.byType(BrowserLiveView)), same(state));
      expect(
        commands.where((c) => c['type'] == 'browser_view_start'),
        hasLength(1),
      );
      expect(commands.where((c) => c['type'] == 'browser_view_stop'), isEmpty);
      final divider = find.byKey(const ValueKey('browser-split-divider'));
      final before = tester.getTopLeft(divider).dx;
      await tester.drag(divider, const Offset(80, 0));
      await tester.pump();
      expect(tester.getTopLeft(divider).dx, greaterThan(before));

      await tester.binding.setSurfaceSize(const Size(400, 800));
      await tester.pump();
      await tester.tap(find.byTooltip('Expand browser'));
      await tester.pump();
      expect(
        tester.widget<BrowserLiveView>(find.byType(BrowserLiveView)).compact,
        isFalse,
      );
      // System Back minimizes in place, rather than discarding the browser.
      await tester.binding.handlePopRoute();
      await tester.pump();
      expect(
        tester.widget<BrowserLiveView>(find.byType(BrowserLiveView)).compact,
        isTrue,
      );
      final preview = find.byType(BrowserLiveView);
      await tester.drag(find.text('Browser · Live'), const Offset(-200, 200));
      await tester.pump();
      final rect = tester.getRect(preview);
      expect(rect.left, greaterThanOrEqualTo(0));
      expect(rect.right, lessThanOrEqualTo(400));
      expect(rect.bottom, lessThan(650));
      await tester.tap(find.byTooltip('Close browser preview'));
      await tester.pump();
      expect(find.byType(BrowserLiveView), findsNothing);
      expect(
        commands.where((c) => c['type'] == 'browser_view_stop'),
        hasLength(1),
      );
      await tester.pumpWidget(const SizedBox());
      draft.dispose();
      live.dispose();
    },
  );

  test('release ignores late take-control replies', () async {
    final sent = <Map<String, Object?>>[];
    final live = BrowserLiveController(sent.add);
    live.receive({
      'browser': {'open': true, 'instance': 'one', 'controlled': false},
    });
    live.watching = true;
    final acquire = live.takeControl();
    final acquireID = sent.last['id'];
    final release = live.release();
    final releaseID = sent.last['id'];
    live.receive({
      'id': acquireID,
      'controlToken': 'late',
      'browser': {'open': true, 'instance': 'one', 'controlled': true},
    });
    await acquire;
    expect(live.canControl, isFalse);
    live.receive({
      'id': releaseID,
      'browser': {'open': true, 'instance': 'one', 'controlled': false},
    });
    await release;
    live.dispose();
  });
}
