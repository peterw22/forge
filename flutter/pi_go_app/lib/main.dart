import 'dart:async';
import 'dart:convert';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_markdown_plus/flutter_markdown_plus.dart';
import 'package:image_picker/image_picker.dart';
import 'package:super_clipboard/super_clipboard.dart';

import 'clipboard_images.dart';

import 'transport.dart';

void main() => runApp(const PiGoApp());

class PiGoApp extends StatelessWidget {
  const PiGoApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      debugShowCheckedModeBanner: false,
      title: 'Pi Go',
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xff25765f),
          brightness: Brightness.light,
        ),
        useMaterial3: true,
      ),
      darkTheme: ThemeData(
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xff62d6a7),
          brightness: Brightness.dark,
        ),
        useMaterial3: true,
      ),
      themeMode: ThemeMode.system,
      home: const AgentPage(),
    );
  }
}

enum ConnectionKind { unix, tcp, websocket }

class CommandSuggestion {
  const CommandSuggestion(this.value, this.description);
  final String value, description;
}

class SessionItem {
  SessionItem({
    required this.id,
    required this.name,
    required this.preview,
    required this.lastMessageTime,
  });
  final String id, name, preview;
  final DateTime? lastMessageTime;
}

class ApprovalRequest {
  ApprovalRequest(
    this.id,
    this.toolCallID,
    this.tool,
    this.description,
    this.reason,
  );
  final String id, toolCallID, tool, description, reason;
}

class PendingImage {
  PendingImage(this.name, this.mimeType, this.bytes);
  final String name, mimeType;
  final Uint8List bytes;
}

class TranscriptItem {
  TranscriptItem(
    this.role,
    this.text, {
    this.id,
    this.error = false,
    this.collapsed = false,
    this.toolName,
    this.toolArguments,
    this.images = const [],
    this.safetyStatus,
    this.safetyMessage,
  });
  final String role;
  final String? id;
  String text;
  bool error;
  bool collapsed;
  final String? toolName;
  final dynamic toolArguments;
  final List<Uint8List> images;
  String? safetyStatus, safetyMessage;
}

class AgentConnection extends ChangeNotifier {
  AgentTransport? _transport;
  StreamSubscription<String>? _lines;
  final List<TranscriptItem> messages = [];
  String status = 'disconnected';
  bool streaming = false;
  ApprovalRequest? pendingApproval;
  int input = 0;
  int cacheRead = 0;
  int output = 0;
  int total = 0;
  int contextUsed = 0;
  String upstreamTransport = '—';
  String currentModel = 'gpt-5.6-terra';
  String currentThinking = 'high';
  String currentCWD = '';
  String? currentSession;
  Completer<List<SessionItem>>? _sessionRequest;

  bool get connected => _transport != null;

  Future<void> connect(ConnectionKind kind, String address) async {
    await disconnect();
    status = 'connecting';
    notifyListeners();
    try {
      final transport = switch (kind) {
        ConnectionKind.unix => await connectUnixTransport(address),
        ConnectionKind.tcp => await connectTcpTransport(address),
        ConnectionKind.websocket => await connectWebSocketTransport(address),
      };
      await _attach(transport);
    } catch (error) {
      status = 'connection failed: $error';
      notifyListeners();
    }
  }

  Future<void> _attach(AgentTransport transport) async {
    final sessionToRestore = currentSession;
    _transport = transport;
    status = 'connected';
    _lines = transport.messages.listen(
      _receive,
      onError: _closed,
      onDone: _closed,
    );
    notifyListeners();
    if (sessionToRestore != null && sessionToRestore.isNotEmpty) {
      send({
        'id': 'flutter-reconnect-session',
        'type': 'switch_session',
        'session': sessionToRestore,
      });
    } else {
      send({'id': 'flutter-state', 'type': 'get_state'});
    }
  }

  void _closed([Object? error]) {
    _transport = null;
    streaming = false;
    pendingApproval = null;
    status = error == null ? 'disconnected' : 'connection lost: $error';
    notifyListeners();
  }

  Future<void> disconnect() async {
    final subscription = _lines;
    _lines = null;
    await subscription?.cancel();
    final transport = _transport;
    _transport = null;
    await transport?.close();
    streaming = false;
    pendingApproval = null;
    status = 'disconnected';
    notifyListeners();
  }

  void send(Map<String, Object?> command) {
    final transport = _transport;
    if (transport == null) return;
    transport.send(jsonEncode(command));
  }

  void prompt(String text, List<PendingImage> images) {
    if (!connected || streaming || (text.trim().isEmpty && images.isEmpty)) {
      return;
    }
    send({
      'id': 'flutter-${DateTime.now().microsecondsSinceEpoch}',
      'type': 'prompt',
      'message': text.trim(),
      'content': images
          .map(
            (image) => {
              'type': 'image',
              'mimeType': image.mimeType,
              'data': base64Encode(image.bytes),
            },
          )
          .toList(),
    });
  }

  void abort() => send({'id': 'flutter-abort', 'type': 'abort'});

  void resolveApproval(String id, bool approved) {
    if (pendingApproval?.id == id) pendingApproval = null;
    send({
      'id': 'flutter-approval-${DateTime.now().microsecondsSinceEpoch}',
      'type': 'approval_response',
      'approvalId': id,
      'approved': approved,
    });
    status = approved ? 'operation manually approved' : 'operation rejected';
    notifyListeners();
  }

  void compact(String instructions) {
    if (!connected || streaming) return;
    streaming = true;
    status = 'compacting context…';
    send({
      'id': 'flutter-compact-${DateTime.now().microsecondsSinceEpoch}',
      'type': 'compact',
      'customInstructions': instructions.trim(),
    });
    notifyListeners();
  }

  void setModel(String model) {
    currentModel = model;
    status = 'model: $model';
    send({'id': 'flutter-model', 'type': 'set_model', 'model': model});
    notifyListeners();
  }

  void setThinking(String level) {
    currentThinking = level;
    status = 'thinking: $level';
    send({
      'id': 'flutter-thinking',
      'type': 'set_thinking_level',
      'level': level,
    });
    notifyListeners();
  }

  void setWorkingDirectory(String cwd) {
    status = 'changing workspace…';
    send({'id': 'flutter-cwd', 'type': 'set_cwd', 'cwd': cwd});
    notifyListeners();
  }

