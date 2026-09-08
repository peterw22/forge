import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:pi_go_app/forge_fonts.dart';

void main() {
  test('controller starts with the platform font and no eager fallback', () {
    final controller = ForgeFontController();
    addTearDown(controller.dispose);

    final base = ThemeData.light().textTheme;
    expect(controller.loadedFallbacks, isEmpty);
    expect(
      controller.apply(base).bodyMedium?.fontFamily,
      base.bodyMedium?.fontFamily,
    );
  });

  test('Latin text does not request a downloaded font', () {
    expect(
      forgeFontFamiliesForText('Hello, Forge 123', const Locale('en')),
      isEmpty,
    );
  });

  test('Chinese font is requested only when Han text appears', () {
    expect(forgeFontFamiliesForText('Hello 世界', const Locale('zh', 'CN')), {
      'Noto Sans SC',
    });
    expect(forgeFontFamiliesForText('繁體中文', const Locale('zh', 'TW')), {
      'Noto Sans TC',
    });
  });

  test('Kana and Hangul select one locale font instead of Chinese too', () {
    expect(forgeFontFamiliesForText('日本語の漢字', const Locale('en')), {
      'Noto Sans JP',
    });
    expect(forgeFontFamiliesForText('한국어 漢字', const Locale('en')), {
      'Noto Sans KR',
    });
  });

  test('Han-only text respects Japanese and Korean UI locales', () {
    expect(forgeFontFamiliesForText('漢字', const Locale('ja')), {
      'Noto Sans JP',
    });
    expect(forgeFontFamiliesForText('漢字', const Locale('ko')), {
      'Noto Sans KR',
    });
  });

  test('other writing systems map to their Noto families', () {
    expect(
      forgeFontFamiliesForText(
        'مرحبا שלום ไทย हिन्दी বাংলা',
        const Locale('en'),
      ),
      containsAll({
        'Noto Sans Arabic',
        'Noto Sans Hebrew',
        'Noto Sans Thai',
        'Noto Sans Devanagari',
        'Noto Sans Bengali',
      }),
    );
  });
}
