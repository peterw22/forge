import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';

/// The design language Forge follows on the current platform.
enum ForgeFamily {
  /// iOS and macOS: neutral surfaces, hairlines, no ink, system alerts.
  apple,

  /// Android: Material 3 tonal surfaces, ink and large shapes.
  material,

  /// Linux and Windows: flat neutral surfaces and small shapes.
  desktop,
}

/// Breakpoint shared by the header, composer and approval sheet.
const double forgeCompactWidth = 720;

/// Transcript and composer never grow wider than a readable column.
const double forgeContentWidth = 860;

/// Platform-dependent measurements. They are derived from the ambient
/// [ThemeData], so they are also correct under a plain `MaterialApp`.
@immutable
class ForgeStyle {
  const ForgeStyle._(this.platform, this.scheme);

  factory ForgeStyle.of(BuildContext context) {
    final theme = Theme.of(context);
    return ForgeStyle._(theme.platform, theme.colorScheme);
  }

  const factory ForgeStyle.forPlatform(
    TargetPlatform platform,
    ColorScheme scheme,
  ) = ForgeStyle._;

  final TargetPlatform platform;
  final ColorScheme scheme;

  ForgeFamily get family => switch (platform) {
    TargetPlatform.iOS || TargetPlatform.macOS => ForgeFamily.apple,
    TargetPlatform.android || TargetPlatform.fuchsia => ForgeFamily.material,
    TargetPlatform.linux || TargetPlatform.windows => ForgeFamily.desktop,
  };

  bool get apple => family == ForgeFamily.apple;

  /// Finger-driven platforms need larger targets and text than pointer ones.
  bool get touch =>
      platform == TargetPlatform.iOS ||
      platform == TargetPlatform.android ||
      platform == TargetPlatform.fuchsia;

  bool get dark => scheme.brightness == Brightness.dark;

  /// Buttons, text fields and menu rows.
  double get controlRadius => switch (family) {
    ForgeFamily.apple => touch ? 12 : 7,
    ForgeFamily.material => 20,
    ForgeFamily.desktop => 8,
  };

  /// Tool panels and grouped content.
  double get cardRadius => switch (family) {
    ForgeFamily.apple => touch ? 14 : 10,
    ForgeFamily.material => 16,
    ForgeFamily.desktop => 10,
  };

  /// Chat bubbles and the composer.
  double get bubbleRadius => switch (family) {
    ForgeFamily.apple => touch ? 20 : 14,
    ForgeFamily.material => 24,
    ForgeFamily.desktop => 12,
  };

  double get dialogRadius => switch (family) {
    ForgeFamily.apple => touch ? 14 : 12,
    ForgeFamily.material => 28,
    ForgeFamily.desktop => 12,
  };

  double get sheetRadius => switch (family) {
    ForgeFamily.apple => 14,
    ForgeFamily.material => 28,
    ForgeFamily.desktop => 12,
  };

  double get codeRadius => family == ForgeFamily.material ? 12 : 8;

  double get hairline => apple ? .5 : 1;

  /// Body text of the transcript.
  double get bodySize => touch ? 16 : 14;

  double get codeSize => touch ? 13 : 12.5;

  /// Height of toolbar buttons and square icon buttons.
  double get controlHeight => touch ? 40 : 32;

  double get toolbarIconSize => touch ? 22 : 18;

  /// A fixed-pitch face that exists on the platform. `monospace` alone falls
  /// back to the proportional system font on Apple platforms.
  TextStyle mono({double? size, Color? color, double height = 1.45}) =>
      TextStyle(
        fontFamily: apple ? 'Menlo' : 'monospace',
        fontFamilyFallback: const [
          'SF Mono',
          'Menlo',
          'Roboto Mono',
          'DejaVu Sans Mono',
          'Consolas',
          'monospace',
        ],
        fontSize: size ?? codeSize,
        height: height,
        color: color,
      );

  Color get hairlineColor => scheme.outlineVariant;

  /// Fill of tool panels, thinking blocks and the composer.
  Color get panelColor => scheme.surfaceContainerLow;

  Color get userBubbleColor => switch (family) {
    ForgeFamily.apple =>
      dark
          ? Color.alphaBlend(
              scheme.primary.withValues(alpha: .22),
              scheme.surfaceContainerHigh,
            )
          : Color.alphaBlend(
              scheme.primary.withValues(alpha: .10),
              scheme.surfaceContainerLow,
            ),
    ForgeFamily.material => scheme.primaryContainer,
    ForgeFamily.desktop => scheme.surfaceContainerHigh,
  };

