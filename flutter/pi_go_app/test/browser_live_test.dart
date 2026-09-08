import 'dart:async';
import 'dart:convert';

import 'dart:ui' as ui;
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/browser_live.dart';
import 'package:pi_go_app/main.dart';

void main() {
  const geometry = <String, dynamic>{
    'width': 1280,
    'height': 720,
    'viewportWidth': 1280,
    'viewportHeight': 800,
    'contentX': 64,
    'contentY': 0,
    'contentWidth': 1152,
    'contentHeight': 720,
  };
  test('live frame letterbox maps to browser CSS coordinates', () {
    expect(
      browserViewportPoint(const Offset(640, 360), geometry),
      const Offset(640, 400),
    );
    expect(browserViewportPoint(const Offset(20, 100), geometry), isNull);
    expect(browserViewportPoint(const Offset(1216, 100), geometry), isNull);
    final transform = TransformationController(
      Matrix4.diagonal3Values(2, 2, 1),
    );
    expect(
      browserViewportPoint(
        transform.toScene(const Offset(1280, 720)),
        geometry,
      ),
      const Offset(640, 400),
    );
    transform.dispose();
  });

  test('frames do not enter chat or rebuild transcript', () {
    final connection = AgentConnection();
    connection.receive(
      jsonEncode({
        'type': 'response',
        'command': 'get_state',
        'session': 'one',
        'browser': {
          'open': true,
          'instance': 'browser-one',
          'controlled': false,
        },
      }),
    );
    var notifications = 0;
    connection.addListener(() => notifications++);
    connection.browserLive.watching = true;
    connection.receive(
      jsonEncode({
        'type': 'browser_frame',
        'browserFrame': {
          ...geometry,
          'instance': 'browser-one',
          'sequence': 1,
          'jpeg': base64Encode([1, 2, 3]),
        },
      }),
    );
    expect(connection.browserLive.jpeg, [1, 2, 3]);
    expect(connection.messages, isEmpty);
    expect(notifications, 0);
    connection.dispose();
  });

  test('stale input errors preserve control and displayed frames', () {
    final sent = <Map<String, Object?>>[];
    final live = BrowserLiveController(sent.add);
    live.setSession('one');
    live.receive({
      'browser': {'open': true, 'instance': 'a', 'controlled': true},
    });
    live.watching = true;
    live.receive({
      'controlToken': 'lease',
      'browserFrame': {
        ...geometry,
        'instance': 'a',
        'sequence': 9,
        'jpeg': 'AQ==',
      },
    });
    live.receive({'command': 'browser_input', 'error': 'stale browser frame'});
    expect(live.canControl, isTrue);
    expect(live.jpeg, [1]);
    live.acknowledge(live.frame!);
    expect(sent.last['type'], 'browser_frame_ack');
    expect(sent.last['frameId'], 9);
    live.dispose();
  });

  test('stale frames and session switches clear control', () {
    final live = BrowserLiveController((_) {});
    live.setSession('one');
    live.receive({
      'browser': {'open': true, 'instance': 'a', 'controlled': false},
    });
    live.watching = true;
    live.receive({
      'browserFrame': {
        ...geometry,
        'instance': 'a',
        'sequence': 2,
        'jpeg': 'AQ==',
      },
    });
    live.receive({
      'browserFrame': {
        ...geometry,
        'instance': 'a',
        'sequence': 1,
        'jpeg': 'Ag==',
      },
    });
    expect(live.jpeg, [1]);
    live.receive({'controlToken': 'token'});
    live.setSession('two');
    expect(live.open, isFalse);
    expect(live.token, isEmpty);
    expect(live.jpeg, isNull);
    live.dispose();
  });

  testWidgets('live view subscribes, preserves transform and stops on exit', (
    tester,
  ) async {
    late BrowserLiveController live;
    final commands = <Map<String, Object?>>[];
    live = BrowserLiveController((command) {
      commands.add(command);
      scheduleMicrotask(
        () => live.receive({
          'id': command['id'],
          'command': command['type'],
          'success': true,
          if (command['type'] == 'browser_control_acquire')
            'controlToken': 'lease',
          'browser': {
            'open': true,
            'instance': 'a',
            'controlled': command['type'] == 'browser_control_acquire',
          },
        }),
      );
    });
    live.setSession('one');
    live.receive({
      'browser': {'open': true, 'instance': 'a', 'controlled': false},
    });
    await tester.pumpWidget(
      MaterialApp(home: BrowserLiveView(controller: live)),
    );
    await tester.pump();
    expect(commands.first['type'], 'browser_view_start');
    expect(find.text('Take control'), findsOneWidget);
    await tester.runAsync(() async {
      final recorder = ui.PictureRecorder();
      final canvas = ui.Canvas(recorder);
      canvas.drawColor(const Color(0xff112233), ui.BlendMode.src);
      final picture = recorder.endRecording();
      final sample = await picture.toImage(16, 16);
      final data = await sample.toByteData(format: ui.ImageByteFormat.png);
      final png = base64Encode(data!.buffer.asUint8List());
      sample.dispose();
      picture.dispose();
      live.receive({
        'browserFrame': {
          ...geometry,
          'instance': 'a',
          'sequence': 7,
          'jpeg': png,
        },
      });
      await Future<void>.delayed(const Duration(milliseconds: 200));
    });
    await tester.pump();
    expect(live.error, isEmpty);
    expect(tester.widget<RawImage>(find.byType(RawImage)).image, isNotNull);
    expect(
      commands
          .where((value) => value['type'] == 'browser_frame_ack')
          .last['frameId'],
      7,
    );
    final viewer = tester.widget<InteractiveViewer>(
      find.byType(InteractiveViewer),
    );
    viewer.transformationController!.value = Matrix4.diagonal3Values(2, 2, 1);
    live.receive({
      'browser': {'open': true, 'instance': 'a', 'controlled': false},
    });
    await tester.pump();
    expect(viewer.transformationController!.value.getMaxScaleOnAxis(), 2);
    await tester.tap(find.text('Take control'));
    await tester.pump();
    expect(live.canControl, isTrue);
    final area = tester.getTopLeft(find.byType(InteractiveViewer));
    await tester.tapAt(area + const Offset(300, 120));
    await tester.pump();
    final click = commands.lastWhere(
      (value) => value['type'] == 'browser_input',
    );
    expect(click['frameId'], 7);
    expect(click['browserAction'], 'click');
    expect(click['controlToken'], 'lease');
    expect(click['x'], closeTo((150 - 64) * 1280 / 1152, 0.1));
    expect(click['y'], closeTo(60 * 800 / 720, 0.1));
    await tester.pumpWidget(const SizedBox());
    await tester.pump();
    expect(commands.last['type'], 'browser_view_stop');
    expect(live.token, isEmpty);
    live.dispose();
  });

  testWidgets(
    'browser button appears with an open browser and empty transcript',
    (tester) async {
      final connection = AgentConnection();
      await tester.pumpWidget(
        MaterialApp(home: AgentPage(connection: connection)),
      );
      expect(find.byTooltip('Browser live view'), findsNothing);
      connection.browserLive.receive({
        'browser': {'open': true, 'instance': 'a', 'controlled': false},
      });
      await tester.pump();
      expect(find.byTooltip('Browser live view'), findsOneWidget);
      await tester.pumpWidget(const SizedBox());
    },
  );
}
