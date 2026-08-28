import 'dart:async';
import 'dart:ui';

import 'package:flutter/material.dart';
import 'package:google_fonts/google_fonts.dart';

import 'runtime_environment.dart';

class ForgeFontController extends ChangeNotifier {
  ForgeFontController() {
    GoogleFonts.config.allowRuntimeFetching = runtimeFontFetchingEnabled;
    _family = _familyForLocales(PlatformDispatcher.instance.locales);
    if (runtimeFontFetchingEnabled) {
      unawaited(_loadFamily(_family));
    }
  }

  String _family = 'Noto Sans';
  String get family => _family;

  TextTheme apply(TextTheme base) {
    if (!runtimeFontFetchingEnabled) {
      return base.apply(fontFamily: _family);
    }
    try {
      return GoogleFonts.getTextTheme(_family, base);
    } catch (_) {
      return base.apply(fontFamily: _family);
    }
  }

  Future<void> ensureLocale(Locale locale) async {
    final family = _familyForLocales([locale]);
    if (family == _family) return;
    _family = family;
    if (!runtimeFontFetchingEnabled) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (hasListeners) notifyListeners();
      });
      return;
    }
    await _loadFamily(family);
  }

  Future<void> _loadFamily(String family) async {
    try {
      final requests = <TextStyle>[
        for (final weight in const [
          FontWeight.w400,
          FontWeight.w500,
          FontWeight.w600,
          FontWeight.w700,
        ])
          GoogleFonts.getFont(family, fontWeight: weight),
      ];
      await GoogleFonts.pendingFonts(requests);
    } catch (error) {
      debugPrint('Forge Google Font load failed for $family: $error');
    }
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (hasListeners) notifyListeners();
    });
  }
}

String _familyForLocales(List<Locale> locales) {
  final locale = locales.isEmpty ? const Locale('en') : locales.first;
  final language = locale.languageCode.toLowerCase();
  final country = (locale.countryCode ?? '').toUpperCase();
  final script = (locale.scriptCode ?? '').toLowerCase();
  return switch (language) {
    'zh' when script == 'hant' || {'TW', 'HK', 'MO'}.contains(country) =>
      'Noto Sans TC',
    'zh' => 'Noto Sans SC',
    'ja' => 'Noto Sans JP',
    'ko' => 'Noto Sans KR',
    'ar' || 'fa' || 'ur' => 'Noto Sans Arabic',
    'he' || 'yi' => 'Noto Sans Hebrew',
    'th' => 'Noto Sans Thai',
    'hi' || 'mr' || 'ne' => 'Noto Sans Devanagari',
    'bn' => 'Noto Sans Bengali',
    'gu' => 'Noto Sans Gujarati',
    'pa' => 'Noto Sans Gurmukhi',
    'ta' => 'Noto Sans Tamil',
    'te' => 'Noto Sans Telugu',
    'ml' => 'Noto Sans Malayalam',
    'si' => 'Noto Sans Sinhala',
    'my' => 'Noto Sans Myanmar',
    _ => 'Noto Sans',
  };
}
