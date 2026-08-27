import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/main.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  testWidgets('shows the disconnected agent shell', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    debugDefaultTargetPlatformOverride = TargetPlatform.macOS;
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(const PiGoApp());
    expect(find.text('Pi Go'), findsOneWidget);
    final app = tester.widget<MaterialApp>(find.byType(MaterialApp));
    expect(app.themeMode, ThemeMode.system);
    expect(app.theme?.brightness, Brightness.light);
    expect(app.darkTheme?.brightness, Brightness.dark);
    expect(find.text('Connect'), findsOneWidget);
    expect(find.text('YOLO'), findsOneWidget);
    final yolo = tester.widget<OutlinedButton>(
      find.widgetWithText(OutlinedButton, 'YOLO'),
    );
    final sessions = tester.widget<OutlinedButton>(
      find.widgetWithText(OutlinedButton, 'Sessions  Ctrl-B S'),
    );
    expect(
      tester.getTopLeft(find.byWidget(yolo)).dx,
      lessThan(tester.getTopLeft(find.byWidget(sessions)).dx),
    );
    expect(find.textContaining('upstream —'), findsOneWidget);
    expect(find.text('Local'), findsOneWidget);
    expect(find.text('Unix'), findsOneWidget);
    expect(find.text('TCP'), findsOneWidget);
    final connections = tester.widget<SegmentedButton<ConnectionKind>>(
      find.byType(SegmentedButton<ConnectionKind>),
    );
    expect(connections.selected, {ConnectionKind.local});
    expect(
      connections.segments
          .firstWhere((segment) => segment.value == ConnectionKind.unix)
          .enabled,
      isFalse,
    );
    final promptField = tester
        .widgetList<TextField>(find.byType(TextField))
        .last;
    expect(promptField.decoration?.hintText, contains('Enter to send'));
    expect(
      promptField.contentInsertionConfiguration?.allowedMimeTypes,
      containsAll(['image/png', 'image/jpeg', 'image/gif', 'image/webp']),
    );
    expect(promptField.contextMenuBuilder, isNotNull);
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets('iOS startup preserves trusted server preferences', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(430, 932));
    debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
    final saved = jsonEncode({
      'activeConnection': 0,
      'connections': [
        {
          'kind': 'websocket',
          'address': 'ws://trusted.example:7346/ws',
          'session': 'session-1',
          'reconnect': false,
          'pushAgentId': 'agent-1',
          'trustedServerFingerprint': 'trusted-fingerprint',
          'trustedServerAgentId': 'agent-1',
        },
      ],
    });
    SharedPreferences.setMockInitialValues({
      'forge.connection_state.v1': saved,
    });
    addTearDown(() => tester.binding.setSurfaceSize(null));

    await tester.pumpWidget(const PiGoApp());
    await tester.pumpAndSettle();

    final preferences = await SharedPreferences.getInstance();
    final restored =
        jsonDecode(preferences.getString('forge.connection_state.v1')!)
            as Map<String, dynamic>;
    final connection =
        (restored['connections'] as List).single as Map<String, dynamic>;
    expect(connection['trustedServerFingerprint'], 'trusted-fingerprint');
    expect(connection['trustedServerAgentId'], 'agent-1');
    expect(find.byTooltip('Select trusted server'), findsOneWidget);

    final addressField = tester
        .widgetList<TextField>(find.byType(TextField))
        .firstWhere(
          (field) => field.decoration?.labelText == 'Agent server address',
        );
    addressField.controller!.text = 'ws://other.example:7346/ws';
    await tester.tap(find.byTooltip('Select trusted server'));
    await tester.pumpAndSettle();
    expect(find.text('ws://trusted.example:7346/ws'), findsOneWidget);
    await tester.tap(find.text('ws://trusted.example:7346/ws'));
    await tester.pumpAndSettle();
    expect(addressField.controller!.text, 'ws://trusted.example:7346/ws');

    await tester.tap(find.byTooltip('Manage connections'));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Manage trusted servers'));
    await tester.pumpAndSettle();
    expect(find.text('trusted-fingerprint'), findsOneWidget);
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets('does not show the local agent option on Android', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 844));
    debugDefaultTargetPlatformOverride = TargetPlatform.android;
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(const PiGoApp());
    expect(find.text('Local'), findsNothing);
    expect(find.byTooltip('Close keyboard'), findsOneWidget);
    expect(find.byIcon(Icons.keyboard_arrow_down), findsOneWidget);
    final connections = tester.widget<SegmentedButton<ConnectionKind>>(
      find.byType(SegmentedButton<ConnectionKind>),
    );
    expect(
      connections.segments.map((segment) => segment.value),
      isNot(contains(ConnectionKind.local)),
    );
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets('shows the close keyboard button on iOS', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 844));
    debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
    addTearDown(() => tester.binding.setSurfaceSize(null));

    await tester.pumpWidget(const PiGoApp());

    expect(find.byTooltip('Close keyboard'), findsOneWidget);
    expect(find.byIcon(Icons.keyboard_arrow_down), findsOneWidget);
    expect(tester.takeException(), isNull);
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets('keeps session and connection controls visible on mobile', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 844));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(const PiGoApp());
    expect(find.byIcon(Icons.tune), findsOneWidget);
    expect(find.byIcon(Icons.rocket_launch_outlined), findsOneWidget);
    expect(find.byIcon(Icons.account_tree_outlined), findsOneWidget);
    expect(find.byIcon(Icons.folder_outlined), findsOneWidget);
    expect(find.byIcon(Icons.link_off), findsOneWidget);
    expect(
      tester
          .widgetList<TextField>(find.byType(TextField))
          .last
          .decoration
          ?.hintText,
      contains('Newline with Enter'),
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('uses the same popup on Android at every width', (tester) async {
    await tester.binding.setSurfaceSize(const Size(900, 844));
    debugDefaultTargetPlatformOverride = TargetPlatform.android;
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(const PiGoApp());

    await tester.tap(find.byTooltip('Model, thinking, and providers'));
    await tester.pumpAndSettle();

    expect(find.byType(BottomSheet), findsNothing);
    expect(find.byType(PopupMenuItem<void>), findsNWidgets(6));
    expect(find.text('Copy device whitelist entry'), findsOneWidget);
    expect(find.text('Providers'), findsOneWidget);
    expect(find.text('Push authorization links'), findsOneWidget);
    expect(find.text('classifier: gpt-5.6-luna'), findsOneWidget);
    expect(tester.takeException(), isNull);
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets('uses the same popup in a narrow macOS window', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 844));
    debugDefaultTargetPlatformOverride = TargetPlatform.macOS;
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(const PiGoApp());

    await tester.tap(find.byTooltip('Model, thinking, and providers'));
    await tester.pumpAndSettle();

    expect(find.byType(BottomSheet), findsNothing);
    expect(find.byType(PopupMenuItem<void>), findsNWidgets(6));
    expect(find.text('Copy device whitelist entry'), findsOneWidget);
    expect(find.text('Providers'), findsOneWidget);
    expect(find.text('Push authorization links'), findsOneWidget);
    expect(tester.takeException(), isNull);
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets('macOS header never auto-collapses', (tester) async {
    await tester.binding.setSurfaceSize(const Size(390, 844));
    debugDefaultTargetPlatformOverride = TargetPlatform.macOS;
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    connection.receive(
      jsonEncode({
        'state': {
          'messages': [
            for (var index = 0; index < 30; index++)
              {
                'role': 'user',
                'content': [
                  {'type': 'text', 'text': 'Message $index ${'detail ' * 12}'},
                ],
              },
          ],
        },
      }),
    );
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    await tester.drag(find.byType(ListView).first, const Offset(0, 500));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('collapsed-header-status')), findsNothing);
    expect(find.text('Pi Go'), findsOneWidget);
    await tester.pump(const Duration(seconds: 6));
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets('reconnect catch-up does not reject pending safety approval', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );

    final approvalEvent = jsonEncode({
      'event': {
        'type': 'approval_required',
        'approvalId': 'approval-1',
        'toolCallId': 'tool-1',
        'toolName': 'bash',
        'description': 'Run a guarded operation.',
        'reason': 'The operation needs explicit approval.',
      },
    });
    connection.receive(approvalEvent);
    await tester.pumpAndSettle();
    expect(find.text('Safety classifier approval required?'), findsOneWidget);

    // A disconnect clears transient client state. Reconnect catch-up can restore
    // the same pending approval before the scheduled dialog dismissal runs.
    connection.receive(
      jsonEncode({
        'event': {'type': 'tool_safety_update', 'toolCallId': 'tool-1'},
      }),
    );
    connection.receive(approvalEvent);
    await tester.pumpAndSettle();

    expect(find.text('Safety classifier approval required?'), findsOneWidget);
    expect(connection.pendingApproval?.id, 'approval-1');
    expect(connection.status, 'waiting for safety approval');

    await tester.tap(find.text('Approve once'));
    await tester.pumpAndSettle();
    expect(connection.pendingApproval, isNull);
    expect(connection.status, 'operation manually approved');
  });

  testWidgets('safety approval uses a full-width highlighted bottom sheet', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 844));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'tool_execution_start',
          'toolCallId': 'bash-approval-1',
          'toolName': 'bash',
          'arguments': {
            'command': r"""for file in *.dart; do
  dart format \"$file\"
done""",

            'description': 'Format Dart source files.',
          },
        },
      }),
    );
    await tester.pump();
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'approval_required',
          'approvalId': 'approval-sheet-1',
          'toolCallId': 'bash-approval-1',
          'toolName': 'bash',
          'description': 'Command:\nfor file in *.dart; do ...',
          'reason': 'This command requires explicit approval.',
        },
      }),
    );
    await tester.pumpAndSettle();

    final sheet = find.byKey(const ValueKey('approval-bottom-sheet'));
    expect(sheet, findsOneWidget);
    expect(find.byType(AlertDialog), findsNothing);
    expect(find.byKey(const ValueKey('approval-bash-code')), findsOneWidget);
    expect(find.textContaining('for file in *.dart'), findsWidgets);
    expect(find.byKey(const ValueKey('approval-reject')), findsOneWidget);
    expect(find.byKey(const ValueKey('approval-approve')), findsOneWidget);
    expect(tester.getSize(sheet).width, closeTo(390, 1));
    expect(tester.getBottomLeft(sheet).dy, closeTo(844, 1));
    expect(tester.takeException(), isNull);

    await tester.tap(find.byKey(const ValueKey('approval-reject')));
    await tester.pumpAndSettle();
    expect(connection.pendingApproval, isNull);
    expect(connection.status, 'operation rejected');
  });

  testWidgets('approval sheet previews highlighted write and replace changes', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(430, 932));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );

    connection.receive(
      jsonEncode({
        'event': {
          'type': 'approval_required',
          'approvalId': 'write-approval',
          'toolCallId': 'write-call',
          'toolName': 'write',
          'reason': 'This operation may overwrite source code.',
          'approvalDetails': {
            'type': 'write',
            'path': 'lib/example.php',
            'content': "<?php\necho 'new';\n",
          },
        },
      }),
    );
    await tester.pumpAndSettle();
    expect(
      find.byKey(const ValueKey('approval-write-preview')),
      findsOneWidget,
    );
    expect(find.byKey(const ValueKey('write-preview-code')), findsOneWidget);
    Navigator.of(tester.element(find.byType(AgentPage))).pop();
    await tester.pumpAndSettle();

    connection.receive(
      jsonEncode({
        'event': {
          'type': 'approval_resolved',
          'approvalId': 'write-approval',
          'toolCallId': 'write-call',
        },
      }),
    );
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'approval_required',
          'approvalId': 'replace-approval',
          'toolCallId': 'replace-call',
          'toolName': 'replace',
          'reason': 'This operation may replace source code.',
          'approvalDetails': {
            'type': 'replace',
            'path': 'lib/example.php',
            'startLine': 2,
            'oldText': "echo 'old';",
            'newText': "echo 'new';",
          },
        },
      }),
    );
    await tester.pumpAndSettle();
    expect(
      find.byKey(const ValueKey('approval-replace-preview')),
      findsOneWidget,
    );
    expect(
      find.byKey(const ValueKey('replace-preview-removed')),
      findsOneWidget,
    );
    expect(find.byKey(const ValueKey('replace-preview-added')), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('remote approval resolution closes and serializes dialogs', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );

    Map<String, dynamic> approval(String id, String toolCall, String detail) =>
        {
          'event': {
            'type': 'approval_required',
            'approvalId': id,
            'toolCallId': toolCall,
            'toolName': 'bash',
            'description': detail,
            'reason': 'The operation needs explicit approval.',
          },
        };

    connection.receive(jsonEncode(approval('approval-1', 'tool-1', 'First')));
    await tester.pumpAndSettle();
    expect(find.text('Safety classifier approval required?'), findsOneWidget);
    expect(find.text('First'), findsOneWidget);

    // Another device resolves the first approval. The server can immediately
    // advance to the next one; this client must replace, not stack, its dialog.
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'approval_resolved',
          'approvalId': 'approval-1',
          'toolCallId': 'tool-1',
        },
      }),
    );
    connection.receive(jsonEncode(approval('approval-2', 'tool-2', 'Second')));
    await tester.pumpAndSettle();

    expect(find.text('Safety classifier approval required?'), findsOneWidget);
    expect(find.text('First'), findsNothing);
    expect(find.text('Second'), findsOneWidget);
    expect(connection.pendingApproval?.id, 'approval-2');

    connection.receive(
      jsonEncode({
        'event': {
          'type': 'approval_resolved',
          'approvalId': 'approval-2',
          'toolCallId': 'tool-2',
        },
      }),
    );
    await tester.pumpAndSettle();
    expect(find.text('Safety classifier approval required?'), findsNothing);
    expect(connection.pendingApproval, isNull);
    expect(connection.status, 'safety approval resolved');
  });

  testWidgets('cron tool shows session-scoped jobs and schedule details', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'tool_execution_start',
          'toolCallId': 'cron-1',
          'toolName': 'cron',
          'arguments': {
            'action': 'create',
            'schedule': '0 9 * * 1-5',
            'timezone': 'Asia/Singapore',
            'prompt': 'Review outstanding work.',
          },
        },
      }),
    );
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'tool_execution_end',
          'toolCallId': 'cron-1',
          'toolName': 'cron',
          'result': {
            'content': [
              {'type': 'text', 'text': 'Created cron job 7'},
            ],
            'details': {
              'action': 'create',
              'job': {
                'id': 7,
                'schedule': '0 9 * * 1-5',
                'timezone': 'Asia/Singapore',
                'prompt': 'Review outstanding work.',
              },
            },
          },
        },
      }),
    );
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('cron-preview')), findsOneWidget);
    expect(find.text('Cron job #7'), findsOneWidget);
    expect(find.text('0 9 * * 1-5'), findsOneWidget);
    expect(find.text('Asia/Singapore'), findsOneWidget);
    expect(find.text('Review outstanding work.'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('cron safety approval previews schedule and prompt', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(430, 932));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'approval_required',
          'approvalId': 'cron-approval',
          'toolCallId': 'cron-call',
          'toolName': 'cron',
          'reason': 'This creates a durable autonomous schedule.',
          'approvalDetails': {
            'type': 'cron',
            'action': 'create',
            'schedule': '*/5 * * * *',
            'timezone': 'UTC',
            'prompt': 'Check repository health.',
          },
        },
      }),
    );
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('approval-cron-preview')), findsOneWidget);
    expect(find.text('*/5 * * * *'), findsOneWidget);
    expect(find.text('UTC'), findsOneWidget);
    expect(find.text('Check repository health.'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('latest tool auto-expands while manual panel choices persist', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );

    void startTool(String id, String command) {
      connection.receive(
        jsonEncode({
          'event': {
            'type': 'tool_execution_start',
            'toolCallId': id,
            'toolName': 'bash',
            'arguments': {'command': command, 'description': 'Run $id.'},
          },
        }),
      );
    }

    startTool('tool-1', 'printf first');
    await tester.pump();
    expect(connection.messages.single.collapsed, isFalse);

    startTool('tool-2', 'printf second');
    await tester.pump();
    expect(connection.messages[0].collapsed, isTrue);
    expect(connection.messages[1].collapsed, isFalse);

    // Tapping anywhere on the first panel manually expands it and exempts it
    // from future automatic collapse/expand changes.
    await tester.tap(find.byKey(const ValueKey('tool-panel-tool-1')));
    await tester.pump();
    expect(connection.messages[0].collapsed, isFalse);
    expect(connection.messages[0].collapseManuallySet, isTrue);

    // Tapping the currently expanded latest panel manually collapses it. It too
    // is now exempt, so the next tool does not reopen or otherwise change it.
    final secondPanel = find.byKey(const ValueKey('tool-panel-tool-2'));
    await tester.ensureVisible(secondPanel);
    await tester.pumpAndSettle();
    await tester.tap(secondPanel);
    await tester.pump();
    expect(connection.messages[1].collapsed, isTrue);
    expect(connection.messages[1].collapseManuallySet, isTrue);

    startTool('tool-3', 'printf third');
    await tester.pump();
    expect(connection.messages[0].collapsed, isFalse);
    expect(connection.messages[1].collapsed, isTrue);
    expect(connection.messages[2].collapsed, isFalse);
    expect(tester.takeException(), isNull);
  });

  testWidgets('write tool shows filename and line-numbered source preview', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    const source = 'void main() {\n  print("hello");\n}\n';
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'tool_execution_start',
          'toolCallId': 'write-1',
          'toolName': 'write',
          'arguments': {'path': 'lib/example.dart', 'content': source},
        },
      }),
    );
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );

    expect(find.text('Tool · write · lib/example.dart'), findsOneWidget);
    expect(
      find.byKey(const ValueKey('write-preview-code')),
      findsOneWidget,
      reason: 'the newest tool panel starts expanded',
    );

    expect(
      find.byKey(const ValueKey('write-preview-filename')),
      findsOneWidget,
    );
    expect(find.text('example.dart'), findsOneWidget);
    expect(find.text('lib/example.dart'), findsOneWidget);
    expect(find.byKey(const ValueKey('write-preview-code')), findsOneWidget);
    expect(find.text(source), findsOneWidget);
    expect(
      find.byKey(const ValueKey('write-preview-line-numbers')),
      findsOneWidget,
    );
    expect(find.text('1\n2\n3\n4'), findsOneWidget);
    expect(find.textContaining(r'"content"'), findsNothing);
    expect(find.text('Writing file…'), findsOneWidget);

    connection.receive(
      jsonEncode({
        'event': {
          'type': 'tool_execution_end',
          'toolCallId': 'write-1',
          'toolName': 'write',
          'result': {
            'content': [
              {'type': 'text', 'text': 'Wrote example.dart'},
            ],
          },
        },
      }),
    );
    await tester.pump();
    expect(find.text('Wrote example.dart'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('replace tool shows a syntax-highlighted Git-style diff', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'tool_execution_start',
          'toolCallId': 'replace-1',
          'toolName': 'replace',
          'arguments': {
            'path': 'src/example.php',
            'oldRegex': r'(?s)function oldName.*?\n}',
            'newText': "function newName(): string {\n    return 'new';\n}",
          },
        },
      }),
    );
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    await tester.pump();
    expect(find.text('Tool · replace · src/example.php'), findsOneWidget);

    connection.receive(
      jsonEncode({
        'event': {
          'type': 'tool_execution_end',
          'toolCallId': 'replace-1',
          'toolName': 'replace',
          'result': {
            'content': [
              {
                'type': 'text',
                'text': 'Replaced one regex match in example.php at line 8',
              },
            ],
            'details': {
              'path': 'src/example.php',
              'mode': 'regex',
              'startLine': 8,
              'oldText': "function oldName(): string {\n    return 'old';\n}",
              'newText': "function newName(): string {\n    return 'new';\n}",
            },
          },
        },
      }),
    );
    await tester.pump();

    expect(
      find.byKey(const ValueKey('replace-preview-filename')),
      findsOneWidget,
    );
    expect(find.text('example.php'), findsOneWidget);
    expect(find.text('src/example.php'), findsOneWidget);
    expect(
      find.byKey(const ValueKey('replace-preview-removed')),
      findsOneWidget,
    );
    expect(find.byKey(const ValueKey('replace-preview-added')), findsOneWidget);
    expect(find.textContaining('function oldName'), findsOneWidget);
    expect(find.textContaining('function newName'), findsOneWidget);
    expect(find.textContaining('at line 8'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('streams and folds assistant thinking trace', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    connection.receive(
      jsonEncode({
        'event': {'type': 'agent_start'},
      }),
    );
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'message_start',
          'message': {'role': 'assistant', 'content': []},
        },
      }),
    );
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'message_update',
          'message': {
            'role': 'assistant',
            'content': [
              {
                'type': 'thinking',
                'text': 'Checking the repository structure.',
              },
            ],
          },
        },
      }),
    );
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    expect(find.text('Thinking'), findsOneWidget);
    expect(find.text('Checking the repository structure.'), findsOneWidget);
    expect(find.byKey(const ValueKey('composer-stop')), findsOneWidget);
    expect(find.text('Stop'), findsOneWidget);
    expect(find.text('Abort / kill tool'), findsNothing);
    expect(find.byKey(const ValueKey('composer-send')), findsNothing);

    connection.receive(
      jsonEncode({
        'event': {
          'type': 'message_end',
          'message': {
            'role': 'assistant',
            'content': [
              {
                'type': 'thinking',
                'text': 'Checking the repository structure.',
              },
              {'type': 'text', 'text': 'Repository checked.'},
            ],
          },
          'usage': {'input': 1, 'output': 1, 'totalTokens': 2},
        },
      }),
    );
    connection.receive(
      jsonEncode({
        'event': {'type': 'agent_end'},
      }),
    );
    await tester.pump();
    expect(find.byKey(const ValueKey('composer-send')), findsOneWidget);
    expect(find.text('Send'), findsOneWidget);
    expect(find.byKey(const ValueKey('composer-stop')), findsNothing);
    expect(find.text('Thinking · tap to expand'), findsOneWidget);
    expect(find.text('Checking the repository structure.'), findsNothing);
    expect(find.text('Repository checked.'), findsOneWidget);

    await tester.tap(find.text('Thinking · tap to expand'));
    await tester.pump();
    expect(find.text('Checking the repository structure.'), findsOneWidget);
  });

  testWidgets('streaming does not pull a scrolled transcript to bottom', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    connection.receive(
      jsonEncode({
        'state': {
          'messages': [
            for (var index = 0; index < 30; index++)
              {
                'role': 'user',
                'content': [
                  {
                    'type': 'text',
                    'text': 'Earlier message $index\n${'detail ' * 12}',
                  },
                ],
              },
          ],
        },
      }),
    );
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );

    connection.receive(
      jsonEncode({
        'event': {'type': 'agent_start'},
      }),
    );
    await tester.pumpAndSettle();
    final transcript = tester.widget<ListView>(find.byType(ListView).first);
    final controller = transcript.controller!;
    expect(controller.offset, closeTo(controller.position.maxScrollExtent, 1));

    // A downward finger gesture must not collapse the header.
    await tester.drag(find.byType(ListView).first, const Offset(0, 600));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('collapsed-header-status')), findsNothing);
    expect(find.text('Pi Go'), findsOneWidget);

    // An upward finger gesture collapses it.
    await tester.drag(find.byType(ListView).first, const Offset(0, -300));
    await tester.pumpAndSettle();
    expect(
      find.byKey(const ValueKey('collapsed-header-status')),
      findsOneWidget,
    );
    expect(find.text('Pi Go'), findsNothing);
    await tester.tap(find.byKey(const ValueKey('collapsed-header-status')));
    await tester.pumpAndSettle();
    expect(find.text('Pi Go'), findsOneWidget);
    await tester.drag(find.byType(ListView).first, const Offset(0, 600));
    await tester.pumpAndSettle();
    final userOffset = controller.offset;
    expect(userOffset, lessThan(controller.position.maxScrollExtent - 100));
    expect(find.byTooltip('Scroll to bottom'), findsOneWidget);

    connection.receive(
      jsonEncode({
        'event': {
          'type': 'message_start',
          'message': {'role': 'assistant', 'content': []},
        },
      }),
    );
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'message_update',
          'message': {
            'role': 'assistant',
            'content': [
              {'type': 'text', 'text': 'Streaming token ' * 100},
            ],
          },
        },
      }),
    );
    await tester.pumpAndSettle();

    expect(controller.offset, closeTo(userOffset, 1));
    expect(
      controller.offset,
      lessThan(controller.position.maxScrollExtent - 100),
    );

    await tester.tap(find.byTooltip('Scroll to bottom'));
    await tester.pumpAndSettle();
    expect(controller.offset, closeTo(controller.position.maxScrollExtent, 1));
    expect(find.byTooltip('Scroll to bottom'), findsNothing);
  });

  testWidgets('prepends a 25-message server transcript page', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    connection.receive(
      jsonEncode({
        'command': 'get_state',
        'historyBefore': 35,
        'historyHasMore': true,
        'state': {
          'messages': [
            for (var index = 35; index < 60; index++)
              {
                'role': index.isEven ? 'user' : 'assistant',
                'content': [
                  {'type': 'text', 'text': 'Message $index ${'detail ' * 12}'},
                ],
              },
          ],
        },
      }),
    );
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    await tester.pumpAndSettle();

    var transcript = tester.widget<ListView>(find.byType(ListView).first);
    expect(
      (transcript.childrenDelegate as SliverChildBuilderDelegate)
          .estimatedChildCount,
      25,
    );
    final controller = transcript.controller!;
    controller.jumpTo(controller.position.minScrollExtent);
    await tester.pump();

    connection.receive(
      jsonEncode({
        'command': 'get_transcript_history',
        'historyBefore': 10,
        'historyHasMore': true,
        'historyMessages': [
          for (var index = 10; index < 35; index++)
            {
              'role': index.isEven ? 'user' : 'assistant',
              'content': [
                {'type': 'text', 'text': 'Message $index ${'detail ' * 12}'},
              ],
            },
        ],
      }),
    );
    await tester.pumpAndSettle();

    transcript = tester.widget<ListView>(find.byType(ListView).first);
    expect(
      (transcript.childrenDelegate as SliverChildBuilderDelegate)
          .estimatedChildCount,
      50,
    );
    expect(controller.offset, greaterThan(0));
  });

  testWidgets('loaded transcript starts at the bottom', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final connection = AgentConnection();
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );

    connection.receive(
      jsonEncode({
        'command': 'get_state',
        'state': {
          'messages': [
            for (var index = 0; index < 30; index++)
              {
                'role': 'user',
                'content': [
                  {
                    'type': 'text',
                    'text': 'Loaded message $index\n${'detail ' * 12}',
                  },
                ],
              },
          ],
        },
      }),
    );
    await tester.pumpAndSettle();

    final transcript = tester.widget<ListView>(find.byType(ListView).first);
    expect(
      transcript.controller!.offset,
      closeTo(transcript.controller!.position.maxScrollExtent, 1),
    );
    expect(find.byTooltip('Scroll to bottom'), findsNothing);
  });

  test('does not add an empty assistant transcript item', () {
    final connection = AgentConnection();
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'message_start',
          'message': {'role': 'assistant', 'content': []},
        },
      }),
    );
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'message_end',
          'message': {
            'role': 'assistant',
            'content': [
              {
                'type': 'toolCall',
                'id': 'call-1',
                'name': 'read',
                'arguments': {'path': 'README.md'},
              },
            ],
          },
        },
      }),
    );
    expect(connection.messages, isEmpty);
  });

  testWidgets('manages two independent connection slots', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1024, 768));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(const PiGoApp());

    await tester.tap(find.byTooltip('Manage connections'));
    await tester.pumpAndSettle();
    expect(find.text('Connections'), findsOneWidget);
    expect(find.text('New connection'), findsOneWidget);
    expect(find.textContaining('Connection 1'), findsOneWidget);
    expect(find.byTooltip('Manage trusted servers'), findsOneWidget);
    expect(find.byTooltip('Forget trusted server identity'), findsNothing);

    await tester.tap(find.byTooltip('Manage trusted servers'));
    await tester.pumpAndSettle();
    expect(find.text('Trusted servers'), findsOneWidget);
    expect(find.text('No trusted server identities'), findsOneWidget);
    await tester.tap(find.widgetWithText(TextButton, 'Done').last);
    await tester.pumpAndSettle();

    await tester.tap(find.text('New connection'));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Manage connections'));
    await tester.pumpAndSettle();
    expect(find.textContaining('Connection 1'), findsOneWidget);
    expect(find.textContaining('Connection 2 · ws://'), findsOneWidget);
    final addButton = tester.widget<FilledButton>(
      find.widgetWithText(FilledButton, 'New connection'),
    );
    expect(addButton.onPressed, isNull);
    expect(find.byTooltip('Close connection'), findsNWidgets(2));

    await tester.tap(find.textContaining('Connection 1'));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Manage connections'));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Close connection').last);
    await tester.pumpAndSettle();
    expect(find.byTooltip('Close connection'), findsOneWidget);
  });

  test(
    'keeps OpenAI login and multiple named API providers simultaneously',
    () {
      final connection = AgentConnection();
      connection.receive(
        jsonEncode({
          'command': 'auth_status',
          'authenticated': true,
          'accountId': 'openai-account',
        }),
      );
      connection.receive(
        jsonEncode({
          'command': 'get_provider_config',
          'provider': 'api',
          'providerConfigs': [
            {
              'name': 'Qwen',
              'configured': true,
              'apiKeyConfigured': true,
              'protocol': 'openai',
              'openaiBaseUrl': 'https://qwen.example/v1',
              'anthropicBaseUrl': 'https://qwen.example/anthropic',
              'defaultModel': 'qwen-coder',
              'models': ['qwen-coder'],
            },
            {
              'name': 'Local-Lab',
              'configured': true,
              'apiKeyConfigured': true,
              'protocol': 'openai',
              'openaiBaseUrl': 'http://localhost:11434/v1',
              'anthropicBaseUrl': 'http://localhost:11434',
              'models': ['coder'],
            },
          ],
        }),
      );
      connection.receive(
        jsonEncode({
          'command': 'list_models',
          'models': [
            {
              'id': 'gpt-5.6-terra',
              'provider': 'openai-codex',
              'label': 'OpenAI Codex · gpt-5.6-terra',
            },
            {
              'id': 'Qwen/qwen-coder',
              'provider': 'Qwen',
              'label': 'Qwen · qwen-coder',
            },
            {
              'id': 'Local-Lab/coder',
              'provider': 'Local-Lab',
              'label': 'Local-Lab · coder',
            },
          ],
        }),
      );
      expect(connection.authenticated, isTrue);
      expect(connection.apiProviders.map((provider) => provider.name), [
        'Qwen',
        'Local-Lab',
      ]);
      expect(
        connection.apiProviders.first.openAIBaseURL,
        'https://qwen.example/v1',
      );
      expect(
        connection.availableModels.map((model) => model.id),
        containsAll(['gpt-5.6-terra', 'Qwen/qwen-coder', 'Local-Lab/coder']),
      );
    },
  );

  test('tracks pushed active and waiting-input session status in memory', () {
    final connection = AgentConnection();
    var notification = '';
    bool? needsFeedback;
    connection.onSessionNotification = (session, feedback) {
      notification = session;
      needsFeedback = feedback;
    };
    connection.receive(
      jsonEncode({
        'type': 'session_status',
        'session': 'session-1',
        'active': true,
        'waitingInput': true,
      }),
    );
    expect(connection.sessionActive['session-1'], isTrue);
    expect(connection.sessionsWaitingInput, contains('session-1'));
    expect(notification, 'session-1');
    expect(needsFeedback, isTrue);

    notification = '';
    needsFeedback = null;
    connection.receive(
      jsonEncode({
        'type': 'session_status',
        'session': 'session-1',
        'active': true,
        'waitingInput': true,
      }),
    );
    expect(
      notification,
      isEmpty,
      reason: 'duplicate status must not notify twice',
    );

    connection.receive(
      jsonEncode({
        'type': 'session_status',
        'session': 'session-1',
        'active': false,
        'waitingInput': false,
      }),
    );
    expect(connection.sessionActive['session-1'], isFalse);
    expect(connection.sessionsWaitingInput, isNot(contains('session-1')));
    expect(notification, 'session-1');
    expect(needsFeedback, isFalse);
  });
}
