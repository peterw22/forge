import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/main.dart';

void main() {
  testWidgets('live browser tool result renders its screenshot', (tester) async {
    final connection = AgentConnection();
    const png =
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aV1sAAAAASUVORK5CYII=';
    connection.receive(jsonEncode({
      'event': {
        'type': 'tool_execution_start',
        'toolCallId': 'browser-1',
        'toolName': 'browser_screenshot',
        'arguments': <String, dynamic>{},
      },
    }));
    connection.receive(jsonEncode({
      'event': {
        'type': 'tool_execution_end',
        'toolCallId': 'browser-1',
        'toolName': 'browser_screenshot',
        'result': {
          'content': [
            {'type': 'text', 'text': 'Immediate snapshot.'},
            {'type': 'image', 'mimeType': 'image/png', 'data': png},
          ],
        },
      },
    }));
    expect(connection.messages.last.images, hasLength(1));
    expect(connection.messages.last.images.single, base64Decode(png));
    await tester.pumpWidget(MaterialApp(home: AgentPage(connection: connection)));
    await tester.pump();
    expect(find.byType(Image), findsOneWidget);
  });
}