  void setSessionName(String? name) {
    send({'id': 'flutter-name', 'type': 'set_session_name', 'name': name});
    status = name == null ? 'session name cleared' : 'session name: $name';
    notifyListeners();
  }

  void clearTranscript() {
    messages.clear();
    status = 'transcript cleared; conversation context retained';
    notifyListeners();
  }

  void setLocalStatus(String value) {
    status = value;
    notifyListeners();
  }

  Future<List<SessionItem>> listSessions() {
    final request = Completer<List<SessionItem>>();
    _sessionRequest = request;
    send({'id': 'flutter-sessions', 'type': 'list_sessions'});
    return request.future.timeout(const Duration(seconds: 5));
  }

  void newSession() {
    status = 'creating session…';
    notifyListeners();
    send({'id': 'flutter-new-session', 'type': 'new_session'});
  }

  void switchSession(String id) {
    status = 'switching session…';
    notifyListeners();
    send({
      'id': 'flutter-switch-session',
      'type': 'switch_session',
      'session': id,
    });
  }

  void _receive(String line) {
    try {
      final response = jsonDecode(line) as Map<String, dynamic>;
      if (response['error'] case final String error when error.isNotEmpty) {
        status = error;
        if (response['command'] == 'compact') streaming = false;
        if (response['command'] == 'list_sessions' && _sessionRequest != null) {
          _sessionRequest?.completeError(error);
          _sessionRequest = null;
        }
      }
      if (response['command'] == 'list_sessions' &&
          response['error'] == null &&
          _sessionRequest != null) {
        final sessions = <SessionItem>[];
        for (final value in (response['sessions'] ?? const [])) {
          if (value is! Map<String, dynamic>) continue;
          sessions.add(
            SessionItem(
              id: '${value['id'] ?? ''}',
              name: '${value['name'] ?? ''}',
              preview: '${value['preview'] ?? ''}',
              lastMessageTime: DateTime.tryParse(
                '${value['lastMessageTime'] ?? ''}',
              ),
            ),
          );
        }
        _sessionRequest?.complete(sessions);
        _sessionRequest = null;
      }
      if (response['session'] case final String session
          when session.isNotEmpty) {
        currentSession = session;
      }
      if (response['model'] case final String model when model.isNotEmpty) {
        currentModel = model;
      }
      if (response['thinking'] case final String thinking
          when thinking.isNotEmpty) {
        currentThinking = thinking;
      }
      if (response['cwd'] case final String cwd when cwd.isNotEmpty) {
        currentCWD = cwd;
        if (response['command'] == 'set_cwd') status = 'workspace: $cwd';
      }
      if (response['state'] case final Map<String, dynamic> state) {
        _restore(state);
        if (response['command'] == 'switch_session') {
          status = 'session switched';
        }
        if (response['command'] == 'new_session') {
          status = 'new session created';
        }
      }
      if (response['event'] case final Map<String, dynamic> event) {
        _event(event);
      }
    } catch (error) {
      status = 'invalid agent response: $error';
    }
    notifyListeners();
  }

  void _restore(Map<String, dynamic> state) {
    messages.clear();
    streaming = (state['Streaming'] ?? state['streaming'] ?? false) == true;
    final stored = state['Messages'] ?? state['messages'] ?? const [];
    final toolLabels = <String, String>{};
    final toolCalls = <String, Map<String, dynamic>>{};
    for (final value in stored) {
      if (value is! Map<String, dynamic> ||
          value['role'] != 'assistant' ||
          value['content'] is! List) {
        continue;
      }
      for (final block in value['content'] as List) {
        if (block is Map<String, dynamic> && block['type'] == 'toolCall') {
          final id = '${block['id'] ?? ''}';
          toolLabels[id] = _toolLabel(
            '${block['name'] ?? ''}',
            block['arguments'],
          );
          toolCalls[id] = block;
        }
      }
    }
    for (final value in stored) {
      if (value is! Map<String, dynamic>) continue;
      final role = '${value['role'] ?? ''}';
      final text = _contentText(value['content']);
      final images = _contentImages(value['content']);
      if (text.isNotEmpty || images.isNotEmpty) {
        final callID = '${value['toolCallId'] ?? ''}';
        final call = toolCalls[callID];
        final label = role == 'toolResult'
            ? (toolLabels[callID] ?? '${value['toolName'] ?? 'tool'}')
            : value['toolName'];
        final callName = call == null ? null : call['name'];
        final toolName = role == 'toolResult'
            ? '${callName ?? value['toolName'] ?? 'tool'}'
            : null;
        final arguments = role == 'toolResult' && call != null
            ? call['arguments']
            : null;
        messages.add(
          TranscriptItem(
            _roleLabel(role, label),
            role == 'toolResult'
                ? _toolMarkdown(toolName!, arguments, text)
                : text,
            error: value['isError'] == true,
            collapsed: role == 'toolResult' || role == 'compactionSummary',
            toolName: toolName,
            toolArguments: arguments,
            images: images,
          ),
        );
      }
    }
    final usage = state['Usage'] ?? state['usage'];
    if (usage is Map<String, dynamic>) _setUsage(usage, cumulative: true);
    final transport = state['UpstreamTransport'] ?? state['upstreamTransport'];
    upstreamTransport = transport is String && transport.isNotEmpty
        ? transport
        : '—';
    final compactedContext = state['ContextTokens'] ?? state['contextTokens'];
    if (compactedContext is int && compactedContext > 0) {
      contextUsed = compactedContext;
    }
  }