  Color get onUserBubbleColor => family == ForgeFamily.material
      ? scheme.onPrimaryContainer
      : scheme.onSurface;

  Color get successColor =>
      dark ? const Color(0xff5fd38d) : const Color(0xff1f8a4c);

  Color get warningColor =>
      dark ? const Color(0xffffc857) : const Color(0xffb26a00);

  List<BoxShadow> get floatingShadow => [
    BoxShadow(
      color: Colors.black.withValues(alpha: dark ? .32 : .08),
      blurRadius: family == ForgeFamily.desktop ? 10 : 18,
      offset: const Offset(0, 4),
    ),
  ];
}

ColorScheme _forgeColorScheme(Brightness brightness, TargetPlatform platform) {
  final light = brightness == Brightness.light;
  final seeded = ColorScheme.fromSeed(
    seedColor: light ? const Color(0xff25765f) : const Color(0xff62d6a7),
    brightness: brightness,
  );
  final family = ForgeStyle.forPlatform(platform, seeded).family;
  // Android keeps the tonal Material surfaces. Apple and desktop platforms use
  // neutral greys and reserve the brand colour for accents.
  return switch ((family, light)) {
    (ForgeFamily.material, _) => seeded,
    (ForgeFamily.apple, true) => seeded.copyWith(
      surface: const Color(0xffffffff),
      surfaceDim: const Color(0xffe5e5ea),
      surfaceBright: const Color(0xffffffff),
      surfaceContainerLowest: const Color(0xffffffff),
      surfaceContainerLow: const Color(0xfff7f7f9),
      surfaceContainer: const Color(0xfff2f2f5),
      surfaceContainerHigh: const Color(0xffebebef),
      surfaceContainerHighest: const Color(0xffe4e4e9),
      onSurface: const Color(0xff1c1c1e),
      onSurfaceVariant: const Color(0xff66666b),
      outline: const Color(0xffaeaeb4),
      outlineVariant: const Color(0xffdcdce1),
      surfaceTint: Colors.transparent,
    ),
    (ForgeFamily.apple, false) => seeded.copyWith(
      surface: const Color(0xff161618),
      surfaceDim: const Color(0xff0f0f10),
      surfaceBright: const Color(0xff3a3a3c),
      surfaceContainerLowest: const Color(0xff0f0f10),
      surfaceContainerLow: const Color(0xff1d1d20),
      surfaceContainer: const Color(0xff232326),
      surfaceContainerHigh: const Color(0xff2c2c2f),
      surfaceContainerHighest: const Color(0xff38383b),
      onSurface: const Color(0xfff2f2f7),
      onSurfaceVariant: const Color(0xffa0a0a7),
      outline: const Color(0xff636368),
      outlineVariant: const Color(0xff39393d),
      surfaceTint: Colors.transparent,
    ),
    (ForgeFamily.desktop, true) => seeded.copyWith(
      surface: const Color(0xfffafafa),
      surfaceDim: const Color(0xffdedede),
      surfaceBright: const Color(0xffffffff),
      surfaceContainerLowest: const Color(0xffffffff),
      surfaceContainerLow: const Color(0xfff3f3f3),
      surfaceContainer: const Color(0xffededed),
      surfaceContainerHigh: const Color(0xffe6e6e6),
      surfaceContainerHighest: const Color(0xffdedede),
      onSurface: const Color(0xff1e1e1e),
      onSurfaceVariant: const Color(0xff5e5e5e),
      outline: const Color(0xffa6a6a6),
      outlineVariant: const Color(0xffd7d7d7),
      surfaceTint: Colors.transparent,
    ),
    (ForgeFamily.desktop, false) => seeded.copyWith(
      surface: const Color(0xff242424),
      surfaceDim: const Color(0xff1a1a1a),
      surfaceBright: const Color(0xff454545),
      surfaceContainerLowest: const Color(0xff1e1e1e),
      surfaceContainerLow: const Color(0xff2b2b2b),
      surfaceContainer: const Color(0xff303030),
      surfaceContainerHigh: const Color(0xff383838),
      surfaceContainerHighest: const Color(0xff424242),
      onSurface: const Color(0xfff2f2f2),
      onSurfaceVariant: const Color(0xffb0b0b0),
      outline: const Color(0xff6e6e6e),
      outlineVariant: const Color(0xff454545),
      surfaceTint: Colors.transparent,
    ),
  };
}

