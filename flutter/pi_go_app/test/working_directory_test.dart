import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/forge_adaptive.dart';
import 'package:pi_go_app/main.dart';
import 'package:pi_go_app/transport_base.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// An agent with a small directory tree, which records what it is sent.
class FakeAgent implements AgentTransport {
  FakeAgent(this.connection);
  final AgentConnection connection;
  final sent = <Map<String, dynamic>>[];

  static const tree = {
    '/': ['home'],
    '/home': ['dev'],
    '/home/dev': ['.config', 'forge', 'notes'],
    '/home/dev/forge': ['cmd', 'docs'],
    '/home/dev/forge/cmd': <String>[],
  };

  Iterable<Map<String, dynamic>> of(String type) =>
      sent.where((command) => command['type'] == type);

  @override
  Stream<String> get messages => const Stream.empty();

  @override
  Future<void> close() async {}

  @override
  void send(String message) {
    final command = jsonDecode(message) as Map<String, dynamic>;
    sent.add(command);
    switch (command['type']) {
      case 'list_directories':
        final path = '${command['directory'] ?? '/home/dev/forge'}';
        final names = tree[path];
        reply(
          names == null
              ? {'command': 'list_directories', 'error': 'no such directory'}
              : {
                  'command': 'list_directories',
                  'directory': path,
                  'parent': path == '/'
                      ? ''
                      : path.substring(0, path.lastIndexOf('/')).isEmpty
                      ? '/'
                      : path.substring(0, path.lastIndexOf('/')),
                  'home': '/home/dev',
                  'directories': names,
                },
        );
      case 'list_sessions':
        reply({
          'command': 'list_sessions',
          'sessions': [
            {
              'id': 'session-1',
              'summary': 'Added a parser.',
              'lastMessageTime': '2026-09-26T14:12:00Z',
              'cwd': '/home/dev/forge',
            },
            {
              'id': 'session-2',
              'summary': 'Wrote the notes.',
              'lastMessageTime': '2026-09-25T09:40:00Z',
              'cwd': '/home/dev/notes',
            },
          ],
        });
    }
  }

  void reply(Map<String, dynamic> response) =>
      connection.receive(jsonEncode({'type': 'response', ...response}));

