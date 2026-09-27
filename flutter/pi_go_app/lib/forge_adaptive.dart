import 'package:flutter/cupertino.dart';
import 'package:flutter/material.dart';

import 'forge_theme.dart';

/// Whether a [ForgeSheet] is shown as a bottom sheet rather than a dialog.
class _ForgeSheetScope extends InheritedWidget {
  const _ForgeSheetScope({required this.sheet, required super.child});
  final bool sheet;

  static bool sheetOf(BuildContext context) =>
      context.getInheritedWidgetOfExactType<_ForgeSheetScope>()?.sheet ?? false;

  @override
  bool updateShouldNotify(_ForgeSheetScope oldWidget) =>
      sheet != oldWidget.sheet;
}

/// Presents a [ForgeSheet] the way the platform presents secondary content:
/// a bottom sheet on a phone, a centred dialog in a window or on a tablet.
Future<T?> showForgeSheet<T>({
  required BuildContext context,
  required WidgetBuilder builder,
  bool barrierDismissible = true,
}) {
  final style = ForgeStyle.of(context);
  final size = MediaQuery.sizeOf(context);
  if (style.touch && size.width < 600) {
    return showModalBottomSheet<T>(
      context: context,
      isScrollControlled: true,
      useSafeArea: true,
      useRootNavigator: true,
      isDismissible: barrierDismissible,
      enableDrag: barrierDismissible,
      showDragHandle: barrierDismissible,
      builder: (context) =>
          _ForgeSheetScope(sheet: true, child: Builder(builder: builder)),
    );
  }
  return showDialog<T>(
    context: context,
    barrierDismissible: barrierDismissible,
    builder: (context) =>
        _ForgeSheetScope(sheet: false, child: Builder(builder: builder)),
  );
}

/// Title, scrolling body and actions of secondary content such as the session
/// list or provider settings.
class ForgeSheet extends StatelessWidget {
  const ForgeSheet({
    super.key,
    required this.title,
    required this.child,
    this.icon,
    this.titleActions = const [],
    this.actions = const [],
    this.width = 620,
    this.height,
    this.padded = true,
  });

  final String title;
  final IconData? icon;
  final List<Widget> titleActions;
  final Widget child;
  final List<Widget> actions;

  /// Widest the content grows in a dialog.
  final double width;

  /// Preferred body height for lists that load more rows while open.
  final double? height;

  /// Lists draw their rows edge to edge and pass false.
  final bool padded;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final style = ForgeStyle.of(context);
    final sheet = _ForgeSheetScope.sheetOf(context);
    final gutter = sheet ? 20.0 : 24.0;
    final centered = sheet && style.apple;

    final heading = Text(
      title,
      maxLines: 2,
      overflow: TextOverflow.ellipsis,
      textAlign: centered ? TextAlign.center : TextAlign.start,
      style:
          (centered ? theme.textTheme.titleMedium : theme.textTheme.titleLarge)
              ?.copyWith(color: theme.colorScheme.onSurface),
    );
    final header = Padding(
      padding: EdgeInsets.fromLTRB(gutter, sheet ? 2 : 20, gutter - 8, 12),
      child: Row(
        children: [
          if (icon != null && !centered) ...[
            Icon(icon, size: 22, color: theme.colorScheme.onSurfaceVariant),
            const SizedBox(width: 10),
          ],
          // Balance the trailing actions so a centred title stays centred.
          if (centered)
            for (final _ in titleActions) const SizedBox(width: 40),
          Expanded(child: heading),
          ...titleActions,
          const SizedBox(width: 8),
        ],
      ),
    );

    Widget body = child;
    if (height != null) body = SizedBox(height: height, child: body);
    if (padded) {
      body = Padding(
        padding: EdgeInsets.symmetric(horizontal: gutter),
        child: body,
      );
    }
    final content = Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        header,
        Flexible(
          child: DefaultTextStyle.merge(
            style: theme.textTheme.bodyMedium?.copyWith(
              color: theme.colorScheme.onSurface,
            ),
            child: body,
          ),
        ),
        if (actions.isEmpty)
          SizedBox(height: sheet ? 12 : 20)
        else
          Padding(
            padding: EdgeInsets.fromLTRB(gutter - 8, 14, gutter - 8, 16),
            child: OverflowBar(
              alignment: MainAxisAlignment.end,
              overflowAlignment: OverflowBarAlignment.end,
              spacing: 8,
              overflowSpacing: 8,
              children: actions,
            ),
          ),
      ],
    );

    if (sheet) {
      return Padding(
        // The sheet stays above the keyboard while a field is being edited.
        padding: EdgeInsets.only(
          bottom: MediaQuery.viewInsetsOf(context).bottom,
        ),
        child: SafeArea(top: false, child: content),
      );
    }
    return Dialog(
      clipBehavior: Clip.antiAlias,
      insetPadding: EdgeInsets.symmetric(
        horizontal: style.touch ? 24 : 16,
        vertical: 24,
      ),
      child: ConstrainedBox(
        constraints: BoxConstraints(maxWidth: width),
        child: content,
      ),
    );
  }
}