/// Builds the Forge theme for [platform]. [fonts] adds lazily loaded script
/// fallbacks to the finished text theme.
ThemeData forgeTheme({
  required Brightness brightness,
  TargetPlatform? platform,
  TextTheme Function(TextTheme base)? fonts,
}) {
  final base = ThemeData(brightness: brightness, platform: platform);
  final scheme = _forgeColorScheme(brightness, base.platform);
  final style = ForgeStyle.forPlatform(base.platform, scheme);
  final touch = style.touch;
  final apple = style.apple;
  final material = style.family == ForgeFamily.material;

  final control = RoundedRectangleBorder(
    borderRadius: BorderRadius.circular(style.controlRadius),
  );
  final OutlinedBorder buttonShape = material ? const StadiumBorder() : control;
  final buttonHeight = touch ? 44.0 : 32.0;
  final buttonPadding = EdgeInsets.symmetric(horizontal: touch ? 18 : 12);

  var textTheme = ThemeData(
    colorScheme: scheme,
    platform: base.platform,
    useMaterial3: true,
  ).textTheme;
  if (!material) {
    // Material's display sizes and wide tracking look foreign next to native
    // Apple and desktop text.
    textTheme = textTheme.copyWith(
      headlineSmall: textTheme.headlineSmall?.copyWith(
        fontSize: touch ? 22 : 18,
        fontWeight: FontWeight.w700,
        letterSpacing: -.2,
      ),
      titleLarge: textTheme.titleLarge?.copyWith(
        fontSize: touch ? 20 : 17,
        fontWeight: FontWeight.w700,
        letterSpacing: -.2,
      ),
      titleMedium: textTheme.titleMedium?.copyWith(
        fontSize: touch ? 17 : 14,
        fontWeight: FontWeight.w600,
        letterSpacing: -.1,
      ),
      titleSmall: textTheme.titleSmall?.copyWith(
        fontSize: touch ? 15 : 13,
        fontWeight: FontWeight.w600,
        letterSpacing: 0,
      ),
      bodyLarge: textTheme.bodyLarge?.copyWith(
        fontSize: touch ? 17 : 14,
        letterSpacing: touch ? -.2 : 0,
      ),
      bodyMedium: textTheme.bodyMedium?.copyWith(
        fontSize: touch ? 15 : 13,
        letterSpacing: 0,
      ),
      bodySmall: textTheme.bodySmall?.copyWith(
        fontSize: touch ? 13 : 12,
        letterSpacing: 0,
      ),
      labelLarge: textTheme.labelLarge?.copyWith(
        fontSize: touch ? 15 : 13,
        fontWeight: FontWeight.w600,
        letterSpacing: 0,
      ),
      labelMedium: textTheme.labelMedium?.copyWith(
        fontSize: touch ? 13 : 12,
        letterSpacing: 0,
      ),
      labelSmall: textTheme.labelSmall?.copyWith(
        fontSize: touch ? 12 : 11,
        letterSpacing: 0,
      ),
    );
  }
  if (fonts != null) textTheme = fonts(textTheme);

  final buttonText = WidgetStatePropertyAll(textTheme.labelLarge);
  // An outlined field keeps its floating label in the border's notch; a
  // filled one would draw the label across the fill's rounded edge.
  final fieldBorder = OutlineInputBorder(
    borderRadius: BorderRadius.circular(material ? 12 : style.controlRadius),
    borderSide: BorderSide(color: scheme.outlineVariant),
  );

  return ThemeData(
    useMaterial3: true,
    platform: base.platform,
    colorScheme: scheme,
    textTheme: textTheme,
    scaffoldBackgroundColor: scheme.surface,
    visualDensity: touch ? VisualDensity.standard : VisualDensity.compact,
    materialTapTargetSize: touch
        ? MaterialTapTargetSize.padded
        : MaterialTapTargetSize.shrinkWrap,
    splashFactory: material ? InkSparkle.splashFactory : NoSplash.splashFactory,
    highlightColor: material ? null : scheme.onSurface.withValues(alpha: .06),
    hoverColor: scheme.onSurface.withValues(alpha: .05),
    cupertinoOverrideTheme: CupertinoThemeData(
      brightness: brightness,
      primaryColor: scheme.primary,
      scaffoldBackgroundColor: scheme.surface,
      barBackgroundColor: scheme.surface,
    ),
    pageTransitionsTheme: const PageTransitionsTheme(
      builders: {
        TargetPlatform.android: PredictiveBackPageTransitionsBuilder(),
        TargetPlatform.iOS: CupertinoPageTransitionsBuilder(),
        TargetPlatform.macOS: FadeForwardsPageTransitionsBuilder(),
        TargetPlatform.linux: FadeForwardsPageTransitionsBuilder(),
        TargetPlatform.windows: FadeForwardsPageTransitionsBuilder(),
      },
    ),
    dividerTheme: DividerThemeData(
      color: scheme.outlineVariant,
      thickness: style.hairline,
      space: 1,
    ),
    appBarTheme: AppBarTheme(
      backgroundColor: scheme.surface,
      surfaceTintColor: Colors.transparent,
      scrolledUnderElevation: 0,
      elevation: 0,
      titleTextStyle: textTheme.titleMedium?.copyWith(color: scheme.onSurface),
      toolbarHeight: touch ? 52 : 44,
      shape: Border(
        bottom: BorderSide(color: scheme.outlineVariant, width: style.hairline),
      ),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        shape: buttonShape,
        minimumSize: Size(0, buttonHeight),
        padding: buttonPadding,
      ).copyWith(textStyle: buttonText),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        shape: buttonShape,
        minimumSize: Size(0, buttonHeight),
        padding: buttonPadding,
        foregroundColor: material ? null : scheme.onSurface,
        backgroundColor: material ? null : scheme.surfaceContainerLowest,
        side: BorderSide(color: scheme.outlineVariant),
      ).copyWith(textStyle: buttonText),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(
        shape: buttonShape,
        minimumSize: Size(0, buttonHeight),
        padding: EdgeInsets.symmetric(horizontal: touch ? 14 : 10),
      ).copyWith(textStyle: buttonText),
    ),
    iconButtonTheme: IconButtonThemeData(
      style: IconButton.styleFrom(
        shape: material ? const CircleBorder() : control,
      ),
    ),
    segmentedButtonTheme: SegmentedButtonThemeData(
      style: ButtonStyle(
        shape: WidgetStatePropertyAll(buttonShape),
        visualDensity: touch ? VisualDensity.standard : VisualDensity.compact,
        textStyle: WidgetStatePropertyAll(textTheme.labelLarge),
        side: WidgetStatePropertyAll(BorderSide(color: scheme.outlineVariant)),
        backgroundColor: material
            ? null
            : WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.selected)
                    ? scheme.primary.withValues(alpha: style.dark ? .30 : .14)
                    : scheme.surfaceContainerLowest,
              ),
        foregroundColor: material
            ? null
            : WidgetStateProperty.resolveWith(
                (states) => states.contains(WidgetState.disabled)
                    ? scheme.onSurface.withValues(alpha: .38)
                    : scheme.onSurface,
              ),
      ),
    ),
    inputDecorationTheme: InputDecorationThemeData(
      isDense: !touch,
      border: fieldBorder,
      enabledBorder: fieldBorder,
      disabledBorder: fieldBorder.copyWith(
        borderSide: BorderSide(
          color: scheme.outlineVariant.withValues(alpha: .6),
        ),
      ),
      focusedBorder: fieldBorder.copyWith(
        borderSide: BorderSide(color: scheme.primary, width: 1.5),
      ),
      errorBorder: fieldBorder.copyWith(
        borderSide: BorderSide(color: scheme.error),
      ),
      focusedErrorBorder: fieldBorder.copyWith(
        borderSide: BorderSide(color: scheme.error, width: 1.5),
      ),
      labelStyle: TextStyle(color: scheme.onSurfaceVariant),
      hintStyle: TextStyle(color: scheme.onSurfaceVariant),
      helperMaxLines: 3,
    ),
    cardTheme: CardThemeData(
      elevation: 0,
      margin: const EdgeInsets.symmetric(vertical: 4),
      color: scheme.surfaceContainerLow,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(style.cardRadius),
        side: BorderSide(color: scheme.outlineVariant, width: style.hairline),
      ),
    ),
    listTileTheme: ListTileThemeData(
      shape: control,
      iconColor: scheme.onSurfaceVariant,
      selectedColor: scheme.primary,
      selectedTileColor: scheme.primary.withValues(alpha: .08),
      subtitleTextStyle: textTheme.bodySmall?.copyWith(
        color: scheme.onSurfaceVariant,
      ),
    ),
    chipTheme: ChipThemeData(
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(material ? 8 : style.controlRadius),
      ),
      side: BorderSide(color: scheme.outlineVariant),
      backgroundColor: scheme.surfaceContainerLow,
      labelStyle: textTheme.labelMedium?.copyWith(color: scheme.onSurface),
    ),
    dialogTheme: DialogThemeData(
      backgroundColor: material
          ? scheme.surfaceContainerHigh
          : scheme.surfaceContainerLowest,
      surfaceTintColor: Colors.transparent,
      elevation: material ? 3 : 12,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(style.dialogRadius),
        side: material
            ? BorderSide.none
            : BorderSide(color: scheme.outlineVariant, width: style.hairline),
      ),
      titleTextStyle: textTheme.titleLarge?.copyWith(color: scheme.onSurface),
      contentTextStyle: textTheme.bodyMedium?.copyWith(
        color: scheme.onSurfaceVariant,
      ),
    ),
    bottomSheetTheme: BottomSheetThemeData(
      backgroundColor: material
          ? scheme.surfaceContainerLow
          : scheme.surfaceContainerLowest,
      surfaceTintColor: Colors.transparent,
      showDragHandle: true,
      dragHandleColor: scheme.onSurfaceVariant.withValues(alpha: .4),
      dragHandleSize: apple ? const Size(36, 5) : const Size(32, 4),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(
          top: Radius.circular(style.sheetRadius),
        ),
      ),
    ),
    popupMenuTheme: PopupMenuThemeData(
      color: material ? scheme.surfaceContainer : scheme.surfaceContainerLowest,
      surfaceTintColor: Colors.transparent,
      elevation: material ? 3 : 10,
      menuPadding: EdgeInsets.symmetric(vertical: touch ? 8 : 5),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(material ? 16 : style.cardRadius),
        side: material
            ? BorderSide.none
            : BorderSide(color: scheme.outlineVariant, width: style.hairline),
      ),
      textStyle: textTheme.bodyMedium?.copyWith(color: scheme.onSurface),
      labelTextStyle: WidgetStateProperty.resolveWith(
        (states) => textTheme.bodyMedium?.copyWith(
          color: states.contains(WidgetState.disabled)
              ? scheme.onSurface.withValues(alpha: .38)
              : scheme.onSurface,
        ),
      ),
      iconColor: scheme.onSurfaceVariant,
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      width: touch ? null : 520,
      insetPadding: touch ? const EdgeInsets.fromLTRB(12, 0, 12, 12) : null,
      backgroundColor: scheme.inverseSurface,
      contentTextStyle: textTheme.bodyMedium?.copyWith(
        color: scheme.onInverseSurface,
      ),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(style.cardRadius),
      ),
    ),
    tooltipTheme: TooltipThemeData(
      waitDuration: touch ? Duration.zero : const Duration(milliseconds: 450),
      textStyle: textTheme.labelSmall?.copyWith(color: scheme.onInverseSurface),
      decoration: BoxDecoration(
        color: scheme.inverseSurface.withValues(alpha: .94),
        borderRadius: BorderRadius.circular(6),
      ),
    ),
    badgeTheme: BadgeThemeData(
      backgroundColor: scheme.primary,
      textColor: scheme.onPrimary,
    ),
    floatingActionButtonTheme: FloatingActionButtonThemeData(
      elevation: 2,
      focusElevation: 2,
      hoverElevation: 3,
      highlightElevation: 2,
      backgroundColor: scheme.surfaceContainerLowest,
      foregroundColor: scheme.onSurface,
      shape: CircleBorder(side: BorderSide(color: scheme.outlineVariant)),
    ),
    scrollbarTheme: ScrollbarThemeData(
      radius: const Radius.circular(8),
      thickness: WidgetStatePropertyAll(touch ? 4 : 7),
      crossAxisMargin: 2,
    ),
    progressIndicatorTheme: ProgressIndicatorThemeData(
      linearTrackColor: scheme.outlineVariant,
      linearMinHeight: 3,
    ),
  );
}
