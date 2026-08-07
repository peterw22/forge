import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pi_go_app/main.dart';

void main() {
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
    expect(find.textContaining('upstream —'), findsOneWidget);
    expect(find.text('Unix'), findsOneWidget);
    expect(find.text('TCP'), findsOneWidget);
    final connections = tester.widget<SegmentedButton<ConnectionKind>>(
      find.byType(SegmentedButton<ConnectionKind>),
    );
    expect(connections.selected, {ConnectionKind.websocket});
    expect(connections.segments.first.enabled, isFalse);
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

  testWidgets('keeps session and connection controls visible on mobile', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 844));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(const PiGoApp());
    expect(find.byIcon(Icons.tune), findsOneWidget);
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
}
