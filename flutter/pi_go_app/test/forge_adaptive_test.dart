import 'dart:convert';

import 'package:flutter/cupertino.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/forge_theme.dart';
import 'package:pi_go_app/main.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  void useWindow(WidgetTester tester, Size size) {
    tester.view.devicePixelRatio = 1;
    tester.view.physicalSize = size;
    addTearDown(tester.view.reset);
  }

  void trustOneServer() {
    SharedPreferences.setMockInitialValues({
      'forge.connection_state.v1': jsonEncode({
        'activeConnection': 0,
        'connections': [
          {
            'kind': 'websocket',
            'address': 'ws://trusted.example:7346/ws',
            'reconnect': false,
            'trustedServerFingerprint': 'trusted-fingerprint',
            'trustedServerAgentId': 'agent-1',
          },
        ],
      }),
    });
  }

  test('theme follows the design language of each platform', () {
    final ios = forgeTheme(
      brightness: Brightness.light,
      platform: TargetPlatform.iOS,
    );
    final mac = forgeTheme(
      brightness: Brightness.light,
      platform: TargetPlatform.macOS,
    );
    final android = forgeTheme(
      brightness: Brightness.light,
      platform: TargetPlatform.android,
    );
    final linux = forgeTheme(
      brightness: Brightness.dark,
      platform: TargetPlatform.linux,
    );

    expect(ios.splashFactory, NoSplash.splashFactory);
    expect(android.splashFactory, InkSparkle.splashFactory);
    expect(ios.visualDensity, VisualDensity.standard);
    expect(mac.visualDensity, VisualDensity.compact);
    expect(linux.visualDensity, VisualDensity.compact);
    expect(linux.brightness, Brightness.dark);
    // Android keeps tonal Material surfaces; the others are neutral.
    expect(ios.colorScheme.surface, const Color(0xffffffff));
    expect(android.colorScheme.surface, isNot(const Color(0xffffffff)));
    expect(ios.colorScheme.primary, android.colorScheme.primary);

    final phone = ForgeStyle.forPlatform(TargetPlatform.iOS, ios.colorScheme);
    final desktop = ForgeStyle.forPlatform(
      TargetPlatform.macOS,
      mac.colorScheme,
    );
    expect(phone.family, ForgeFamily.apple);
    expect(desktop.family, ForgeFamily.apple);
    expect(phone.touch, isTrue);
    expect(desktop.touch, isFalse);
    expect(phone.controlHeight, greaterThan(desktop.controlHeight));
    expect(phone.mono().fontFamily, 'Menlo');
    expect(
      ForgeStyle.forPlatform(
        TargetPlatform.android,
        android.colorScheme,
      ).mono().fontFamily,
      'monospace',
    );
  });

  for (final platform in [TargetPlatform.iOS, TargetPlatform.android]) {
    testWidgets('a $platform phone presents connections as a bottom sheet', (
      tester,
    ) async {
      useWindow(tester, const Size(393, 852));
      debugDefaultTargetPlatformOverride = platform;
      await tester.pumpWidget(const PiGoApp());
      await tester.pumpAndSettle();

      await tester.tap(find.byTooltip('Manage connections'));
      await tester.pumpAndSettle();

      expect(find.byType(BottomSheet), findsOneWidget);
      expect(find.byType(Dialog), findsNothing);
      expect(find.text('Connections'), findsOneWidget);
      expect(find.widgetWithText(FilledButton, 'New connection'), findsOne);

      await tester.tap(find.widgetWithText(TextButton, 'Done'));
      await tester.pumpAndSettle();
      expect(find.byType(BottomSheet), findsNothing);
      expect(tester.takeException(), isNull);
      debugDefaultTargetPlatformOverride = null;
    });
  }

  for (final (platform, size) in [
    (TargetPlatform.macOS, const Size(1100, 760)),
    // A narrow desktop window is still driven by a pointer.
    (TargetPlatform.macOS, const Size(420, 760)),
    (TargetPlatform.linux, const Size(1100, 760)),
    // A tablet has room for a dialog.
    (TargetPlatform.android, const Size(900, 1200)),
  ]) {
    testWidgets('$platform at ${size.width} presents connections as a dialog', (
      tester,
    ) async {
      useWindow(tester, size);
      debugDefaultTargetPlatformOverride = platform;
      await tester.pumpWidget(const PiGoApp());
      await tester.pumpAndSettle();

      await tester.tap(find.byTooltip('Manage connections'));
      await tester.pumpAndSettle();

      expect(find.byType(Dialog), findsOneWidget);
      expect(find.byType(BottomSheet), findsNothing);
      expect(find.text('Connections'), findsOneWidget);
      expect(tester.takeException(), isNull);
      debugDefaultTargetPlatformOverride = null;
    });
  }

  Future<void> openForgetConfirmation(WidgetTester tester) async {
    await tester.pumpWidget(const PiGoApp());
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Manage connections'));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Manage trusted servers'));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('Delete trusted server'));
    await tester.pumpAndSettle();
    expect(find.text('Forget trusted server identity?'), findsOneWidget);
  }

  Future<Map<String, dynamic>> savedState() async {
    final preferences = await SharedPreferences.getInstance();
    return jsonDecode(preferences.getString('forge.connection_state.v1')!)
        as Map<String, dynamic>;
  }

  testWidgets('Apple platforms confirm with a system alert', (tester) async {
    useWindow(tester, const Size(393, 852));
    debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
    trustOneServer();
    await openForgetConfirmation(tester);

    expect(find.byType(CupertinoAlertDialog), findsOneWidget);
    expect(find.byType(AlertDialog), findsNothing);
    final forget = tester.widget<CupertinoDialogAction>(
      find.widgetWithText(CupertinoDialogAction, 'Forget identity'),
    );
    expect(forget.isDestructiveAction, isTrue);

    await tester.tap(find.widgetWithText(CupertinoDialogAction, 'Cancel'));
    await tester.pumpAndSettle();
    expect(find.byType(CupertinoAlertDialog), findsNothing);
    expect(find.text('trusted-fingerprint'), findsOneWidget);
    expect((await savedState())['trustedServers'], hasLength(1));
    expect(tester.takeException(), isNull);
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets('Android confirms with a Material alert and forgets the server', (
    tester,
  ) async {
    useWindow(tester, const Size(412, 915));
    debugDefaultTargetPlatformOverride = TargetPlatform.android;
    trustOneServer();
    await openForgetConfirmation(tester);

    expect(find.byType(AlertDialog), findsOneWidget);
    expect(find.byType(CupertinoAlertDialog), findsNothing);

    await tester.tap(find.widgetWithText(FilledButton, 'Forget identity'));
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsNothing);
    expect(find.text('No trusted server identities'), findsOneWidget);
    expect((await savedState())['trustedServers'], isEmpty);
    expect(tester.takeException(), isNull);
    debugDefaultTargetPlatformOverride = null;
  });

  testWidgets(
    'phone composer sends with an icon, window composer with a label',
    (tester) async {
      useWindow(tester, const Size(393, 852));
      debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
      await tester.pumpWidget(const PiGoApp());
      await tester.pumpAndSettle();
      expect(find.byKey(const ValueKey('composer-send')), findsOneWidget);
      expect(find.byTooltip('Send'), findsOneWidget);
      expect(find.text('Send'), findsNothing);
      expect(find.byTooltip('Close keyboard'), findsOneWidget);

      tester.view.physicalSize = const Size(1100, 760);
      debugDefaultTargetPlatformOverride = TargetPlatform.macOS;
      await tester.pumpWidget(const SizedBox());
      await tester.pumpWidget(const PiGoApp());
      await tester.pumpAndSettle();
      expect(find.widgetWithText(FilledButton, 'Send'), findsOneWidget);
      expect(find.byTooltip('Close keyboard'), findsNothing);
      expect(tester.takeException(), isNull);
      debugDefaultTargetPlatformOverride = null;
    },
  );

  testWidgets('safety approval is a sheet on a phone and a panel in a window', (
    tester,
  ) async {
    final approval = jsonEncode({
      'event': {
        'type': 'approval_required',
        'approvalId': 'approval-1',
        'toolCallId': 'tool-1',
        'toolName': 'bash',
        'description': 'Run a guarded operation.',
        'reason': 'The operation needs explicit approval.',
      },
    });
    final panel = find.byKey(const ValueKey('approval-bottom-sheet'));

    useWindow(tester, const Size(1100, 760));
    final connection = AgentConnection();
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    connection.receive(approval);
    await tester.pumpAndSettle();
    expect(tester.getSize(panel).width, 640);
    expect(tester.getCenter(panel).dx, closeTo(550, 1));
    expect(tester.getBottomLeft(panel).dy, lessThan(760));

    await tester.tap(find.byKey(const ValueKey('approval-approve')));
    await tester.pumpAndSettle();
    expect(connection.status, 'operation manually approved');

    tester.view.physicalSize = const Size(393, 852);
    await tester.pumpAndSettle();
    connection.receive(approval);
    await tester.pumpAndSettle();
    expect(tester.getSize(panel).width, closeTo(393, 1));
    expect(tester.getBottomLeft(panel).dy, closeTo(852, 1));
    expect(tester.takeException(), isNull);
  });

  Map<String, dynamic> replaceHistory({
    required Map<String, dynamic> arguments,
    required String output,
    bool isError = false,
    Map<String, dynamic>? details,
  }) => {
    'command': 'get_state',
    'state': {
      'messages': [
        {
          'role': 'assistant',
          'content': [
            {
              'type': 'toolCall',
              'id': 'call-1',
              'name': 'replace',
              'arguments': arguments,
            },
          ],
        },
        {
          'role': 'toolResult',
          'toolCallId': 'call-1',
          'toolName': 'replace',
          'isError': isError,
          'toolDetails': ?details,
          'content': [
            {'type': 'text', 'text': output},
          ],
        },
      ],
    },
  };

  Future<void> showHistory(
    WidgetTester tester,
    Map<String, dynamic> history,
  ) async {
    useWindow(tester, const Size(1100, 760));
    final connection = AgentConnection()..receive(jsonEncode(history));
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    await tester.pumpAndSettle();
    expect(connection.messages.single.collapsed, isTrue);
    await tester.tap(find.textContaining('Tool · replace'));
    await tester.pump();
  }

  final removed = find.byKey(const ValueKey('replace-preview-removed'));
  final added = find.byKey(const ValueKey('replace-preview-added'));

  testWidgets('reloaded replace without details still shows its diff', (
    tester,
  ) async {
    await showHistory(
      tester,
      replaceHistory(
        arguments: {
          'path': 'README.md',
          'oldText': '## Image paste',
          'newText': '## Platform look',
        },
        output: 'Replaced one text match in README.md at line 68',
      ),
    );
    expect(removed, findsOneWidget);
    expect(added, findsOneWidget);
    expect(find.textContaining('## Image paste'), findsOneWidget);
    expect(find.textContaining('## Platform look'), findsOneWidget);
    // The line comes from the result text when details are missing.
    expect(
      find.descendant(of: removed, matching: find.text('68')),
      findsOneWidget,
    );
    expect(find.textContaining('"oldText"'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('reloaded replace prefers the matched text in its details', (
    tester,
  ) async {
    await showHistory(
      tester,
      replaceHistory(
        arguments: {'path': 'a.dart', 'oldRegex': 'va. x', 'newText': 'new'},
        output: 'Replaced one regex match in a.dart at line 3',
        details: {
          'path': 'a.dart',
          'oldText': 'var x',
          'newText': 'final x',
          'startLine': 3,
        },
      ),
    );
    expect(find.descendant(of: removed, matching: find.text('3')), findsOne);
    expect(find.textContaining('var x'), findsOneWidget);
    expect(find.textContaining('final x'), findsOneWidget);
  });

  testWidgets('replace that cannot be drawn as a diff shows its raw call', (
    tester,
  ) async {
    // A regex call does not say which text it matched.
    await showHistory(
      tester,
      replaceHistory(
        arguments: {'path': 'a.dart', 'oldRegex': 'va. x', 'newText': 'new'},
        output: 'Replaced one regex match in a.dart at line 3',
      ),
    );
    expect(removed, findsNothing);
    expect(find.textContaining('"oldRegex"'), findsOneWidget);

    // A failed call changed nothing, so a diff would mislead.
    await tester.pumpWidget(const SizedBox());
    await showHistory(
      tester,
      replaceHistory(
        arguments: {'path': 'a.dart', 'oldText': 'old', 'newText': 'new'},
        output: 'Tool failed: oldText must occur exactly once',
        isError: true,
      ),
    );
    expect(removed, findsNothing);
    expect(find.textContaining('"oldText"'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('code in a tool panel has no band painted behind its lines', (
    tester,
  ) async {
    useWindow(tester, const Size(1100, 760));
    final connection = AgentConnection()
      ..receive(
        jsonEncode({
          'event': {
            'type': 'tool_execution_start',
            'toolCallId': 'bash-1',
            'toolName': 'bash',
            'arguments': {'command': 'printf first', 'description': 'Print'},
          },
        }),
      );
    await tester.pumpWidget(
      MaterialApp(
        theme: forgeTheme(brightness: Brightness.light),
        home: AgentPage(connection: connection),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('tool-panel-bash-1')));
    await tester.pump();

    // Selectable markdown renders through SelectableText, not RichText.
    final backgrounds = <Color?>[];
    for (final text in tester.widgetList<SelectableText>(
      find.byType(SelectableText),
    )) {
      final root = text.textSpan;
      if (root == null || !root.toPlainText().contains('printf first')) {
        continue;
      }
      root.visitChildren((span) {
        backgrounds.add(span.style?.backgroundColor);
        return true;
      });
    }
    expect(backgrounds, isNotEmpty);
    expect(backgrounds.every((color) => color == null || color.a == 0), isTrue);
  });

  for (final (platform, indicator) in [
    (TargetPlatform.iOS, CupertinoActivityIndicator),
    (TargetPlatform.macOS, CupertinoActivityIndicator),
    (TargetPlatform.android, CircularProgressIndicator),
    (TargetPlatform.linux, CircularProgressIndicator),
  ]) {
    testWidgets('a running tool uses the $platform activity indicator', (
      tester,
    ) async {
      useWindow(tester, const Size(1100, 760));
      final connection = AgentConnection();
      await tester.pumpWidget(
        MaterialApp(
          theme: forgeTheme(brightness: Brightness.light, platform: platform),
          home: AgentPage(connection: connection),
        ),
      );
      connection
        ..receive(
          jsonEncode({
            'event': {'type': 'agent_start'},
          }),
        )
        ..receive(
          jsonEncode({
            'event': {
              'type': 'tool_execution_start',
              'toolCallId': 'tool-1',
              'toolName': 'bash',
              'arguments': {'command': 'sleep 30', 'description': 'Wait'},
            },
          }),
        );
      await tester.pump();

      final running = find.byKey(const ValueKey('tool-running-tool-1'));
      expect(
        find.descendant(of: running, matching: find.byType(indicator)),
        findsOneWidget,
      );
      expect(tester.getSize(running), const Size.square(14));

      connection.receive(
        jsonEncode({
          'event': {'type': 'agent_end'},
        }),
      );
      await tester.pumpAndSettle();
      expect(running, findsNothing);
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets('code indented with tabs keeps its indentation in a preview', (
    tester,
  ) async {
    useWindow(tester, const Size(1100, 760));
    final connection = AgentConnection()
      ..receive(
        jsonEncode({
          'event': {
            'type': 'tool_execution_start',
            'toolCallId': 'write-1',
            'toolName': 'write',
            'arguments': {
              'path': 'main.go',
              'content': 'func main() {\n\tif ok {\n\t\trun()\n\t}\n}\n',
            },
          },
        }),
      );
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    await tester.tap(find.byKey(const ValueKey('tool-panel-write-1')));
    await tester.pump();

    final code = tester.widget<SelectableText>(
      find.byKey(const ValueKey('write-preview-code')),
    );
    expect(
      code.textSpan!.toPlainText(),
      'func main() {\n    if ok {\n        run()\n    }\n}\n',
    );
    // A tab after text advances to the next stop, not by a fixed amount.
    connection.receive(
      jsonEncode({
        'event': {
          'type': 'tool_execution_start',
          'toolCallId': 'write-2',
          'toolName': 'write',
          'arguments': {'path': 'table.txt', 'content': 'ab\tc\nabcd\te'},
        },
      }),
    );
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('tool-panel-write-1')));
    await tester.tap(find.byKey(const ValueKey('tool-panel-write-2')));
    await tester.pump();
    expect(
      tester
          .widget<SelectableText>(
            find.byKey(const ValueKey('write-preview-code')),
          )
          .textSpan!
          .toPlainText(),
      'ab  c\nabcd    e',
    );
  });
}