  /// What the agent sends when a session opens.
  void open(
    String command, {
    String session = 'session-1',
    String cwd = '/home/dev/forge',
    String model = 'gpt-5.6-terra',
    bool empty = true,
  }) => reply({
    'command': command,
    'session': session,
    'cwd': cwd,
    'model': model,
    'state': {
      'messages': [
        if (!empty)
          {
            'role': 'user',
            'content': [
              {'type': 'text', 'text': 'hello'},
            ],
          },
      ],
    },
  });
}

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  Future<FakeAgent> connect(
    WidgetTester tester, {
    Size size = const Size(1100, 760),
  }) async {
    tester.view.devicePixelRatio = 1;
    tester.view.physicalSize = size;
    addTearDown(tester.view.reset);
    final connection = AgentConnection();
    final agent = FakeAgent(connection);
    await connection.attachTransport(agent);
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    return agent;
  }

  final chooser = find.byKey(const ValueKey('directory-path'));
  String shownPath(WidgetTester tester) =>
      tester.widget<SelectableText>(chooser).data!;

  testWidgets('a session that starts empty asks where it should work', (
    tester,
  ) async {
    final agent = await connect(tester);
    agent.open('get_state');
    await tester.pumpAndSettle();

    expect(find.text('Where should this session work?'), findsOneWidget);
    expect(shownPath(tester), '/home/dev/forge');
    expect(find.text('cmd'), findsOneWidget);
    expect(find.text('docs'), findsOneWidget);

    await tester.tap(find.byTooltip('Parent directory'));
    await tester.pumpAndSettle();
    expect(shownPath(tester), '/home/dev');
    // Hidden directories are listed on request.
    expect(find.text('.config'), findsNothing);
    await tester.tap(find.byTooltip('Show hidden directories'));
    await tester.pump();
    expect(find.text('.config'), findsOneWidget);

    await tester.tap(find.text('notes'));
    await tester.pumpAndSettle();
    expect(
      find.descendant(
        of: find.byType(ForgeSheet),
        matching: find.text('no such directory'),
      ),
      findsOneWidget,
    );
    expect(shownPath(tester), '/home/dev', reason: 'a failure stays put');

    await tester.tap(find.text('forge'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('cmd'));
    await tester.pumpAndSettle();
    expect(shownPath(tester), '/home/dev/forge/cmd');
    expect(find.text('No directories inside'), findsOneWidget);

    await tester.tap(find.text('Use this directory'));
    await tester.pumpAndSettle();
    expect(chooser, findsNothing);
    expect(agent.of('set_cwd').single['cwd'], '/home/dev/forge/cmd');
    expect(tester.takeException(), isNull);
  });

  testWidgets('cancelling keeps the directory the session has', (tester) async {
    final agent = await connect(tester);
    agent.open('get_state');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    expect(chooser, findsNothing);
    expect(agent.of('set_cwd'), isEmpty);

    // The same session does not ask again, for example after a reconnect.
    agent.open('get_state');
    await tester.pumpAndSettle();
    expect(chooser, findsNothing);
  });

  testWidgets('choosing the directory it already has changes nothing', (
    tester,
  ) async {
    final agent = await connect(tester);
    agent.open('get_state');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Use this directory'));
    await tester.pumpAndSettle();
    expect(agent.of('set_cwd'), isEmpty);
  });

  testWidgets('a session with a conversation does not ask', (tester) async {
    final agent = await connect(tester);
    agent.open('get_state', empty: false);
    await tester.pumpAndSettle();
    expect(chooser, findsNothing);
    expect(agent.of('list_directories'), isEmpty);
  });

  testWidgets('a session the user switches to does not ask', (tester) async {
    final agent = await connect(tester);
    agent.open('get_state', empty: false);
    await tester.pumpAndSettle();
    agent.open('switch_session', session: 'session-2');
    await tester.pumpAndSettle();
    expect(chooser, findsNothing);
  });

  testWidgets('a new session is created in the chosen directory', (
    tester,
  ) async {
    final agent = await connect(tester);
    agent.open('get_state', empty: false);
    await tester.pumpAndSettle();

    await tester.tap(find.text('Sessions  Ctrl-B S'));
    await tester.pumpAndSettle();
    // Each session shows where it works.
    expect(find.text('/home/dev/forge'), findsOneWidget);
    expect(find.text('/home/dev/notes'), findsOneWidget);

    await tester.tap(find.text('New session'));
    await tester.pumpAndSettle();
    expect(find.text('Where should the new session work?'), findsOneWidget);
    expect(agent.of('new_session'), isEmpty, reason: 'not before choosing');

    await tester.tap(find.byTooltip('Home directory'));
    await tester.pumpAndSettle();
    expect(shownPath(tester), '/home/dev');
    await tester.tap(find.text('Use this directory'));
    await tester.pumpAndSettle();
    expect(agent.of('new_session').single['cwd'], '/home/dev');

    // The session that was just created does not ask again.
    agent.open('new_session', session: 'session-3', cwd: '/home/dev');
    await tester.pumpAndSettle();
    expect(chooser, findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('cancelling the chooser creates no session', (tester) async {
    final agent = await connect(tester);
    agent.open('get_state', empty: false);
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField).last, '/new');
    await tester.tap(find.byKey(const ValueKey('composer-send')));
    await tester.pumpAndSettle();
    expect(chooser, findsOneWidget);
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    expect(agent.of('new_session'), isEmpty);
  });

  testWidgets('the directory cannot be changed in the middle of a session', (
    tester,
  ) async {
    final agent = await connect(tester);
    agent.open('get_state', empty: false);
    await tester.pumpAndSettle();
    expect(find.byIcon(Icons.folder_outlined), findsNothing);

    await tester.enterText(find.byType(TextField).last, '/cwd /tmp');
    await tester.tap(find.byKey(const ValueKey('composer-send')));
    await tester.pump();
    expect(agent.of('set_cwd'), isEmpty);
    expect(agent.connection.status, 'unknown command: /cwd');
  });

  testWidgets('the header shows the directory after the upstream', (
    tester,
  ) async {
    final agent = await connect(tester);
    agent.open('get_state', empty: false);
    await tester.pumpAndSettle();

    final upstream = find.text('upstream —');
    final directory = find.byKey(const ValueKey('header-working-directory'));
    expect(upstream, findsOneWidget);
    expect(tester.widget<Text>(directory).data, 'cwd /home/dev/forge');
    // It follows the upstream in reading order, on the same line or the next.
    final after = tester.getTopLeft(directory);
    final before = tester.getTopRight(upstream);
    expect(after.dy > before.dy || after.dx > before.dx, isTrue);

    // A long path keeps its end, and its whole is in the tooltip.
    const long = '/home/dev/projects/clients/acme/services/billing-gateway';
    agent.open('switch_session', session: 'session-2', cwd: long, empty: false);
    await tester.pumpAndSettle();
    expect(
      tester.widget<Text>(directory).data,
      'cwd …/acme/services/billing-gateway',
    );
    expect(find.byTooltip(long), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('a disconnected app shows no directory', (tester) async {
    tester.view.devicePixelRatio = 1;
    tester.view.physicalSize = const Size(393, 852);
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: AgentConnection())),
    );
    expect(
      find.byKey(const ValueKey('header-working-directory')),
      findsNothing,
    );
  });

  for (final (model, upstream) in [
    ('claude/claude-sonnet-5', 'Claude'),
    ('agy/gemini-3.8-flash-low', 'Agy'),
    ('gpt-5.6-terra', '—'),
    ('my-provider/claude-like', '—'),
  ]) {
    testWidgets('the upstream of $model is $upstream', (tester) async {
      final agent = await connect(tester, size: const Size(393, 852));
      agent.open('get_state', empty: false, model: model);
      await tester.pumpAndSettle();
      expect(find.text('upstream $upstream'), findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  }

  test('a reported transport shows unless a command line provider runs', () {
    final connection = AgentConnection();
    void receive(Map<String, dynamic> value) =>
        connection.receive(jsonEncode(value));
    receive({
      'event': {'type': 'upstream_transport', 'upstreamTransport': 'WS'},
    });
    expect(connection.upstream, 'WS');
    receive({'model': 'claude/claude-sonnet-5'});
    expect(connection.upstream, 'Claude');
    receive({'model': 'gpt-5.6-terra'});
    expect(connection.upstream, 'WS');
  });

  testWidgets('returning to a session at start up asks only for that one', (
    tester,
  ) async {
    tester.view.devicePixelRatio = 1;
    tester.view.physicalSize = const Size(1100, 760);
    addTearDown(tester.view.reset);
    // The app remembers the session it was in.
    final connection = AgentConnection()..currentSession = 'session-2';
    final agent = FakeAgent(connection);
    await tester.pumpWidget(
      MaterialApp(home: AgentPage(connection: connection)),
    );
    await connection.attachTransport(agent);
    expect(agent.of('switch_session').single['session'], 'session-2');

    // The agent first sends the session it attaches every client to.
    agent.open('get_state', session: 'session-1');
    await tester.pumpAndSettle();
    expect(chooser, findsNothing);

    agent.open('switch_session', session: 'session-2', cwd: '/home/dev');
    await tester.pumpAndSettle();
    expect(find.text('Where should this session work?'), findsOneWidget);
    expect(shownPath(tester), '/home/dev');
  });
}
