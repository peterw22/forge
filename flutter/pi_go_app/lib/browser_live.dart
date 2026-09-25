import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';

class BrowserLiveController extends ChangeNotifier {
  BrowserLiveController(this.send);
  final void Function(Map<String, Object?>) send;
  bool open = false, controlled = false, watching = false;
  String session = '', instance = '', token = '', error = '';
  Map<String, dynamic>? frame;
  Uint8List? jpeg;
  bool _acceptControl = false;
  int _id = 0;
  final Map<String, Completer<void>> _pending = {};

  bool get canControl => open && watching && token.isNotEmpty;

  void setSession(String value) {
    if (session == value) return;
    reset();
    session = value;
  }

  void reset() {
    open = controlled = watching = _acceptControl = false;
    instance = token = error = '';
    frame = null;
    jpeg = null;
    for (final pending in _pending.values) {
      pending.completeError(StateError('Browser connection changed'));
    }
    _pending.clear();
    notifyListeners();
  }

  void receive(Map<String, dynamic> response) {
    if (response['browser'] case final Map<String, dynamic> state) {
      final next = '${state['instance'] ?? ''}';
      if (instance != next || state['open'] != true) {
        token = '';
        frame = null;
        jpeg = null;
        watching = false;
      }
      instance = next;
      open = state['open'] == true;
      controlled = state['controlled'] == true;
      if (!controlled) token = '';
    }
    if (response['browserFrame'] case final Map<String, dynamic> value) {
      if (!watching || !open || value['instance'] != instance) return;
      final sequence = (value['sequence'] as num?)?.toInt() ?? 0;
      if (sequence <= ((frame?['sequence'] as num?)?.toInt() ?? 0)) return;
      final data = value['jpeg'];
      if (data is! String || data.length > 1500000) return;
      try {
        jpeg = base64Decode(data);
        frame = value;
      } catch (_) {
        return;
      }
    }
    final message = response['error'];
    if (message is String && message.isNotEmpty) {
      error = message;
      // A stale frame or invalid input does not revoke the server lease.
      // Only authoritative browser state/reset/release clears the token.
    }
    if (response['controlToken'] case final String value
        when value.isNotEmpty) {
      if (watching && _acceptControl) token = value;
    }
    final pending = _pending.remove(response['id']);
    if (pending != null) {
      if (message is String && message.isNotEmpty) {
        pending.completeError(StateError(message));
      } else {
        pending.complete();
      }
    }
    notifyListeners();
  }

  Future<void> command(
    String type, [
    Map<String, Object?> args = const {},
  ]) async {
    final id = 'browser-live-${++_id}';
    final pending = Completer<void>();
    _pending[id] = pending;
    send({
      'id': id,
      'type': type,
      'session': session,
      'browserInstance': instance,
      ...args,
    });
    try {
      await pending.future.timeout(const Duration(seconds: 8));
    } finally {
      _pending.remove(id);
    }
  }

  Future<void> start() async {
    if (!open) return;
    watching = true;
    try {
      await command('browser_view_start');
    } catch (_) {
      watching = false;
      rethrow;
    }
  }

  void stop() {
    if (watching) {
      send({
        'type': 'browser_view_stop',
        'session': session,
        'browserInstance': instance,
      });
    }
    watching = false;
    _acceptControl = false;
    token = '';
    jpeg = null;
    frame = null;
    notifyListeners();
  }

  void acknowledge(Map<String, dynamic> metadata) {
    if (!watching || metadata['instance'] != instance) return;
    send({
      'type': 'browser_frame_ack',
      'session': session,
      'browserInstance': instance,
      'frameId': metadata['sequence'],
    });
  }

  Future<void> takeControl() {
    _acceptControl = true;
    return command('browser_control_acquire');
  }

  Future<void> release() async {
    _acceptControl = false;
    token = '';
    await command('browser_control_release');
  }

  Future<void> input(
    String action, [
    Map<String, Object?> args = const {},
  ]) async {
    if (!canControl || frame == null) {
      throw StateError('Take control and wait for a frame first');
    }
    await command('browser_input', {
      'controlToken': token,
      'frameId': frame!['sequence'],
      'browserAction': action,
      ...args,
    });
  }
}

