import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'browser_live.dart';

/// A single stable viewer element switches between full, split and PiP bounds.
/// Resizing never disposes it, restarts its stream, or changes browser viewport.
class AdaptiveBrowserWorkspace extends StatefulWidget {
  const AdaptiveBrowserWorkspace({
    super.key,
    required this.controller,
    required this.chat,
  });
  final BrowserLiveController controller;
  final Widget chat;
  @override
  State<AdaptiveBrowserWorkspace> createState() =>
      _AdaptiveBrowserWorkspaceState();
}

class _AdaptiveBrowserWorkspaceState extends State<AdaptiveBrowserWorkspace> {
  bool showing = false, minimized = false;
  bool? wasWide;
  bool narrowAfterResize = false;
  double split = 0.5;
  Offset pip = const Offset(1, 0); // normalized within available bounds
  String session = '';
  bool open = false;

  @override
  void initState() {
    super.initState();
    session = widget.controller.session;
    open = widget.controller.open;
    widget.controller.addListener(browserChanged);
  }

  @override
  void didUpdateWidget(AdaptiveBrowserWorkspace oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.controller != widget.controller) {
      oldWidget.controller.removeListener(browserChanged);
      widget.controller.addListener(browserChanged);
      showing = minimized = false;
      session = widget.controller.session;
      open = widget.controller.open;
    }
  }

  void browserChanged() {
    // Do not rebuild the chat tree on every JPEG frame.
    final live = widget.controller;
    if (open == live.open && session == live.session) return;
    setState(() {
      if (!live.open || session != live.session) showing = minimized = false;
      open = live.open;
      session = live.session;
    });
  }

  @override
  void dispose() {
    widget.controller.removeListener(browserChanged);
    super.dispose();
  }

  void minimize() {
    FocusManager.instance.primaryFocus?.unfocus();
    setState(() => minimized = true);
  }

  void expand() => setState(() {
    showing = true;
    minimized = false;
    narrowAfterResize = false;
  });

  @override
  Widget build(BuildContext context) => LayoutBuilder(
    builder: (context, bounds) {
      final wide = bounds.maxWidth >= 1100;
      // Derived responsive state is updated without another frame/setState.
      // A wide->narrow transition becomes a preview rather than covering chat.
      if (wasWide != wide) {
        if (wasWide == true && !wide && showing) narrowAfterResize = true;
        if (wide) narrowAfterResize = false;
        wasWide = wide;
      }
      final preview = showing && (minimized || (!wide && narrowAfterResize));
      final side = showing && wide && !minimized;
      final full = showing && !side && !preview;
      final width = bounds.maxWidth, height = bounds.maxHeight;
      final left = side ? (width * split).clamp(420.0, width - 420.0) : width;
      final pw = math.min(280.0, math.max(0.0, width - 24));
      final ph = math.min(pw * 9 / 16 + 36, math.max(0.0, height - 24));
      // Main Scaffold already subtracts the keyboard inset. Reserve a composer
      // strip too; very short windows clamp the PiP back into available space.
      final bottomReserve = math.min(180.0, math.max(0.0, height - ph - 24));
      final dx = math.max(0.0, width - pw - 24);
      final dy = math.max(0.0, height - ph - bottomReserve - 24);
      final x = 12 + pip.dx.clamp(0.0, 1.0) * dx;
      final y = 12 + pip.dy.clamp(0.0, 1.0) * dy;
      return PopScope(
        canPop: !full,
        onPopInvokedWithResult: (didPop, result) {
          if (!didPop && full) minimize();
        },
        child: Stack(
          children: [
            Positioned(
              left: 0,
              top: 0,
              bottom: 0,
              width: left,
              child: IgnorePointer(ignoring: full, child: widget.chat),
            ),
            if (side)
              Positioned(
                left: left - 4,
                top: 0,
                bottom: 0,
                width: 8,
                child: MouseRegion(
                  cursor: SystemMouseCursors.resizeColumn,
                  child: GestureDetector(
                    key: const ValueKey('browser-split-divider'),
                    behavior: HitTestBehavior.opaque,
                    onHorizontalDragUpdate: (details) => setState(
                      () => split = ((left + details.delta.dx) / width).clamp(
                        0.38,
                        0.62,
                      ),
                    ),
                    child: ColoredBox(color: Theme.of(context).dividerColor),
                  ),
                ),
              ),
            // Same key, element type and ancestry in all modes: subscription and
            // zoom state survive a breakpoint crossing or a PiP/full transition.
            if (showing)
              Positioned(
                key: const ValueKey('browser-view-position'),
                left: preview
                    ? x
                    : side
                    ? left + 4
                    : 0,
                top: preview ? y : 0,
                width: preview
                    ? pw
                    : side
                    ? width - left - 4
                    : width,
                height: preview ? ph : height,
                child: GestureDetector(
                  behavior: HitTestBehavior.deferToChild,
                  onPanUpdate: preview
                      ? (details) => setState(() {
                          pip = Offset(
                            dx == 0
                                ? 0
                                : (pip.dx + details.delta.dx / dx).clamp(0, 1),
                            dy == 0
                                ? 0
                                : (pip.dy + details.delta.dy / dy).clamp(0, 1),
                          );
                        })
                      : null,
                  onPanEnd: preview
                      ? (_) => setState(
                          () => pip = Offset(pip.dx < 0.5 ? 0 : 1, pip.dy),
                        )
                      : null,
                  child: BrowserLiveView(
                    key: ObjectKey(widget.controller),
                    controller: widget.controller,
                    compact: preview,
                    onMinimize: minimize,
                    onExpand: expand,
                    onClose: () => setState(() {
                      showing = minimized = false;
                    }),
                  ),
                ),
              ),
            if (open && !showing)
              Positioned(
                right: 16,
                bottom: math.min(200.0, math.max(16.0, height - 64)),
                child: FloatingActionButton.small(
                  heroTag: 'browser-live-view',
                  tooltip: 'Browser live view',
                  onPressed: expand,
                  child: const Icon(Icons.web),
                ),
              ),
          ],
        ),
      );
    },
  );
}
