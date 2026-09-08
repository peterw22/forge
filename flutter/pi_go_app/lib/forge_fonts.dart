import 'dart:async';
import 'dart:ui';

import 'package:flutter/material.dart';
import 'package:google_fonts/google_fonts.dart';

import 'runtime_environment.dart';

/// Loads large script fonts only after matching text is actually displayed.
///
/// Latin, Greek, and Cyrillic continue to use the platform UI font. In
/// particular, this avoids eagerly loading four Noto weights at startup (over
/// 40 MiB for a CJK family) and lets Linux/Flatpak use its normal system font.
class ForgeFontController extends ChangeNotifier {
  ForgeFontController() {
    GoogleFonts.config.allowRuntimeFetching = runtimeFontFetchingEnabled;
  }

  final Set<String> _requestedFamilies = <String>{};
  final List<String> _loadedFallbacks = <String>[];
  bool _disposed = false;

  @visibleForTesting
  List<String> get loadedFallbacks => List.unmodifiable(_loadedFallbacks);

  TextTheme apply(TextTheme base) => _loadedFallbacks.isEmpty
      ? base
      : base.apply(fontFamilyFallback: List.unmodifiable(_loadedFallbacks));

  /// Examines [text] without retaining it and schedules at most one regular
  /// font per newly encountered writing system.
  void observeText(String text, {Locale? locale}) {
    if (_disposed || text.isEmpty || !runtimeFontFetchingEnabled) return;
    final families = forgeFontFamiliesForText(
      text,
      locale ?? PlatformDispatcher.instance.locale,
    );
    for (final family in families) {
      if (_requestedFamilies.add(family)) unawaited(_loadFamily(family));
    }
  }

  Future<void> _loadFamily(String family) async {
    try {
      // One regular face is enough as a glyph fallback. Flutter synthesizes
      // weight where needed, avoiding three additional 10+ MiB CJK faces.
      final style = GoogleFonts.getFont(family, fontWeight: FontWeight.w400);
      final fallback = style.fontFamily;
      await GoogleFonts.pendingFonts([style]);
      if (_disposed ||
          fallback == null ||
          _loadedFallbacks.contains(fallback)) {
        return;
      }
      _loadedFallbacks.add(fallback);
      notifyListeners();
    } catch (error) {
      // Keep the system fallback and do not retry on every rebuild. Repeated
      // failed downloads previously caused request/future churn in Flatpak.
      debugPrint('Forge lazy font load failed for $family: $error');
    }
  }

  @override
  void dispose() {
    _disposed = true;
    super.dispose();
  }
}

class ForgeFontScope extends InheritedWidget {
  const ForgeFontScope({
    super.key,
    required this.controller,
    required super.child,
  });

  final ForgeFontController controller;

  static ForgeFontController of(BuildContext context) {
    final scope = context.getInheritedWidgetOfExactType<ForgeFontScope>();
    assert(scope != null, 'ForgeFontScope is missing');
    return scope!.controller;
  }

  static ForgeFontController? maybeOf(BuildContext context) =>
      context.getInheritedWidgetOfExactType<ForgeFontScope>()?.controller;

  @override
  bool updateShouldNotify(ForgeFontScope oldWidget) =>
      controller != oldWidget.controller;
}

@visibleForTesting
Set<String> forgeFontFamiliesForText(String text, Locale locale) {
  var han = false;
  var japanese = false;
  var korean = false;
  final families = <String>{};

  for (final rune in text.runes) {
    if (_inRanges(rune, const [
      (0x3040, 0x30ff), // Hiragana and Katakana
      (0x31f0, 0x31ff),
    ])) {
      japanese = true;
    } else if (_inRanges(rune, const [
      (0x1100, 0x11ff),
      (0x3130, 0x318f),
      (0xa960, 0xa97f),
      (0xac00, 0xd7ff),
    ])) {
      korean = true;
    } else if (_inRanges(rune, const [
      (0x3400, 0x4dbf),
      (0x4e00, 0x9fff),
      (0xf900, 0xfaff),
      (0x20000, 0x323af),
    ])) {
      han = true;
    } else if (_inRanges(rune, const [
      (0x0600, 0x06ff),
      (0x0750, 0x077f),
      (0x08a0, 0x08ff),
      (0xfb50, 0xfdff),
      (0xfe70, 0xfeff),
    ])) {
      families.add('Noto Sans Arabic');
    } else if (_inRanges(rune, const [(0x0590, 0x05ff)])) {
      families.add('Noto Sans Hebrew');
    } else if (_inRanges(rune, const [(0x0e00, 0x0e7f)])) {
      families.add('Noto Sans Thai');
    } else if (_inRanges(rune, const [(0x0900, 0x097f)])) {
      families.add('Noto Sans Devanagari');
    } else if (_inRanges(rune, const [(0x0980, 0x09ff)])) {
      families.add('Noto Sans Bengali');
    } else if (_inRanges(rune, const [(0x0a00, 0x0a7f)])) {
      families.add('Noto Sans Gurmukhi');
    } else if (_inRanges(rune, const [(0x0a80, 0x0aff)])) {
      families.add('Noto Sans Gujarati');
    } else if (_inRanges(rune, const [(0x0b80, 0x0bff)])) {
      families.add('Noto Sans Tamil');
    } else if (_inRanges(rune, const [(0x0c00, 0x0c7f)])) {
      families.add('Noto Sans Telugu');
    } else if (_inRanges(rune, const [(0x0d00, 0x0d7f)])) {
      families.add('Noto Sans Malayalam');
    } else if (_inRanges(rune, const [(0x0d80, 0x0dff)])) {
      families.add('Noto Sans Sinhala');
    } else if (_inRanges(rune, const [
      (0x1000, 0x109f),
      (0xaa60, 0xaa7f),
      (0xa9e0, 0xa9ff),
    ])) {
      families.add('Noto Sans Myanmar');
    }
  }

  // Kana/Hangul fonts include the Han glyphs expected for those scripts, so
  // mixed Japanese/Korean text must not also load a Chinese family.
  if (japanese) {
    families.add('Noto Sans JP');
  } else if (korean) {
    families.add('Noto Sans KR');
  } else if (han) {
    final language = locale.languageCode.toLowerCase();
    final country = (locale.countryCode ?? '').toUpperCase();
    final script = (locale.scriptCode ?? '').toLowerCase();
    if (language == 'ja') {
      families.add('Noto Sans JP');
    } else if (language == 'ko') {
      families.add('Noto Sans KR');
    } else if (script == 'hant' || {'TW', 'HK', 'MO'}.contains(country)) {
      families.add('Noto Sans TC');
    } else {
      families.add('Noto Sans SC');
    }
  }
  return families;
}

bool _inRanges(int rune, List<(int, int)> ranges) {
  for (final (start, end) in ranges) {
    if (rune >= start && rune <= end) return true;
  }
  return false;
}