/// Map image coordinates (after inverse zoom/pan) through the letterbox.
Offset? browserViewportPoint(Offset imagePoint, Map<String, dynamic> frame) {
  double n(String key) => (frame[key] as num?)?.toDouble() ?? 0;
  final x = imagePoint.dx - n('contentX');
  final y = imagePoint.dy - n('contentY');
  final w = n('contentWidth'), h = n('contentHeight');
  if (w <= 0 || h <= 0 || x < 0 || y < 0 || x >= w || y >= h) return null;
  return Offset(x * n('viewportWidth') / w, y * n('viewportHeight') / h);
}

class BrowserLiveView extends StatefulWidget {
  const BrowserLiveView({
    super.key,
    required this.controller,
    this.compact = false,
    this.onMinimize,
    this.onClose,
    this.onExpand,
  });
  final BrowserLiveController controller;
  final bool compact;
  final VoidCallback? onMinimize, onClose, onExpand;
  @override
  State<BrowserLiveView> createState() => _BrowserLiveViewState();
}

class _BrowserLiveViewState extends State<BrowserLiveView>
    with WidgetsBindingObserver {
  final transform = TransformationController();
  final text = TextEditingController();
  Timer? heartbeat;
  bool foreground = true, inputBusy = false, renewing = false;
  final Set<int> pointers = {};
  Offset? pressStart;
  bool moved = false;
  final surfaceKey = GlobalKey();
  String? fittedInstance;
  ui.Image? image;
  Map<String, dynamic>? displayedFrame;
  Uint8List? decodedBytes;
  bool decoding = false;

  BrowserLiveController get live => widget.controller;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    live.addListener(changed);
    unawaited(attempt(live.start));
    heartbeat = Timer.periodic(const Duration(seconds: 5), (_) async {
      if (!foreground || !live.open || renewing) return;
      renewing = true;
      try {
        await attempt(() async {
          await live.start();
          if (!widget.compact && live.canControl) await live.takeControl();
        });
      } finally {
        renewing = false;
      }
    });
  }

  @override
  void didUpdateWidget(BrowserLiveView oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (widget.compact && !oldWidget.compact) {
      text.clear();
      unawaited(attempt(live.release));
    }
  }

  void changed() {
    if (!mounted) return;
    if (live.jpeg == null) {
      image?.dispose();
      image = null;
      displayedFrame = null;
      decodedBytes = null;
    }
    setState(() {});
    unawaited(decodeLatest());
  }

  // Keep one decoded image and coalesce incoming frames while decoding.
  // RawImage avoids ImageCache retaining a distinct MemoryImage per JPEG.
  Future<void> decodeLatest() async {
    if (decoding || !mounted) return;
    decoding = true;
    try {
      while (mounted &&
          live.jpeg != null &&
          !identical(decodedBytes, live.jpeg)) {
        final bytes = live.jpeg!;
        final metadata = live.frame;
        ui.Codec? codec;
        try {
          codec = await ui.instantiateImageCodec(bytes);
          final decoded = await codec.getNextFrame();
          if (!mounted ||
              live.instance != metadata?['instance'] ||
              !live.watching) {
            decoded.image.dispose();
            break;
          }
          final old = image;
          setState(() {
            image = decoded.image;
            displayedFrame = metadata;
            decodedBytes = bytes;
          });
          // Let the render object adopt the new image before freeing the old.
          WidgetsBinding.instance.addPostFrameCallback((_) {
            old?.dispose();
            if (mounted && metadata != null) live.acknowledge(metadata);
          });
        } catch (error) {
          if (mounted) {
            live.error = 'Browser frame decode failed: $error';
            setState(() {});
          }
          if (metadata != null) live.acknowledge(metadata);
          decodedBytes = bytes;
          break;
        } finally {
          codec?.dispose();
        }
      }
    } finally {
      decoding = false;
    }
  }

  Future<void> attempt(Future<void> Function() action) async {
    try {
      await action();
    } catch (error) {
      if (mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(SnackBar(content: Text('$error')));
      }
    }
  }

  Future<void> input(
    String action, [
    Map<String, Object?> args = const {},
  ]) async {
    if (inputBusy || !live.canControl || displayedFrame == null) return;
    setState(() => inputBusy = true);
    // Validate against the frame actually displayed, not a newer undecoded JPEG.
    await attempt(
      () => live.command('browser_input', {
        'controlToken': live.token,
        'frameId': displayedFrame!['sequence'],
        'browserAction': action,
        ...args,
      }),
    );
    if (mounted) setState(() => inputBusy = false);
  }

  void click(Offset local, String button) {
    if (!live.canControl || displayedFrame == null || moved) return;
    final point = browserViewportPoint(
      transform.toScene(local),
      displayedFrame!,
    );
    if (point != null) {
      unawaited(
        input('click', {'x': point.dx, 'y': point.dy, 'button': button}),
      );
    }
  }

  Offset localPosition(Offset global) =>
      (surfaceKey.currentContext!.findRenderObject() as RenderBox)
          .globalToLocal(global);

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    foreground = state == AppLifecycleState.resumed;
    if (foreground) {
      unawaited(attempt(live.start));
    } else {
      live.stop();
    }
  }

  @override
  void dispose() {
    heartbeat?.cancel();
    WidgetsBinding.instance.removeObserver(this);
    live.removeListener(changed);
    live.stop();
    image?.dispose();
    transform.dispose();
    text.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    if (widget.compact) {
      return Material(
        elevation: 8,
        borderRadius: BorderRadius.circular(12),
        clipBehavior: Clip.antiAlias,
        child: Column(
          children: [
            SizedBox(
              height: 36,
              child: Row(
                children: [
                  const SizedBox(width: 8),
                  const Expanded(
                    child: Text(
                      'Browser · Live',
                      style: TextStyle(fontSize: 12),
                    ),
                  ),
                  IconButton(
                    padding: EdgeInsets.zero,
                    tooltip: 'Expand browser',
                    onPressed: widget.onExpand,
                    icon: const Icon(Icons.open_in_full, size: 18),
                  ),
                  IconButton(
                    padding: EdgeInsets.zero,
                    tooltip: 'Close browser preview',
                    onPressed: widget.onClose,
                    icon: const Icon(Icons.close, size: 18),
                  ),
                ],
              ),
            ),
            Expanded(
              child: GestureDetector(
                behavior: HitTestBehavior.opaque,
                onTap: widget.onExpand,
                child: SizedBox.expand(
                  child: image == null
                      ? const Center(
                          child: Text(
                            'Waiting for frame…',
                            style: TextStyle(fontSize: 12),
                          ),
                        )
                      : RawImage(image: image, fit: BoxFit.contain),
                ),
              ),
            ),
          ],
        ),
      );
    }
    final enabled = live.canControl && !inputBusy && displayedFrame != null;
    return Scaffold(
      appBar: AppBar(
        automaticallyImplyLeading: widget.onMinimize == null,
        leading: widget.onMinimize == null
            ? null
            : IconButton(
                tooltip: 'Minimize browser',
                onPressed: widget.onMinimize,
                icon: const Icon(Icons.picture_in_picture_alt),
              ),
        title: const Text('Browser Live View'),
        actions: [
          if (widget.onClose != null)
            IconButton(
              tooltip: 'Close browser view',
              onPressed: widget.onClose,
              icon: const Icon(Icons.close),
            ),
          TextButton(
            onPressed: live.open
                ? () =>
                      attempt(live.canControl ? live.release : live.takeControl)
                : null,
            child: Text(live.canControl ? 'Release control' : 'Take control'),
          ),
        ],
      ),
      body: SafeArea(
        child: Column(
          children: [
            Padding(
              padding: const EdgeInsets.all(8),
              child: Text(
                !live.open
                    ? 'Browser closed or disconnected'
                    : live.canControl
                    ? 'Controlling · tap to click · long press for right click'
                    : live.controlled
                    ? 'View only · another client has control'
                    : 'View only · 720p JPEG · up to 2 FPS',
              ),
            ),
            if (live.error.isNotEmpty)
              Padding(
                padding: const EdgeInsets.symmetric(horizontal: 8),
                child: Text(
                  live.error,
                  style: TextStyle(color: Theme.of(context).colorScheme.error),
                ),
              ),
            Expanded(
              child: LayoutBuilder(
                builder: (context, constraints) {
                  if (fittedInstance != live.instance &&
                      constraints.maxWidth > 0) {
                    fittedInstance = live.instance;
                    final fit = (constraints.maxWidth / 1280).clamp(0.1, 1.0);
                    transform.value = Matrix4.diagonal3Values(fit, fit, 1);
                  }
                  return Listener(
                    key: surfaceKey,
                    onPointerDown: (event) {
                      pointers.add(event.pointer);
                      if (pointers.length == 1) {
                        pressStart = event.localPosition;
                        moved = false;
                      } else {
                        moved = true;
                      }
                    },
                    onPointerMove: (event) {
                      if (pressStart != null &&
                          (event.localPosition - pressStart!).distance > 8) {
                        moved = true;
                      }
                    },
                    onPointerUp: (event) {
                      pointers.remove(event.pointer);
                    },
                    onPointerCancel: (event) {
                      pointers.remove(event.pointer);
                      moved = true;
                    },
                    child: GestureDetector(
                      behavior: HitTestBehavior.opaque,
                      onTapUp: (details) =>
                          click(localPosition(details.globalPosition), 'left'),
                      onLongPressStart: (details) =>
                          click(localPosition(details.globalPosition), 'right'),
                      onSecondaryTapUp: (details) =>
                          click(localPosition(details.globalPosition), 'right'),
                      child: InteractiveViewer(
                        transformationController: transform,
                        constrained: false,
                        alignment: Alignment.topLeft,
                        minScale: 0.1,
                        maxScale: 5,
                        child: SizedBox(
                          width: 1280,
                          height: 720,
                          child: image == null
                              ? const Center(
                                  child: Text('Waiting for browser frame…'),
                                )
                              : RawImage(
                                  image: image,
                                  width: 1280,
                                  height: 720,
                                  fit: BoxFit.fill,
                                ),
                        ),
                      ),
                    ),
                  );
                },
              ),
            ),
            const Text(
              'Pinch to zoom · drag to pan the image · arrows scroll the page',
              style: TextStyle(fontSize: 12),
            ),
            SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: Row(
                children: [
                  IconButton(
                    tooltip: 'Scroll page up',
                    onPressed: enabled
                        ? () => input('scroll', {'direction': 'up'})
                        : null,
                    icon: const Icon(Icons.keyboard_double_arrow_up),
                  ),
                  IconButton(
                    tooltip: 'Scroll page down',
                    onPressed: enabled
                        ? () => input('scroll', {'direction': 'down'})
                        : null,
                    icon: const Icon(Icons.keyboard_double_arrow_down),
                  ),
                  for (final key in [
                    'Enter',
                    'Tab',
                    'Backspace',
                    'Escape',
                    'ArrowLeft',
                    'ArrowRight',
                    'ControlOrMeta+A',
                  ])
                    TextButton(
                      onPressed: enabled
                          ? () => input('key', {'key': key})
                          : null,
                      child: Text(key),
                    ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.all(8),
              child: Row(
                children: [
                  Expanded(
                    child: TextField(
                      controller: text,
                      enabled: enabled,
                      autocorrect: false,
                      enableSuggestions: false,
                      decoration: const InputDecoration(
                        hintText: 'Click a browser field, then enter text',
                        border: OutlineInputBorder(),
                      ),
                      onSubmitted: (_) => sendText(),
                    ),
                  ),
                  IconButton(
                    tooltip: 'Type into browser',
                    onPressed: enabled ? sendText : null,
                    icon: const Icon(Icons.send),
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> sendText() async {
    if (!live.canControl || text.text.isEmpty || inputBusy) return;
    final value = text.text;
    text.clear();
    await input('type', {'text': value});
  }
}