  void _event(Map<String, dynamic> event) {
    switch (event['type']) {
      case 'upstream_transport':
        final transport = event['upstreamTransport'];
        if (transport is String && transport.isNotEmpty) {
          upstreamTransport = transport;
        }
      case 'approval_required':
        pendingApproval = ApprovalRequest(
          '${event['approvalId'] ?? ''}',
          '${event['toolCallId'] ?? ''}',
          '${event['toolName'] ?? 'operation'}',
          '${event['description'] ?? ''}',
          '${event['reason'] ?? 'Bash Safety requires manual approval.'}',
        );
        status = 'waiting for safety approval';
      case 'compaction_start':
        streaming = true;
        status = 'compacting context…';
      case 'compaction_end':
        streaming = false;
        status = event['error'] == null || '${event['error']}'.isEmpty
            ? 'context compacted'
            : '${event['error']}';
      case 'agent_start':
        streaming = true;
        status = 'streaming';
      case 'agent_end':
        streaming = false;
        status = event['error'] == null ? 'idle' : '${event['error']}';
      case 'message_start':
        final message = event['message'];
        if (message is! Map<String, dynamic>) return;
        final role = '${message['role'] ?? ''}';
        if (role == 'user') {
          messages.add(
            TranscriptItem(
              'You',
              _contentText(message['content']),
              images: _contentImages(message['content']),
            ),
          );
        } else if (role == 'assistant') {
          messages.add(TranscriptItem('Assistant', ''));
        }
      case 'message_update':
        final message = event['message'];
        if (message is Map<String, dynamic> && message['role'] == 'assistant') {
          _replaceLastAssistant(_contentText(message['content']));
        }
      case 'message_end':
        final message = event['message'];
        if (message is Map<String, dynamic> && message['role'] == 'assistant') {
          _replaceLastAssistant(_contentText(message['content']));
          final usage = event['usage'];
          if (usage is Map<String, dynamic>) _setUsage(usage);
        }
      case 'tool_execution_start':
        messages.add(
          TranscriptItem(
            'Tool · ${_toolLabel('${event['toolName'] ?? ''}', event['arguments'])}',
            _toolMarkdown('${event['toolName'] ?? ''}', event['arguments'], ''),
            id: '${event['toolCallId'] ?? ''}',
            collapsed: true,
            toolName: '${event['toolName'] ?? ''}',
            toolArguments: event['arguments'],
          ),
        );
        status = '${event['toolName']} running · Abort kills the process';
      case 'tool_safety_update':
        _updateToolSafety(event);
        if (pendingApproval?.toolCallID == '${event['toolCallId'] ?? ''}') {
          pendingApproval = null;
        }
      case 'tool_execution_update':
        _updateTool(event, false);
      case 'tool_execution_end':
        _updateTool(event, event['isError'] == true);
        status = event['isError'] == true
            ? '${event['toolName']} failed gracefully'
            : 'streaming';
    }
  }

  String _toolMarkdown(String name, dynamic arguments, String output) {
    final bash = name == 'bash';
    final input = bash && arguments is Map
        ? '${arguments['command'] ?? ''}'
        : const JsonEncoder.withIndent('  ').convert(arguments ?? {});
    final result = output.isEmpty ? '(waiting for output…)' : output;
    return '**${bash ? 'Command' : 'Input'}**\n\n${_codeBlock(bash ? 'bash' : 'json', input)}\n\n**${bash ? 'Output' : 'Result'}**\n\n${_codeBlock('text', result)}';
  }

  String _codeBlock(String language, String value) {
    var longest = 0;
    for (final match in RegExp(r'`+').allMatches(value)) {
      if (match.group(0)!.length > longest) longest = match.group(0)!.length;
    }
    final fence = List.filled(longest < 3 ? 3 : longest + 1, '`').join();
    return '$fence$language\n$value\n$fence';
  }

  String _toolLabel(String name, dynamic arguments) {
    if (name == 'bash' && arguments is Map) {
      final description = '${arguments['description'] ?? ''}'.trim();
      if (description.isNotEmpty) return 'bash · $description';
    }
    if ((name == 'read' || name == 'write' || name == 'edit') &&
        arguments is Map) {
      final path = '${arguments['path'] ?? ''}';
      if (path.isNotEmpty) return '$name · $path';
    }
    return name;
  }

  void _updateToolSafety(Map<String, dynamic> event) {
    final id = '${event['toolCallId'] ?? ''}';
    for (var index = messages.length - 1; index >= 0; index--) {
      if (messages[index].id == id) {
        messages[index].safetyStatus = '${event['safetyStatus'] ?? ''}';
        messages[index].safetyMessage = '${event['safetyMessage'] ?? ''}';
        return;
      }
    }
  }

  void _updateTool(Map<String, dynamic> event, bool failed) {
    final id = '${event['toolCallId'] ?? ''}';
    final result = event['result'];
    if (result is! Map<String, dynamic>) return;
    final text = _contentText(result['content']);
    for (var index = messages.length - 1; index >= 0; index--) {
      if (messages[index].id == id) {
        if (text.isNotEmpty) {
          messages[index].text = _toolMarkdown(
            messages[index].toolName ?? '${event['toolName'] ?? 'tool'}',
            messages[index].toolArguments,
            text,
          );
        }
        messages[index].error = failed || result['isError'] == true;
        return;
      }
    }
  }

  void _replaceLastAssistant(String text) {
    for (var index = messages.length - 1; index >= 0; index--) {
      if (messages[index].role == 'Assistant') {
        messages[index].text = text;
        return;
      }
    }
    messages.add(TranscriptItem('Assistant', text));
  }

  void _setUsage(Map<String, dynamic> usage, {bool cumulative = false}) {
    int number(String lower, String upper) =>
        (usage[lower] ?? usage[upper] ?? 0) as int;
    if (cumulative) {
      input = number('input', 'Input');
      cacheRead = number('cacheRead', 'CacheRead');
      output = number('output', 'Output');
      total = number('totalTokens', 'TotalTokens');
      contextUsed = input;
    } else {
      final turnInput = number('input', 'Input');
      input += turnInput;
      cacheRead += number('cacheRead', 'CacheRead');
      output += number('output', 'Output');
      final turnTotal = number('totalTokens', 'TotalTokens');
      total += turnTotal;
      contextUsed = turnTotal > 0 ? turnTotal : turnInput;
    }
  }

  List<Uint8List> _contentImages(dynamic content) {
    if (content is! List) return const [];
    final images = <Uint8List>[];
    for (final block in content.whereType<Map<String, dynamic>>()) {
      if (block['type'] != 'image' || block['data'] is! String) continue;
      try {
        images.add(base64Decode(block['data'] as String));
      } catch (_) {
        /* Ignore corrupt persisted previews. */
      }
    }
    return images;
  }

  String _contentText(dynamic content) {
    if (content is String) return content;
    if (content is! List) return '';
    return content
        .whereType<Map<String, dynamic>>()
        .where((block) => block['type'] == 'text')
        .map((block) => '${block['text'] ?? ''}')
        .where((text) => text.isNotEmpty)
        .join('\n');
  }

  String _roleLabel(String role, dynamic toolName) => switch (role) {
    'user' => 'You',
    'assistant' => 'Assistant',
    'toolResult' => 'Tool · ${toolName ?? 'result'}',
    'compactionSummary' => 'Compaction summary',
    _ => role,
  };