class ForgeAlertAction<T> {
  const ForgeAlertAction(
    this.label,
    this.value, {
    this.primary = false,
    this.destructive = false,
  });
  final String label;
  final T value;

  /// The action the alert recommends.
  final bool primary;
  final bool destructive;
}

/// Asks a short question with the platform's own alert.
Future<T?> showForgeAlert<T>({
  required BuildContext context,
  required String title,
  required List<Widget> content,
  required List<ForgeAlertAction<T>> actions,
  bool barrierDismissible = true,
}) {
  final apple = ForgeStyle.of(context).apple;
  return showDialog<T>(
    context: context,
    barrierDismissible: barrierDismissible,
    builder: (context) {
      final scheme = Theme.of(context).colorScheme;
      final body = Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: apple
            ? CrossAxisAlignment.center
            : CrossAxisAlignment.start,
        children: content,
      );
      if (apple) {
        return CupertinoAlertDialog(
          title: Text(title),
          content: Padding(padding: const EdgeInsets.only(top: 6), child: body),
          actions: [
            for (final action in actions)
              CupertinoDialogAction(
                isDefaultAction: action.primary && !action.destructive,
                isDestructiveAction: action.destructive,
                onPressed: () => Navigator.pop(context, action.value),
                child: Text(action.label),
              ),
          ],
        );
      }
      return AlertDialog(
        title: Text(title),
        content: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 480),
          child: SingleChildScrollView(child: body),
        ),
        actions: [
          for (final action in actions)
            if (action.primary || action.destructive)
              FilledButton(
                onPressed: () => Navigator.pop(context, action.value),
                style: action.destructive
                    ? FilledButton.styleFrom(
                        backgroundColor: scheme.error,
                        foregroundColor: scheme.onError,
                      )
                    : null,
                child: Text(action.label),
              )
            else
              TextButton(
                onPressed: () => Navigator.pop(context, action.value),
                child: Text(action.label),
              ),
        ],
      );
    },
  );
}

/// One row of a single-choice list. Apple platforms mark the current value
/// with a trailing checkmark, the others with a leading radio button.
class ForgeChoiceTile extends StatelessWidget {
  const ForgeChoiceTile({
    super.key,
    required this.title,
    required this.selected,
    required this.onTap,
    this.subtitle,
  });

  final String title;
  final String? subtitle;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final apple = ForgeStyle.of(context).apple;
    final scheme = Theme.of(context).colorScheme;
    return ListTile(
      selected: selected,
      leading: apple
          ? null
          : Icon(
              selected
                  ? Icons.radio_button_checked
                  : Icons.radio_button_unchecked,
            ),
      trailing: apple && selected
          ? Icon(Icons.check_rounded, color: scheme.primary)
          : null,
      title: Text(title),
      subtitle: subtitle == null ? null : Text(subtitle!),
      onTap: onTap,
    );
  }
}

/// A labelled block of text the user is expected to copy, such as a
/// fingerprint or a shell command.
class ForgeCopyableValue extends StatelessWidget {
  const ForgeCopyableValue({super.key, required this.value, this.label});

  final String? label;
  final String value;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final style = ForgeStyle.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        if (label != null) ...[
          Text(
            label!,
            style: theme.textTheme.labelMedium?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 4),
        ],
        Container(
          width: double.infinity,
          padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 8),
          decoration: BoxDecoration(
            color: theme.colorScheme.surfaceContainer,
            borderRadius: BorderRadius.circular(style.codeRadius),
          ),
          child: SelectableText(
            value,
            style: style.mono(color: theme.colorScheme.onSurface),
          ),
        ),
      ],
    );
  }
}