  @override
  void dispose() {
    _lines?.cancel();
    _transport?.close();
    super.dispose();
  }
}

class AgentPage extends StatefulWidget {
  const AgentPage({super.key});
  @override
  State<AgentPage> createState() => _AgentPageState();
}

class _AgentPageState extends State<AgentPage> {
  static const models = [
    'gpt-5.3-codex-spark',
    'gpt-5.5',
    'gpt-5.6-luna',
    'gpt-5.6-sol',
    'gpt-5.6-terra',
  ];
  static const thinkingLevels = [
    'off',
    'minimal',
    'low',
    'medium',
    'high',
    'xhigh',
    'max',
  ];
  final agent = AgentConnection();
  final prompt = TextEditingController();
  final address = TextEditingController();
  final scroll = ScrollController();
  final promptFocus = FocusNode();
  final List<PendingImage> attachments = [];
  late ConnectionKind connectionKind;
  String model = 'gpt-5.6-terra';
  String thinking = 'high';
  bool controlBChord = false;
  bool showConnectionSettings = true;
  bool wasConnected = false;
  String? shownApprovalID;
  bool approvalDialogOpen = false;

  @override
  void initState() {
    super.initState();
    connectionKind = webSocketOnlyClient
        ? ConnectionKind.websocket
        : nativeSocketsSupported
        ? ConnectionKind.unix
        : ConnectionKind.websocket;
    address.text = _defaultAddress(connectionKind);
    agent.addListener(_changed);
    prompt.addListener(_promptChanged);
    HardwareKeyboard.instance.addHandler(_handleKey);
    if (kIsWeb) {
      ClipboardEvents.instance?.registerPasteEventListener(_handleWebPaste);
    }
  }

  bool _handleKey(KeyEvent event) {
    if (event is! KeyDownEvent) return false;
    if (HardwareKeyboard.instance.isControlPressed &&
        event.logicalKey == LogicalKeyboardKey.keyB) {
      controlBChord = true;
      return true;
    }
    if (controlBChord) {
      controlBChord = false;
      if (event.logicalKey == LogicalKeyboardKey.keyS && agent.connected) {
        _showSessions();
        return true;
      }
    }
    return false;
  }

  Future<void> _showApproval(ApprovalRequest request) async {
    if (!mounted) return;
    approvalDialogOpen = true;
    final approved = await showDialog<bool>(
      context: context,
      barrierDismissible: false,
      builder: (context) => AlertDialog(
        title: const Text('Approve operation blocked by Bash Safety?'),
        content: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 620),
          child: SingleChildScrollView(
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  request.tool,
                  style: Theme.of(context).textTheme.labelLarge,
                ),
                const SizedBox(height: 8),
                SelectableText(request.description),
                const SizedBox(height: 16),
                Text('Reason', style: Theme.of(context).textTheme.labelLarge),
                const SizedBox(height: 6),
                SelectableText(request.reason),
              ],
            ),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Reject'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Approve once'),
          ),
        ],
      ),
    );
    approvalDialogOpen = false;
    if (agent.pendingApproval?.id == request.id) {
      agent.resolveApproval(request.id, approved == true);
    }
  }

  String _defaultAddress(ConnectionKind kind) => switch (kind) {
    ConnectionKind.unix => defaultUnixAddress,
    ConnectionKind.tcp => '127.0.0.1:7346',
    ConnectionKind.websocket => defaultWebSocketAddress,
  };

  void _changed() {
    if (!mounted) return;
    model = agent.currentModel;
    thinking = agent.currentThinking;
    if (agent.connected && !wasConnected) {
      showConnectionSettings = false;
    }
    if (!agent.connected) {
      showConnectionSettings = true;
    }
    wasConnected = agent.connected;
    setState(() {});
    final approval = agent.pendingApproval;
    if (approval != null && approval.id != shownApprovalID) {
      shownApprovalID = approval.id;
      WidgetsBinding.instance.addPostFrameCallback(
        (_) => _showApproval(approval),
      );
    } else if (approval == null && approvalDialogOpen) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted &&
            approvalDialogOpen &&
            Navigator.of(context, rootNavigator: true).canPop()) {
          Navigator.of(context, rootNavigator: true).pop(false);
        }
      });
    }
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (scroll.hasClients) {
        scroll.animateTo(
          scroll.position.maxScrollExtent,
          duration: const Duration(milliseconds: 180),
          curve: Curves.easeOut,
        );
      }
    });
  }

  @override
  void dispose() {
    HardwareKeyboard.instance.removeHandler(_handleKey);
    if (kIsWeb) {
      ClipboardEvents.instance?.unregisterPasteEventListener(_handleWebPaste);
    }
    agent.removeListener(_changed);
    prompt.removeListener(_promptChanged);
    agent.dispose();
    prompt.dispose();
    promptFocus.dispose();
    address.dispose();
    scroll.dispose();
    super.dispose();
  }

  Future<void> _showWorkspace() async {
    final controller = TextEditingController(text: agent.currentCWD);
    final selected = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Agent working directory'),
        content: SizedBox(
          width: 620,
          child: TextField(
            controller: controller,
            autofocus: true,
            decoration: const InputDecoration(
              labelText: 'Absolute directory on the agent server',
              border: OutlineInputBorder(),
            ),
            onSubmitted: (value) => Navigator.pop(context, value.trim()),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, controller.text.trim()),
            child: const Text('Use directory'),
          ),
        ],
      ),
    );
    controller.dispose();
    if (selected != null && selected.isNotEmpty) {
      agent.setWorkingDirectory(selected);
    }
  }

  Future<void> _showSessions() async {
    try {
      final sessions = await agent.listSessions();
      if (!mounted) return;
      final selected = await showDialog<String>(
        context: context,
        builder: (context) => AlertDialog(
          title: const Text('Switch session'),
          content: SizedBox(
            width: 720,
            height: 480,
            child: sessions.isEmpty
                ? const Center(child: Text('No existing sessions'))
                : ListView.separated(
                    itemCount: sessions.length,
                    separatorBuilder: (_, _) => const Divider(height: 1),
                    itemBuilder: (context, index) {
                      final session = sessions[index];
                      final name = session.name.trim().isEmpty
                          ? session.id
                          : session.name;
                      final timestamp =
                          session.lastMessageTime
                              ?.toLocal()
                              .toString()
                              .substring(0, 16) ??
                          '';
                      return ListTile(
                        title: Text(name),
                        subtitle: Text(
                          session.preview.isEmpty
                              ? '(no messages)'
                              : session.preview,
                          maxLines: 2,
                          overflow: TextOverflow.ellipsis,
                        ),
                        trailing: Text(timestamp),
                        onTap: () => Navigator.pop(context, session.id),
                      );
                    },
                  ),
          ),
          actions: [
            FilledButton.icon(
              onPressed: () => Navigator.pop(context, '__new_session__'),
              icon: const Icon(Icons.add),
              label: const Text('New session'),
            ),
            TextButton(
              onPressed: () => Navigator.pop(context),
              child: const Text('Cancel'),
            ),
          ],
        ),
      );
      if (selected == '__new_session__') {
        agent.newSession();
      } else if (selected != null) {
        agent.switchSession(selected);
      }
    } catch (error) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Could not list sessions: $error')),
        );
      }
    }
  }

  Future<void> _pickImages() async {
    final files = await ImagePicker().pickMultiImage();
    if (files.isEmpty) return;
    final additions = <PendingImage>[];
    for (final file in files) {
      final bytes = await file.readAsBytes();
      final extension = file.name.split('.').last.toLowerCase();
      final mime =
          file.mimeType ??
          switch (extension) {
            'png' => 'image/png',
            'jpg' || 'jpeg' => 'image/jpeg',
            'gif' => 'image/gif',
            'webp' => 'image/webp',
            _ => '',
          };
      if (supportedClipboardImageMimeTypes.contains(mime)) {
        additions.add(PendingImage(file.name, mime, bytes));
      }
    }
    _addAttachments(additions);
  }

  Future<void> _handleWebPaste(ClipboardReadEvent event) async {
    if (!kIsWeb || !promptFocus.hasFocus || !agent.connected) return;
    try {
      // Requesting the event reader prevents the browser's default text paste,
      // so explicitly insert plain text when the clipboard has no image.
      final reader = await event.getClipboardReader();
      await _pasteFromReader(reader);
    } catch (error) {
      _showPasteError(error);
    }
  }

  Future<void> _pasteFromSystemClipboard() async {
    try {
      final clipboard = SystemClipboard.instance;
      if (clipboard == null) {
        await _pastePlainText();
        return;
      }
      await _pasteFromReader(await clipboard.read());
    } catch (error) {
      _showPasteError(error);
    }
  }

  Future<void> _pasteFromReader(ClipboardReader reader) async {
    final images = await readClipboardImages(reader);
    if (images.isNotEmpty) {
      _addAttachments(
        images
            .map(
              (image) => PendingImage(image.name, image.mimeType, image.bytes),
            )
            .toList(),
      );
      return;
    }
    final text = await reader.readValue(Formats.plainText);
    if (text != null) _insertEditorText(text);
  }

  Future<void> _pastePlainText() async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    if (data?.text != null) _insertEditorText(data!.text!);
  }

  void _insertEditorText(String text) {
    final selection = prompt.selection;
    final start = selection.isValid ? selection.start : prompt.text.length;
    final end = selection.isValid ? selection.end : prompt.text.length;
    prompt.value = TextEditingValue(
      text: prompt.text.replaceRange(start, end, text),
      selection: TextSelection.collapsed(offset: start + text.length),
    );
  }

  void _handleInsertedContent(KeyboardInsertedContent content) {
    if (!supportedClipboardImageMimeTypes.contains(content.mimeType)) return;
    final bytes = content.data;
    if (bytes == null || bytes.isEmpty) {
      unawaited(_pasteFromSystemClipboard());
      return;
    }
    final extension = switch (content.mimeType) {
      'image/png' => 'png',
      'image/jpeg' => 'jpg',
      'image/gif' => 'gif',
      'image/webp' => 'webp',
      _ => 'img',
    };
    _addAttachments([
      PendingImage('pasted-image.$extension', content.mimeType, bytes),
    ]);
  }

  void _addAttachments(List<PendingImage> additions) {
    if (additions.isEmpty || !mounted) return;
    final total = [
      ...attachments,
      ...additions,
    ].fold<int>(0, (sum, image) => sum + image.bytes.length);
    if (attachments.length + additions.length > 4 || total > 10 * 1024 * 1024) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(
          content: Text('Attach at most 4 images and 10 MiB total'),
        ),
      );
      return;
    }
    setState(() => attachments.addAll(additions));
  }

  void _showPasteError(Object error) {
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(content: Text('Could not paste clipboard content: $error')),
    );
  }

  Widget _editorContextMenu(
    BuildContext context,
    EditableTextState editableTextState,
  ) {
    final items = editableTextState.contextMenuButtonItems.map((item) {
      if (item.type != ContextMenuButtonType.paste) return item;
      return item.copyWith(
        onPressed: () {
          editableTextState.hideToolbar();
          unawaited(_pasteFromSystemClipboard());
        },
      );
    }).toList();
    return AdaptiveTextSelectionToolbar.buttonItems(
      anchors: editableTextState.contextMenuAnchors,
      buttonItems: items,
    );
  }

  void _promptChanged() {
    if (mounted) setState(() {});
  }

  List<CommandSuggestion> _commandSuggestions() {
    final input = prompt.text;
    if (!input.startsWith('/')) return const [];
    if (input.startsWith('/model ')) {
      final prefix = input.substring('/model '.length);
      return models
          .where((value) => value.startsWith(prefix))
          .map(
            (value) => CommandSuggestion(
              '/model $value',
              'switch model for this session',
            ),
          )
          .toList();
    }
    if (input.startsWith('/thinking ')) {
      final prefix = input.substring('/thinking '.length);
      return thinkingLevels
          .where((value) => value.startsWith(prefix))
          .map(
            (value) =>
                CommandSuggestion('/thinking $value', 'set reasoning effort'),
          )
          .toList();
    }
    const commands = [
      CommandSuggestion('/compact ', 'summarize older context; optional focus'),
      CommandSuggestion('/cwd ', 'change agent working directory'),
      CommandSuggestion('/model ', 'switch model'),
      CommandSuggestion('/thinking ', 'switch reasoning effort'),
      CommandSuggestion('/sessions', 'select another session'),
      CommandSuggestion('/new', 'create a new session'),
      CommandSuggestion('/name ', 'name or clear this session'),
      CommandSuggestion('/abort', 'abort and kill a running tool'),
      CommandSuggestion('/clear', 'clear the visible transcript'),
      CommandSuggestion('/help', 'show available commands'),
    ];
    return commands
        .where((command) => command.value.startsWith(input))
        .toList();
  }

  void _chooseSuggestion(CommandSuggestion suggestion) {
    prompt.text = suggestion.value;
    prompt.selection = TextSelection.collapsed(offset: prompt.text.length);
  }

  void _runCommand(String input) {
    final parts = input.trim().split(RegExp(r'\s+'));
    switch (parts.first) {
      case '/compact':
        final instructions = input.trim().substring('/compact'.length).trim();
        agent.compact(instructions);
      case '/cwd':
        final workspace = input.trim().substring('/cwd'.length).trim();
        if (workspace.isEmpty) {
          agent.setLocalStatus('usage: /cwd <directory>');
          return;
        }
        agent.setWorkingDirectory(workspace);
      case '/model':
        if (parts.length != 2 || !models.contains(parts[1])) {
          agent.setLocalStatus('usage: /model <model-id>');
          return;
        }
        agent.setModel(parts[1]);
      case '/thinking':
        if (parts.length != 2 || !thinkingLevels.contains(parts[1])) {
          agent.setLocalStatus('usage: /thinking <level>');
          return;
        }
        agent.setThinking(parts[1]);
      case '/sessions':
        _showSessions();
      case '/new':
        agent.newSession();
      case '/name':
        final value = input.trim().substring('/name'.length).trim();
        agent.setSessionName(value.isEmpty || value == 'null' ? null : value);
      case '/abort':
        agent.abort();
      case '/clear':
        agent.clearTranscript();
      case '/help':
        agent.setLocalStatus(
          'commands: /compact [focus] /cwd /model /thinking /sessions /new /name /abort /clear',
        );
      default:
        agent.setLocalStatus('unknown command: ${parts.first}');
    }
  }

  KeyEventResult _desktopEditorKey(FocusNode node, KeyEvent event) {
    if (event.logicalKey != LogicalKeyboardKey.enter &&
        event.logicalKey != LogicalKeyboardKey.numpadEnter) {
      return KeyEventResult.ignored;
    }
    if (event is KeyRepeatEvent) {
      return KeyEventResult.handled;
    }
    if (event is! KeyDownEvent) {
      return KeyEventResult.handled;
    }
    if (HardwareKeyboard.instance.isShiftPressed) {
      _insertEditorNewline();
    } else {
      _submit();
    }
    return KeyEventResult.handled;
  }

  void _insertEditorNewline() {
    final selection = prompt.selection;
    final start = selection.isValid ? selection.start : prompt.text.length;
    final end = selection.isValid ? selection.end : prompt.text.length;
    final text = prompt.text.replaceRange(start, end, '\n');
    prompt.value = TextEditingValue(
      text: text,
      selection: TextSelection.collapsed(offset: start + 1),
    );
  }

  void _submit() {
    if (!agent.connected) return;
    final value = prompt.text.trim();
    if (value.isEmpty && attachments.isEmpty) return;
    if (value.startsWith('/') && attachments.isEmpty) {
      final command = value.split(RegExp(r'\s+')).first;
      const allowedWhileStreaming = {
        '/sessions',
        '/new',
        '/name',
        '/abort',
        '/clear',
        '/help',
      };
      if (agent.streaming && !allowedWhileStreaming.contains(command)) {
        agent.setLocalStatus(
          'this command cannot change an active session; use /new or /sessions to detach',
        );
        return;
      }
      _runCommand(value);
    } else {
      if (agent.streaming) return;
      agent.prompt(value, List<PendingImage>.from(attachments));
    }
    prompt.clear();
    setState(attachments.clear);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: SafeArea(
        child: Column(
          children: [
            _connectionBar(),
            const Divider(height: 1),
            Expanded(
              child: agent.messages.isEmpty
                  ? const Center(
                      child: Text(
                        'Connect to pi-go-agent and start a conversation.',
                      ),
                    )
                  : ListView.builder(
                      controller: scroll,
                      padding: const EdgeInsets.all(20),
                      itemCount: agent.messages.length,
                      itemBuilder: (_, index) =>
                          _message(agent.messages[index]),
                    ),
            ),
            const Divider(height: 1),
            _composer(),
          ],
        ),
      ),
    );
  }

  Widget _connectionBar() => Padding(
    padding: const EdgeInsets.fromLTRB(16, 10, 16, 10),
    child: Column(
      children: [
        LayoutBuilder(
          builder: (context, constraints) {
            final compact = constraints.maxWidth < 720;
            return Column(
              children: [
                Row(
                  children: [
                    const Text(
                      'Pi Go',
                      style: TextStyle(
                        fontSize: 20,
                        fontWeight: FontWeight.w700,
                      ),
                    ),
                    const SizedBox(width: 10),
                    if (compact)
                      PopupMenuButton<void>(
                        tooltip: 'Current model and thinking effort',
                        icon: const Icon(Icons.tune),
                        itemBuilder: (_) => [
                          PopupMenuItem(
                            enabled: false,
                            child: Row(
                              children: [
                                const Icon(Icons.smart_toy_outlined, size: 18),
                                const SizedBox(width: 8),
                                Flexible(child: Text(model)),
                              ],
                            ),
                          ),
                          PopupMenuItem(
                            enabled: false,
                            child: Row(
                              children: [
                                const Icon(Icons.psychology_outlined, size: 18),
                                const SizedBox(width: 8),
                                Text('thinking: $thinking'),
                              ],
                            ),
                          ),
                        ],
                      )
                    else ...[
                      Flexible(
                        child: SingleChildScrollView(
                          scrollDirection: Axis.horizontal,
                          child: Row(
                            children: [
                              Chip(
                                avatar: const Icon(
                                  Icons.smart_toy_outlined,
                                  size: 17,
                                ),
                                label: Text(model),
                              ),
                              const SizedBox(width: 6),
                              Chip(
                                avatar: const Icon(
                                  Icons.psychology_outlined,
                                  size: 17,
                                ),
                                label: Text('thinking: $thinking'),
                              ),
                            ],
                          ),
                        ),
                      ),
                    ],
                    const Spacer(),
                    if (compact)
                      IconButton.outlined(
                        tooltip: 'Switch session',
                        onPressed: agent.connected ? _showSessions : null,
                        icon: const Icon(Icons.account_tree_outlined),
                      )
                    else
                      OutlinedButton.icon(
                        onPressed: agent.connected ? _showSessions : null,
                        icon: const Icon(Icons.account_tree_outlined),
                        label: const Text('Sessions  Ctrl-B S'),
                      ),
                    const SizedBox(width: 6),
                    IconButton.outlined(
                      tooltip: agent.currentCWD.isEmpty
                          ? 'Set agent working directory'
                          : 'Workspace: ${agent.currentCWD}',
                      onPressed: agent.connected && !agent.streaming
                          ? _showWorkspace
                          : null,
                      icon: const Icon(Icons.folder_outlined),
                    ),
                    const SizedBox(width: 6),
                    IconButton.outlined(
                      tooltip: showConnectionSettings
                          ? 'Hide connection settings'
                          : 'Show connection settings',
                      onPressed: () => setState(
                        () => showConnectionSettings = !showConnectionSettings,
                      ),
                      icon: Icon(
                        agent.connected ? Icons.lan_outlined : Icons.link_off,
                      ),
                    ),
                  ],
                ),
                const SizedBox(height: 4),
                Align(
                  alignment: Alignment.centerLeft,
                  child: Text(
                    agent.status,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      color: agent.connected
                          ? Theme.of(context).colorScheme.primary
                          : Theme.of(context).colorScheme.onSurfaceVariant,
                    ),
                  ),
                ),
              ],
            );
          },
        ),
        if (!agent.connected || showConnectionSettings) ...[
          const SizedBox(height: 8),
          SingleChildScrollView(
            scrollDirection: Axis.horizontal,
            child: SegmentedButton<ConnectionKind>(
              segments: [
                ButtonSegment(
                  value: ConnectionKind.unix,
                  label: const Text('Unix'),
                  enabled: nativeSocketsSupported && !webSocketOnlyClient,
                ),
                ButtonSegment(
                  value: ConnectionKind.tcp,
                  label: const Text('TCP'),
                  enabled: nativeSocketsSupported && !webSocketOnlyClient,
                ),
                const ButtonSegment(
                  value: ConnectionKind.websocket,
                  label: Text('WebSocket'),
                ),
              ],
              selected: {connectionKind},
              onSelectionChanged: agent.connected
                  ? null
                  : (value) {
                      setState(() {
                        connectionKind = value.first;
                        address.text = _defaultAddress(connectionKind);
                      });
                    },
            ),
          ),
          const SizedBox(height: 8),
          Row(
            children: [
              Expanded(
                child: TextField(
                  controller: address,
                  enabled: !agent.connected,
                  decoration: const InputDecoration(
                    isDense: true,
                    labelText: 'Agent server address',
                    border: OutlineInputBorder(),
                  ),
                ),
              ),
              const SizedBox(width: 10),
              FilledButton(
                onPressed: agent.connected
                    ? agent.disconnect
                    : () => agent.connect(connectionKind, address.text),
                child: Text(agent.connected ? 'Disconnect' : 'Connect'),
              ),
            ],
          ),
        ],
      ],
    ),
  );

  Widget _safetyBanner(TranscriptItem item) {
    final status = item.safetyStatus;
    final scheme = Theme.of(context).colorScheme;
    final (color, icon) = switch (status) {
      'approved' => (
        Theme.of(context).brightness == Brightness.dark
            ? Colors.greenAccent.shade400
            : Colors.green.shade700,
        Icons.check_circle_outline,
      ),
      'rejected' => (scheme.error, Icons.gpp_bad_outlined),
      _ => (scheme.onSurfaceVariant, Icons.hourglass_top_outlined),
    };
    return Container(
      margin: const EdgeInsets.only(top: 8),
      padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 7),
      decoration: BoxDecoration(
        color: color.withValues(alpha: .12),
        borderRadius: BorderRadius.circular(7),
        border: Border.all(color: color.withValues(alpha: .45)),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(icon, size: 17, color: color),
          const SizedBox(width: 7),
          Expanded(
            child: Text(
              item.safetyMessage ?? 'Luna is classifying this command…',
              style: TextStyle(
                color: color,
                fontSize: 12,
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
        ],
      ),
    );
  }

  Widget _message(TranscriptItem item) => Align(
    alignment: item.role == 'You'
        ? Alignment.centerRight
        : Alignment.centerLeft,
    child: Container(
      constraints: const BoxConstraints(maxWidth: 820),
      margin: const EdgeInsets.only(bottom: 14),
      padding: const EdgeInsets.all(14),
      decoration: BoxDecoration(
        color: item.error
            ? Theme.of(context).colorScheme.errorContainer
            : (item.role == 'You'
                  ? Theme.of(context).colorScheme.primaryContainer
                  : Theme.of(context).colorScheme.surfaceContainerHighest),
        borderRadius: BorderRadius.circular(12),
        border: Border.all(
          color: item.error
              ? Theme.of(context).colorScheme.error
              : Theme.of(context).colorScheme.outlineVariant,
        ),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          InkWell(
            onTap:
                item.role.startsWith('Tool ·') ||
                    item.role == 'Compaction summary'
                ? () => setState(() => item.collapsed = !item.collapsed)
                : null,
            child: Row(
              children: [
                Expanded(
                  child: Text(
                    item.role,
                    style: TextStyle(
                      fontSize: 12,
                      fontWeight: FontWeight.w700,
                      color: Theme.of(context).colorScheme.onSurfaceVariant,
                    ),
                  ),
                ),
                if (item.role.startsWith('Tool ·') ||
                    item.role == 'Compaction summary')
                  Icon(
                    item.collapsed ? Icons.expand_more : Icons.expand_less,
                    size: 19,
                    color: Theme.of(context).colorScheme.onSurfaceVariant,
                  ),
              ],
            ),
          ),
          if (item.safetyStatus != null) _safetyBanner(item),
          if (item.images.isNotEmpty && !item.collapsed) ...[
            const SizedBox(height: 8),
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: item.images
                  .map(
                    (bytes) => ClipRRect(
                      borderRadius: BorderRadius.circular(8),
                      child: Image.memory(
                        bytes,
                        width: 240,
                        height: 180,
                        fit: BoxFit.contain,
                      ),
                    ),
                  )
                  .toList(),
            ),
          ],
          if ((item.role.startsWith('Tool ·') ||
                  item.role == 'Compaction summary') &&
              item.collapsed) ...[
            const SizedBox(height: 5),
            Text(
              item.role == 'Compaction summary'
                  ? 'Summary folded · tap to expand'
                  : 'Input and output folded · tap to expand',
              style: TextStyle(
                color: Theme.of(context).colorScheme.onSurfaceVariant,
                fontSize: 12,
              ),
            ),
          ],
          if (item.text.isNotEmpty && !item.collapsed) ...[
            const SizedBox(height: 7),
            if (item.role.startsWith('Tool ·') ||
                item.role == 'Compaction summary')
              MarkdownBody(
                data: item.text,
                selectable: true,
                styleSheet: MarkdownStyleSheet.fromTheme(Theme.of(context))
                    .copyWith(
                      code: const TextStyle(fontFamily: 'monospace'),
                      codeblockDecoration: BoxDecoration(
                        color: Theme.of(
                          context,
                        ).colorScheme.surfaceContainerLow,
                        borderRadius: BorderRadius.circular(6),
                      ),
                      codeblockPadding: const EdgeInsets.all(12),
                    ),
              )
            else
              MarkdownBody(
                data: item.text,
                selectable: true,
                styleSheet: MarkdownStyleSheet.fromTheme(Theme.of(context))
                    .copyWith(
                      codeblockDecoration: BoxDecoration(
                        color: Theme.of(
                          context,
                        ).colorScheme.surfaceContainerLow,
                        borderRadius: BorderRadius.circular(6),
                      ),
                      codeblockPadding: const EdgeInsets.all(12),
                      blockquoteDecoration: BoxDecoration(
                        border: Border(
                          left: BorderSide(
                            color: Theme.of(context).colorScheme.primary,
                            width: 3,
                          ),
                        ),
                      ),
                    ),
                imageBuilder: (uri, title, alt) => Tooltip(
                  message: uri.toString(),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      const Icon(Icons.image_outlined),
                      const SizedBox(width: 6),
                      Flexible(child: Text(alt ?? 'Image')),
                    ],
                  ),
                ),
              ),
          ],
        ],
      ),
    ),
  );

  Widget _composer() => Padding(
    padding: const EdgeInsets.fromLTRB(16, 10, 16, 14),
    child: Column(
      children: [
        Wrap(
          spacing: 16,
          runSpacing: 6,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Text(
              'session ${agent.total}  in ${agent.input}  cache ${agent.cacheRead}  out ${agent.output}  context ${agent.contextUsed}/272000  upstream ${agent.upstreamTransport}',
            ),
            if (agent.streaming)
              TextButton.icon(
                onPressed: agent.abort,
                icon: const Icon(Icons.stop),
                label: const Text('Abort / kill tool'),
              ),
          ],
        ),
        const SizedBox(height: 8),
        if (_commandSuggestions().isNotEmpty) ...[
          Align(
            alignment: Alignment.centerLeft,
            child: Wrap(
              spacing: 6,
              runSpacing: 6,
              children: _commandSuggestions()
                  .map(
                    (suggestion) => ActionChip(
                      label: Text(
                        '${suggestion.value}  ${suggestion.description}',
                      ),
                      onPressed: () => _chooseSuggestion(suggestion),
                    ),
                  )
                  .toList(),
            ),
          ),
          const SizedBox(height: 8),
        ],
        if (attachments.isNotEmpty) ...[
          Align(
            alignment: Alignment.centerLeft,
            child: Wrap(
              spacing: 8,
              runSpacing: 8,
              children: List.generate(attachments.length, (index) {
                final image = attachments[index];
                return InputChip(
                  avatar: ClipRRect(
                    borderRadius: BorderRadius.circular(3),
                    child: Image.memory(
                      image.bytes,
                      width: 28,
                      height: 28,
                      fit: BoxFit.cover,
                    ),
                  ),
                  label: Text(image.name),
                  onDeleted: () => setState(() => attachments.removeAt(index)),
                );
              }),
            ),
          ),
          const SizedBox(height: 8),
        ],
        Row(
          crossAxisAlignment: CrossAxisAlignment.end,
          children: [
            IconButton.outlined(
              tooltip: 'Attach images',
              onPressed: agent.connected ? _pickImages : null,
              icon: const Icon(Icons.add_photo_alternate_outlined),
            ),
            const SizedBox(width: 8),
            Expanded(
              child: Builder(
                builder: (context) {
                  final nativeMobile =
                      !kIsWeb &&
                      (defaultTargetPlatform == TargetPlatform.android ||
                          defaultTargetPlatform == TargetPlatform.iOS);
                  final desktopInput =
                      !nativeMobile && MediaQuery.sizeOf(context).width >= 720;
                  final editor = TextField(
                    controller: prompt,
                    focusNode: promptFocus,
                    enabled: agent.connected,
                    contentInsertionConfiguration:
                        ContentInsertionConfiguration(
                          allowedMimeTypes: supportedClipboardImageMimeTypes,
                          onContentInserted: _handleInsertedContent,
                        ),
                    contextMenuBuilder: _editorContextMenu,
                    minLines: 1,
                    maxLines: 8,
                    keyboardType: TextInputType.multiline,
                    textInputAction: desktopInput
                        ? TextInputAction.send
                        : TextInputAction.newline,
                    onSubmitted: desktopInput ? (_) => _submit() : null,
                    decoration: InputDecoration(
                      hintText: agent.streaming
                          ? 'Agent is replying · prepare your next message'
                          : desktopInput
                          ? 'Enter to send · Shift+Enter for newline · / for commands'
                          : 'Newline with Enter · tap Send when ready · / for commands',
                      border: const OutlineInputBorder(),
                    ),
                  );
                  final editorWithSubmit = desktopInput
                      ? Focus(onKeyEvent: _desktopEditorKey, child: editor)
                      : editor;
                  if (kIsWeb) return editorWithSubmit;
                  return Actions(
                    actions: {
                      PasteTextIntent: CallbackAction<PasteTextIntent>(
                        onInvoke: (_) {
                          unawaited(_pasteFromSystemClipboard());
                          return null;
                        },
                      ),
                    },
                    child: editorWithSubmit,
                  );
                },
              ),
            ),
            const SizedBox(width: 10),
            FilledButton.icon(
              onPressed: agent.connected && !agent.streaming ? _submit : null,
              icon: const Icon(Icons.arrow_upward),
              label: const Text('Send'),
            ),
          ],
        ),
      ],
    ),
  );
}
