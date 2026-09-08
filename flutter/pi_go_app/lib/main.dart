import 'dart:async';
import 'dart:convert';
import 'dart:ui' as ui;
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_markdown_plus/flutter_markdown_plus.dart';
import 'package:flutter_highlight/themes/atom-one-dark-reasonable.dart';
import 'package:flutter_highlight/themes/atom-one-light.dart';
import 'package:highlight/highlight.dart' as syntax;
import 'package:device_info_plus/device_info_plus.dart';
import 'package:image_picker/image_picker.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:super_clipboard/super_clipboard.dart';
import 'package:url_launcher/url_launcher.dart';

import 'clipboard_images.dart';
import 'feedback_notifications.dart';
import 'forge_fonts.dart';
import 'push_identity.dart';
import 'push_relay.dart';
import 'session_crypto.dart';

import 'transport.dart';

void main() => runApp(const PiGoApp());

class PiGoApp extends StatefulWidget {
  const PiGoApp({super.key});

  @override
  State<PiGoApp> createState() => _PiGoAppState();
}

class _PiGoAppState extends State<PiGoApp> {
  final fonts = ForgeFontController();

  ThemeData _theme(Brightness brightness) {
    final seed = brightness == Brightness.light
        ? const Color(0xff25765f)
        : const Color(0xff62d6a7);
    final base = ThemeData(
      colorScheme: ColorScheme.fromSeed(
        seedColor: seed,
        brightness: brightness,
      ),
      useMaterial3: true,
    );
    return base.copyWith(textTheme: fonts.apply(base.textTheme));
  }

  @override
  void dispose() {
    fonts.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: fonts,
    builder: (context, _) => ForgeFontScope(
      controller: fonts,
      child: MaterialApp(
        debugShowCheckedModeBanner: false,
        title: 'Forge',
        theme: _theme(Brightness.light),
        darkTheme: _theme(Brightness.dark),
        themeMode: ThemeMode.system,
        home: const AgentPage(),
      ),
    ),
  );
}

enum ConnectionKind { local, unix, tcp, websocket }

class ProviderModel {
  const ProviderModel(this.id, this.provider, this.label);
  final String id, provider, label;
}

class APIProviderConfig {
  const APIProviderConfig({
    required this.name,
    required this.configured,
    required this.apiKeyConfigured,
    required this.protocol,
    required this.openAIBaseURL,
    required this.anthropicBaseURL,
    required this.defaultModel,
    required this.models,
  });
  final String name, protocol, openAIBaseURL, anthropicBaseURL, defaultModel;
  final bool configured, apiKeyConfigured;
  final List<String> models;

  factory APIProviderConfig.fromMap(Map<dynamic, dynamic> value) =>
      APIProviderConfig(
        name: '${value['name'] ?? ''}',
        configured: value['configured'] == true,
        apiKeyConfigured: value['apiKeyConfigured'] == true,
        protocol: '${value['protocol'] ?? 'openai'}',
        openAIBaseURL: '${value['openaiBaseUrl'] ?? ''}',
        anthropicBaseURL: '${value['anthropicBaseUrl'] ?? ''}',
        defaultModel: '${value['defaultModel'] ?? ''}',
        models: (value['models'] as List? ?? const [])
            .map((item) => '$item')
            .where((item) => item.isNotEmpty)
            .toList(),
      );
}

class CommandSuggestion {
  const CommandSuggestion(this.value, this.description);
  final String value, description;
}

class SessionItem {
  SessionItem({
    required this.id,
    required this.summary,
    required this.lastMessageTime,
    required this.active,
    required this.waitingInput,
  });
  final String id, summary;
  final DateTime? lastMessageTime;
  final bool active, waitingInput;
}

class SessionPage {
  const SessionPage({
    required this.sessions,
    required this.nextOffset,
    required this.hasMore,
  });
  final List<SessionItem> sessions;
  final int nextOffset;
  final bool hasMore;
}

class ApprovalRequest {
  ApprovalRequest(
    this.id,
    this.toolCallID,
    this.tool,
    this.description,
    this.reason,
    this.details,
  );
  final String id, toolCallID, tool, description, reason;
  final dynamic details;
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
    this.collapseManuallySet = false,
    this.toolName,
    this.toolArguments,
    this.toolOutput = '',
    this.toolDetails,
    this.images = const [],
    this.safetyStatus,
    this.safetyMessage,
    this.thinking = '',
    this.thinkingCollapsed = false,
  });
  final String role;
  final String? id;
  String text;
  bool error;
  bool collapsed;
  bool collapseManuallySet;
  final String? toolName;
  final dynamic toolArguments;
  String toolOutput;
  dynamic toolDetails;
  final List<Uint8List> images;
  String? safetyStatus, safetyMessage;
  String thinking;
  bool thinkingCollapsed;
  int fontTextScanned = 0;
  int fontThinkingScanned = 0;
  bool fontRoleScanned = false;
}

String _sourceLanguageForPath(String path) {
  final name = path.replaceAll('\\', '/').split('/').last.toLowerCase();
  if (name == 'dockerfile' || name.startsWith('dockerfile.')) {
    return 'dockerfile';
  }
  if (name == 'makefile' || name == 'gnumakefile') return 'makefile';
  if (name == 'cmakelists.txt') return 'cmake';
  if (name == '.env' || name.startsWith('.env.')) return 'ini';
  final extension = name.contains('.') ? name.split('.').last : '';
  return switch (extension) {
    'dart' => 'dart',
    'go' => 'go',
    'js' || 'mjs' || 'cjs' || 'jsx' => 'javascript',
    'ts' || 'mts' || 'cts' || 'tsx' => 'typescript',
    'py' => 'python',
    'php' || 'php3' || 'php4' || 'php5' || 'phtml' => 'php',
    'rb' => 'ruby',
    'rs' => 'rust',
    'java' => 'java',
    'kt' || 'kts' => 'kotlin',
    'swift' => 'swift',
    'c' || 'h' || 'cc' || 'cpp' || 'cxx' || 'hpp' => 'cpp',
    'cs' => 'cs',
    'm' || 'mm' => 'objectivec',
    'sh' || 'bash' || 'zsh' => 'bash',
    'ps1' => 'powershell',
    'html' || 'htm' || 'xml' || 'plist' || 'svg' => 'xml',
    'css' => 'css',
    'scss' => 'scss',
    'json' => 'json',
    'yaml' || 'yml' => 'yaml',
    'sql' => 'sql',
    'md' || 'markdown' => 'markdown',
    'ini' || 'cfg' || 'conf' => 'ini',
    'proto' => 'protobuf',
    'graphql' || 'gql' => 'graphql',
    'diff' || 'patch' => 'diff',
    _ => 'text',
  };
}

List<TextSpan> _syntaxSpans(
  String source,
  String language,
  Map<String, TextStyle> theme,
) {
  if (language == 'text' || source.length > 512 * 1024) {
    return [TextSpan(text: source)];
  }
  try {
    final nodes = syntax.highlight.parse(source, language: language).nodes;
    if (nodes == null) return [TextSpan(text: source)];
    List<TextSpan> convert(List<syntax.Node> values) => [
      for (final node in values)
        if (node.value != null)
          TextSpan(text: node.value, style: theme[node.className])
        else
          TextSpan(
            style: theme[node.className],
            children: convert(node.children ?? const []),
          ),
    ];
    return convert(nodes);
  } catch (_) {
    return [TextSpan(text: source)];
  }
}

class AgentConnection extends ChangeNotifier {
  AgentTransport? _transport;
  StreamSubscription<String>? _lines;
  final List<TranscriptItem> messages = [];
  String status = 'disconnected';
  String endpoint = '';
  ConnectionKind endpointKind = ConnectionKind.websocket;
  bool streaming = false;
  bool yolo = false;
  ApprovalRequest? pendingApproval;
  int input = 0;
  int cacheRead = 0;
  int output = 0;
  int total = 0;
  int contextUsed = 0;
  String upstreamTransport = '—';
  String currentModel = 'gpt-5.6-terra';
  String currentThinking = 'high';
  String classifierModel = 'gpt-5.6-luna';
  String currentCWD = '';
  String? currentSession;
  bool authenticated = false;
  String authAccountID = '';
  bool authLoginPending = false;
  String authUserCode = '';
  String authVerificationURI = '';
  List<APIProviderConfig> apiProviders = [];
  List<ProviderModel> availableModels = const [
    ProviderModel(
      'gpt-5.3-codex-spark',
      'openai-codex',
      'OpenAI Codex · gpt-5.3-codex-spark',
    ),
    ProviderModel('gpt-5.5', 'openai-codex', 'OpenAI Codex · gpt-5.5'),
    ProviderModel(
      'gpt-5.6-luna',
      'openai-codex',
      'OpenAI Codex · gpt-5.6-luna',
    ),
    ProviderModel('gpt-5.6-sol', 'openai-codex', 'OpenAI Codex · gpt-5.6-sol'),
    ProviderModel(
      'gpt-5.6-terra',
      'openai-codex',
      'OpenAI Codex · gpt-5.6-terra',
    ),
    ProviderModel('gpt-6-astra', 'openai-codex', 'OpenAI Codex · gpt-6-astra'),
  ];
  final Map<String, bool> sessionActive = {};
  final Set<String> sessionsWaitingInput = {};
  void Function(String session, bool needsFeedback)? onSessionNotification;
  void Function(AgentConnection connection)? onConnected;
  void Function(AgentConnection connection, Map<String, dynamic> response)?
  onAuthenticationMessage;
  void Function(AgentConnection connection, Map<String, dynamic> response)?
  onPushResponse;
  void Function(AgentConnection connection)? onTranscriptLoaded;
  Completer<SessionPage>? _sessionRequest;
  Completer<List<Map<String, dynamic>>>? _pushAuthorizationRequest;
  Completer<void>? _pushAuthorizationRevokeRequest;
  int _historyBefore = 0;
  bool _historyHasMore = false;
  bool _historyLoading = false;
  void Function(AgentConnection connection)? onHistoryPrepended;
  void Function(AgentConnection connection)? onDisconnected;
  int? _activeAssistantIndex;
  bool _protocolReady = false;
  String? _sessionToRestore;
  ClientSecureSession? _secureSession;
  bool _encryptionRequired = false;
  Future<void> _receiveQueue = Future<void>.value();
  Future<void> _sendQueue = Future<void>.value();
  Completer<void>? _connectCompleter;

  bool get connected => _transport != null && _protocolReady;

  Future<void> connect(ConnectionKind kind, String address) async {
    // Reset transport state without reporting an externally visible disconnect;
    // the slot-level reconnect controller owns retry scheduling.
    await disconnect(notifyDisconnected: false);
    endpointKind = kind;
    endpoint = address.trim();
    status = 'connecting';
    notifyListeners();
    try {
      final transport = switch (kind) {
        ConnectionKind.local => await connectLocalTransport(),
        ConnectionKind.unix => await connectUnixTransport(endpoint),
        ConnectionKind.tcp => await connectTcpTransport(endpoint),
        ConnectionKind.websocket => await connectWebSocketTransport(endpoint),
      };
      await _attach(transport);
      await _connectCompleter!.future.timeout(const Duration(seconds: 35));
    } catch (error) {
      await disconnect(notifyDisconnected: false);
      status = 'connection failed: $error';
      notifyListeners();
      onDisconnected?.call(this);
    }
  }

  Future<void> _attach(AgentTransport transport) async {
    _sessionToRestore = currentSession;
    _transport = transport;
    _protocolReady = false;
    _connectCompleter = Completer<void>();
    status = 'authenticating server…';
    _receiveQueue = Future<void>.value();
    _sendQueue = Future<void>.value();
    _lines = transport.messages.listen(
      (line) {
        _receiveQueue = _receiveQueue.then((_) => _receive(line));
      },
      onError: _closed,
      onDone: _closed,
    );
    notifyListeners();
  }

  Future<void> _finishAttach() async {
    if (_protocolReady || _transport == null) return;
    _protocolReady = true;
    status = 'connected';
    if (_connectCompleter case final completer? when !completer.isCompleted) {
      completer.complete();
    }
    notifyListeners();
    send({'id': 'flutter-auth-status', 'type': 'auth_status'});
    refreshProviderConfiguration();
    onConnected?.call(this);
    final sessionToRestore = _sessionToRestore;
    _sessionToRestore = null;
    if (sessionToRestore != null && sessionToRestore.isNotEmpty) {
      send({
        'id': 'flutter-reconnect-session',
        'type': 'switch_session',
        'session': sessionToRestore,
      });
    }
  }

  void _closed([Object? error]) {
    final transport = _transport;
    _transport = null;
    _protocolReady = false;
    if (_connectCompleter case final completer? when !completer.isCompleted) {
      completer.completeError(
        error ?? StateError('Connection closed during authentication'),
      );
    }
    _connectCompleter = null;
    _secureSession = null;
    _encryptionRequired = false;
    unawaited(transport?.close());
    streaming = false;
    pendingApproval = null;
    _pushAuthorizationRequest?.completeError(
      error ?? StateError('Connection closed'),
    );
    _pushAuthorizationRequest = null;
    _pushAuthorizationRevokeRequest?.completeError(
      error ?? StateError('Connection closed'),
    );
    _pushAuthorizationRevokeRequest = null;
    status = error == null ? 'disconnected' : 'connection lost: $error';
    onDisconnected?.call(this);
    notifyListeners();
  }

  Future<void> disconnect({bool notifyDisconnected = true}) async {
    final subscription = _lines;
    _lines = null;
    await subscription?.cancel();
    final transport = _transport;
    _transport = null;
    _protocolReady = false;
    if (_connectCompleter case final completer? when !completer.isCompleted) {
      completer.completeError(StateError('Connection cancelled'));
    }
    _connectCompleter = null;
    _secureSession = null;
    _encryptionRequired = false;
    await transport?.close();
    streaming = false;
    pendingApproval = null;
    _pushAuthorizationRequest?.completeError(StateError('Connection closed'));
    _pushAuthorizationRequest = null;
    _pushAuthorizationRevokeRequest?.completeError(
      StateError('Connection closed'),
    );
    _pushAuthorizationRevokeRequest = null;
    status = 'disconnected';
    if (notifyDisconnected) onDisconnected?.call(this);
    notifyListeners();
  }

  void send(Map<String, Object?> command) {
    final transport = _transport;
    if (transport == null) return;
    final secure = _secureSession;
    if (!_protocolReady &&
        !const {'auth_probe', 'auth_response'}.contains(command['type'])) {
      debugPrint(
        'Blocked plaintext command before authentication: ${command['type']}',
      );
      return;
    }
    if (secure != null && command['type'] != 'encrypted') {
      // Preserve AES-GCM sequence order even when callers issue multiple
      // commands while platform encryption is asynchronous.
      _sendQueue = _sendQueue
          .then((_) async {
            if (_transport != transport || _secureSession != secure) return;
            transport.send(jsonEncode(await secure.encrypt(command)));
          })
          .catchError((Object error, StackTrace stackTrace) {
            debugPrint('Forge encrypted send failed: $error\n$stackTrace');
            if (_transport == transport) _closed(error);
          });
      return;
    }
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

  Future<List<Map<String, dynamic>>> listPushAuthorizations() {
    if (!connected) throw StateError('Agent is not connected');
    if (_pushAuthorizationRequest != null) {
      throw StateError('Push authorization query is already running');
    }
    final completer = Completer<List<Map<String, dynamic>>>();
    _pushAuthorizationRequest = completer;
    send({'id': 'flutter-push-authorizations', 'type': 'push_authorizations'});
    return completer.future.timeout(
      const Duration(seconds: 20),
      onTimeout: () {
        if (_pushAuthorizationRequest == completer) {
          _pushAuthorizationRequest = null;
        }
        throw TimeoutException('Push authorization query timed out');
      },
    );
  }

  Future<void> revokePushAuthorization(String deviceId) {
    if (!connected) throw StateError('Agent is not connected');
    if (_pushAuthorizationRevokeRequest != null) {
      throw StateError('Push authorization revocation is already running');
    }
    final completer = Completer<void>();
    _pushAuthorizationRevokeRequest = completer;
    send({
      'id': 'flutter-push-authorization-revoke',
      'type': 'push_authorization_revoke',
      'deviceId': deviceId,
    });
    return completer.future.timeout(
      const Duration(seconds: 20),
      onTimeout: () {
        if (_pushAuthorizationRevokeRequest == completer) {
          _pushAuthorizationRevokeRequest = null;
        }
        throw TimeoutException('Push authorization revocation timed out');
      },
    );
  }

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

  void login() {
    if (!connected || authLoginPending) return;
    authLoginPending = true;
    authUserCode = '';
    authVerificationURI = '';
    status = 'starting OpenAI sign-in…';
    send({'id': 'flutter-auth-login', 'type': 'auth_login'});
    notifyListeners();
  }

  void cancelLogin() {
    if (!connected || !authLoginPending) return;
    send({'id': 'flutter-auth-cancel', 'type': 'auth_cancel'});
    authLoginPending = false;
    authUserCode = '';
    authVerificationURI = '';
    status = 'OpenAI sign-in cancelled';
    notifyListeners();
  }

  void logout() {
    if (!connected || authLoginPending) return;
    status = 'signing out…';
    send({'id': 'flutter-auth-logout', 'type': 'auth_logout'});
    notifyListeners();
  }

  void refreshProviderConfiguration() {
    if (!connected) return;
    send({
      'id': 'flutter-api-configs',
      'type': 'get_provider_config',
      'provider': 'api',
    });
    send({'id': 'flutter-models', 'type': 'list_models'});
    send({'id': 'flutter-classifier-config', 'type': 'get_classifier_config'});
  }

  void saveAPIProvider({
    required String name,
    required String apiKey,
    required String protocol,
    required String openAIBaseURL,
    required String anthropicBaseURL,
    required String defaultModel,
    required List<String> models,
    bool clearAPIKey = false,
  }) {
    send({
      'id': 'flutter-api-save-$name',
      'type': 'set_provider_config',
      'provider': 'api',
      'providerName': name.trim(),
      if (apiKey.trim().isNotEmpty) 'apiKey': apiKey.trim(),
      'clearApiKey': clearAPIKey,
      'protocol': protocol,
      'openaiBaseUrl': openAIBaseURL.trim(),
      'anthropicBaseUrl': anthropicBaseURL.trim(),
      'defaultModel': defaultModel.trim(),
      'models': models,
    });
    status = 'saving API provider $name…';
    notifyListeners();
  }

  void deleteAPIProvider(String name) {
    send({
      'id': 'flutter-api-delete-$name',
      'type': 'set_provider_config',
      'provider': 'api',
      'providerName': name,
      'deleteProvider': true,
    });
    status = 'deleting API provider $name…';
    notifyListeners();
  }

  void fetchAPIProviderModels(String name) {
    send({
      'id': 'flutter-api-models-$name',
      'type': 'fetch_provider_models',
      'provider': 'api',
      'providerName': name,
    });
    status = 'querying $name models…';
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

  void setClassifierModel(String model) {
    if (!connected || streaming) return;
    status = 'saving classifier model…';
    send({
      'id': 'flutter-classifier-model',
      'type': 'set_classifier_model',
      'classifierModel': model,
    });
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

  void setYOLO(bool enabled) {
    if (!connected || streaming) return;
    status = enabled
        ? 'enabling YOLO mode…'
        : 'enabling classifier safety gate…';
    send({'id': 'flutter-yolo', 'type': 'set_yolo', 'enabled': enabled});
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

  Future<SessionPage> listSessions({int offset = 0, int limit = 5}) {
    final request = Completer<SessionPage>();
    _sessionRequest = request;
    send({
      'id': 'flutter-sessions-${DateTime.now().microsecondsSinceEpoch}',
      'type': 'list_sessions',
      'before': offset,
      'limit': limit,
    });
    return request.future.timeout(const Duration(seconds: 5));
  }

  void newSession() {
    status = 'creating session…';
    notifyListeners();
    send({'id': 'flutter-new-session', 'type': 'new_session'});
  }

  void loadOlderTranscript() {
    if (!connected ||
        _historyLoading ||
        !_historyHasMore ||
        _historyBefore <= 0) {
      return;
    }
    _historyLoading = true;
    send({
      'id': 'flutter-history-${DateTime.now().microsecondsSinceEpoch}',
      'type': 'get_transcript_history',
      'before': _historyBefore,
      'limit': 25,
    });
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

  @visibleForTesting
  void receive(String line) {
    // Unit/widget tests inject decoded backend messages without a transport.
    _protocolReady = true;
    unawaited(_receive(line));
  }

  Future<void> _receive(String line) async {
    try {
      var response = jsonDecode(line) as Map<String, dynamic>;
      if (response['type'] == 'encrypted') {
        final secure = _secureSession;
        if (secure == null) {
          throw StateError('Encrypted message arrived before key derivation');
        }
        response = await secure.decrypt(response);
      } else if (_encryptionRequired) {
        throw StateError(
          'Plaintext message received after encryption negotiation',
        );
      }
      final responseType = '${response['type'] ?? ''}';
      if (responseType == 'key_confirmation') {
        final secure = _secureSession;
        if (secure == null) {
          throw StateError('Key confirmation arrived without keys');
        }
        final transport = _transport;
        transport?.send(
          jsonEncode(
            await secure.encrypt({
              'type': 'key_confirmation',
              'connectionId': '${response['connectionId'] ?? ''}',
            }),
          ),
        );
        return;
      }
      if (responseType.startsWith('auth_') &&
          const {
            'auth_required',
            'auth_challenge',
            'auth_success',
            'auth_failed',
          }.contains(responseType)) {
        if (responseType == 'auth_success') {
          unawaited(_finishAttach());
        } else {
          onAuthenticationMessage?.call(this, response);
        }
        return;
      }
      if (!_protocolReady &&
          responseType == 'response' &&
          response['command'] == 'get_state') {
        if (endpointKind != ConnectionKind.local) {
          throw StateError(
            'Network server attempted an unauthenticated plaintext session',
          );
        }
        // The bundled local child process is trusted parent/child IPC. Every
        // independently reachable WebSocket, TCP, and Unix listener requires
        // device identity and encrypted key confirmation.
        unawaited(_finishAttach());
      }
      if ('${response['command'] ?? ''}'.startsWith('push_')) {
        onPushResponse?.call(this, response);
      }
      if (response['command'] == 'push_authorizations') {
        final completer = _pushAuthorizationRequest;
        _pushAuthorizationRequest = null;
        if (completer != null) {
          if (response['error'] case final String error when error.isNotEmpty) {
            completer.completeError(error);
          } else {
            final authorizations =
                (response['pushAuthorizations'] as List? ?? const [])
                    .whereType<Map>()
                    .map(
                      (item) =>
                          item.map((key, value) => MapEntry('$key', value)),
                    )
                    .toList();
            completer.complete(authorizations);
          }
        }
      }
      if (response['command'] == 'push_authorization_revoke') {
        final completer = _pushAuthorizationRevokeRequest;
        _pushAuthorizationRevokeRequest = null;
        if (completer != null) {
          if (response['error'] case final String error when error.isNotEmpty) {
            completer.completeError(error);
          } else {
            completer.complete();
          }
        }
      }
      if (response['type'] == 'auth_device_code') {
        authLoginPending = true;
        authUserCode = '${response['userCode'] ?? ''}';
        authVerificationURI = '${response['verificationUri'] ?? ''}';
        status = 'Open ${response['verificationUri']} and enter $authUserCode';
      }
      if (response['authenticated'] case final bool signedIn) {
        authenticated = signedIn;
        authAccountID = '${response['accountId'] ?? ''}';
        if (response['command'] == 'auth_login') {
          authLoginPending = false;
          authUserCode = '';
          authVerificationURI = '';
          status = signedIn
              ? 'OpenAI sign-in complete'
              : 'OpenAI sign-in failed';
        } else if (response['command'] == 'auth_logout') {
          authLoginPending = false;
          status = 'signed out of OpenAI';
        }
      }
      if (response['providerConfigs'] case final List values) {
        apiProviders = values
            .whereType<Map>()
            .map(APIProviderConfig.fromMap)
            .where((config) => config.name.isNotEmpty)
            .toList();
        if (response['command'] == 'set_provider_config') {
          status = 'API provider settings saved';
          send({'id': 'flutter-models', 'type': 'list_models'});
        }
        if (response['command'] == 'fetch_provider_models') {
          status = 'API provider models updated';
          send({'id': 'flutter-models', 'type': 'list_models'});
        }
      } else if (response['providerConfig'] case final Map config) {
        // Compatibility with an older agent that exposes one Qwen-style API.
        final parsed = APIProviderConfig.fromMap({
          ...config,
          'name': config['name'] ?? response['provider'] ?? 'qwen-code-plan',
        });
        apiProviders = [parsed];
      }
      if (response['models'] case final List values) {
        final parsed = <ProviderModel>[];
        for (final value in values.whereType<Map<String, dynamic>>()) {
          final id = '${value['id'] ?? ''}';
          if (id.isNotEmpty) {
            parsed.add(
              ProviderModel(
                id,
                '${value['provider'] ?? ''}',
                '${value['label'] ?? id}',
              ),
            );
          }
        }
        if (parsed.isNotEmpty || response['command'] == 'list_models') {
          availableModels = parsed;
        }
      }
      if (response['event'] case final Map<String, dynamic> event
          when event['type'] == 'turn_summary') {
        final summary = '${event['summary'] ?? ''}';
        if (summary.isNotEmpty) status = summary;
      }
      if (response['type'] == 'session_status') {
        final session = '${response['session'] ?? ''}';
        if (session.isNotEmpty) {
          final wasActive = sessionActive[session] == true;
          final active = response['active'] == true;
          final waiting = response['waitingInput'] == true;
          sessionActive[session] = active;
          if (waiting) {
            final newlyWaiting = sessionsWaitingInput.add(session);
            if (newlyWaiting) onSessionNotification?.call(session, true);
          } else {
            sessionsWaitingInput.remove(session);
            if (wasActive && !active) {
              onSessionNotification?.call(session, false);
            }
          }
        }
      }
      if (response['command'] == 'get_transcript_history') {
        _historyLoading = false;
        if (response['error'] == null && response['historyMessages'] is List) {
          final older = _transcriptItems(
            response['historyMessages'],
            expandLatestTool: false,
          );
          if (older.isNotEmpty) {
            messages.insertAll(0, older);
            if (_activeAssistantIndex != null) {
              _activeAssistantIndex = _activeAssistantIndex! + older.length;
            }
            onHistoryPrepended?.call(this);
          }
          _historyBefore = (response['historyBefore'] as int?) ?? 0;
          _historyHasMore = response['historyHasMore'] == true;
        }
      }
      if (response['error'] case final String error when error.isNotEmpty) {
        status = error;
        if (response['command'] == 'auth_login') authLoginPending = false;
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
              summary: '${value['summary'] ?? ''}',
              lastMessageTime: DateTime.tryParse(
                '${value['lastMessageTime'] ?? ''}',
              ),
              active:
                  sessionActive['${value['id'] ?? ''}'] ??
                  value['active'] == true,
              waitingInput: sessionsWaitingInput.contains(
                '${value['id'] ?? ''}',
              ),
            ),
          );
        }
        _sessionRequest?.complete(
          SessionPage(
            sessions: sessions,
            nextOffset: (response['sessionsOffset'] as int?) ?? sessions.length,
            hasMore: response['sessionsHasMore'] == true,
          ),
        );
        _sessionRequest = null;
      }
      if (response['session'] case final String session
          when session.isNotEmpty) {
        currentSession = session;
      }
      if (response['model'] case final String model when model.isNotEmpty) {
        currentModel = model;
      }
      if (response['classifierModel'] case final String selected
          when selected.isNotEmpty) {
        classifierModel = selected;
        if (response['command'] == 'set_classifier_model') {
          status = 'classifier: $selected';
        }
      }
      if (response['thinking'] case final String thinking
          when thinking.isNotEmpty) {
        currentThinking = thinking;
      }
      if (response['yolo'] case final bool enabled) {
        yolo = enabled;
        if (response['command'] == 'set_yolo') {
          status = enabled
              ? 'YOLO mode enabled · classifier safety bypassed'
              : 'Classifier safety gate enabled';
        }
      }
      if (response['cwd'] case final String cwd when cwd.isNotEmpty) {
        currentCWD = cwd;
        if (response['command'] == 'set_cwd') status = 'workspace: $cwd';
      }
      if (response['state'] case final Map<String, dynamic> state) {
        _historyBefore = (response['historyBefore'] as int?) ?? 0;
        _historyHasMore = response['historyHasMore'] == true;
        _historyLoading = false;
        _restore(state);
        if (const {
          'get_state',
          'switch_session',
          'new_session',
        }.contains(response['command'])) {
          onTranscriptLoaded?.call(this);
        }
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
    } catch (error, stackTrace) {
      status = 'invalid agent response: $error';
      debugPrint('Forge protocol receive failed: $error\n$stackTrace');
      // Any malformed, unexpected plaintext, sequence, or AEAD failure is
      // terminal. Continuing could desynchronize keys or accept downgraded data.
      unawaited(disconnect());
    }
    notifyListeners();
  }

  List<TranscriptItem> _transcriptItems(
    dynamic stored, {
    bool expandLatestTool = true,
  }) {
    if (stored is! List) return const [];
    final result = <TranscriptItem>[];
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
      final thinking = _contentThinking(value['content']);
      final images = _contentImages(value['content']);
      if (text.isEmpty && thinking.isEmpty && images.isEmpty) continue;
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
      result.add(
        TranscriptItem(
          _roleLabel(role, label),
          role == 'toolResult'
              ? _toolMarkdown(toolName!, arguments, text)
              : text,
          error: value['isError'] == true,
          collapsed: role == 'toolResult' || role == 'compactionSummary',
          toolName: toolName,
          toolArguments: arguments,
          toolOutput: role == 'toolResult' ? text : '',
          toolDetails: role == 'toolResult'
              ? (value['toolDetails'] ?? value['details'])
              : null,
          images: images,
          thinking: thinking,
          thinkingCollapsed: thinking.isNotEmpty && text.isNotEmpty,
        ),
      );
    }
    if (expandLatestTool) _applyAutomaticToolCollapse(result);
    return result;
  }

  void _applyAutomaticToolCollapse(List<TranscriptItem> values) {
    TranscriptItem? latest;
    for (final item in values.reversed) {
      if (item.role.startsWith('Tool ·')) {
        latest = item;
        break;
      }
    }
    for (final item in values) {
      if (!item.role.startsWith('Tool ·') || item.collapseManuallySet) continue;
      item.collapsed = !identical(item, latest);
    }
  }

  void setToolCollapsed(TranscriptItem item, bool collapsed) {
    if (!item.role.startsWith('Tool ·')) return;
    item
      ..collapsed = collapsed
      ..collapseManuallySet = true;
    notifyListeners();
  }

  void toggleToolCollapsed(TranscriptItem item) {
    setToolCollapsed(item, !item.collapsed);
  }

  void _restore(Map<String, dynamic> state) {
    messages.clear();
    _activeAssistantIndex = null;
    streaming = (state['Streaming'] ?? state['streaming'] ?? false) == true;
    yolo = (state['YOLO'] ?? state['yolo'] ?? false) == true;
    final stored = state['Messages'] ?? state['messages'] ?? const [];
    messages.addAll(_transcriptItems(stored));
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
          '${event['reason'] ?? 'Safety classifier requires manual approval.'}',
          event['approvalDetails'],
        );
        status = 'waiting for safety approval';
      case 'approval_resolved':
        if (pendingApproval?.id == '${event['approvalId'] ?? ''}') {
          pendingApproval = null;
          status = 'safety approval resolved';
        }
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
          _activeAssistantIndex = null;
        }
      case 'message_update':
        final message = event['message'];
        if (message is Map<String, dynamic> && message['role'] == 'assistant') {
          _replaceLastAssistant(message['content']);
        }
      case 'message_end':
        final message = event['message'];
        if (message is Map<String, dynamic> && message['role'] == 'assistant') {
          _replaceLastAssistant(message['content'], complete: true);
          _activeAssistantIndex = null;
          final usage = event['usage'];
          if (usage is Map<String, dynamic>) _setUsage(usage);
        }
      case 'tool_execution_start':
        final item = TranscriptItem(
          'Tool · ${_toolLabel('${event['toolName'] ?? ''}', event['arguments'])}',
          _toolMarkdown('${event['toolName'] ?? ''}', event['arguments'], ''),
          id: '${event['toolCallId'] ?? ''}',
          collapsed: false,
          toolName: '${event['toolName'] ?? ''}',
          toolArguments: event['arguments'],
        );
        messages.add(item);
        _applyAutomaticToolCollapse(messages);
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
    if (name == 'write' && arguments is Map) {
      final path = '${arguments['path'] ?? ''}';
      final content = '${arguments['content'] ?? ''}';
      final result = output.isEmpty ? '(waiting for result…)' : output;
      return '**File**\n\n${_codeBlock('text', path)}\n\n**Contents**\n\n${_codeBlock(_sourceLanguageForPath(path), content)}\n\n**Result**\n\n${_codeBlock('text', result)}';
    }
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
    if ((name == 'read' ||
            name == 'write' ||
            name == 'edit' ||
            name == 'replace') &&
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
        messages[index].toolOutput = text;
        messages[index].toolDetails = result['details'];
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

  void _replaceLastAssistant(dynamic content, {bool complete = false}) {
    final text = _contentText(content);
    final thinking = _contentThinking(content);
    if (text.isEmpty && thinking.isEmpty) return;

    final active = _activeAssistantIndex;
    if (active != null && active >= 0 && active < messages.length) {
      final item = messages[active];
      item.text = text;
      item.thinking = thinking;
      if (complete && thinking.isNotEmpty && text.isNotEmpty) {
        item.thinkingCollapsed = true;
      }
      return;
    }
    messages.add(
      TranscriptItem(
        'Assistant',
        text,
        thinking: thinking,
        thinkingCollapsed: complete && thinking.isNotEmpty && text.isNotEmpty,
      ),
    );
    _activeAssistantIndex = messages.length - 1;
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

  String _contentText(dynamic content) => _contentBlockText(content, 'text');

  String _contentThinking(dynamic content) =>
      _contentBlockText(content, 'thinking');

  String _contentBlockText(dynamic content, String type) {
    if (content is String) return type == 'text' ? content : '';
    if (content is! List) return '';
    return content
        .whereType<Map<String, dynamic>>()
        .where((block) => block['type'] == type)
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

class TrustedServer {
  const TrustedServer({
    required this.endpoint,
    required this.fingerprint,
    required this.agentId,
    this.pushAgentId = '',
  });
  final String endpoint;
  final String fingerprint;
  final String agentId;
  final String pushAgentId;

  Map<String, Object?> toJson() => {
    'endpoint': endpoint,
    'fingerprint': fingerprint,
    'agentId': agentId,
    'pushAgentId': pushAgentId,
  };
}

class ConnectionSlot {
  ConnectionSlot({
    required this.connection,
    required this.kind,
    required this.address,
    this.reconnectOnLaunch = false,
    this.pushAgentId = '',
    this.trustedServerFingerprint = '',
    this.trustedServerAgentId = '',
  });

  final AgentConnection connection;
  ConnectionKind kind;
  String address;
  bool reconnectOnLaunch;
  String pushAgentId;
  String trustedServerFingerprint;
  String trustedServerAgentId;
  Timer? reconnectTimer;
  int reconnectAttempt = 0;
  bool reconnectInProgress = false;

  void cancelReconnect({bool resetAttempt = true}) {
    reconnectTimer?.cancel();
    reconnectTimer = null;
    if (resetAttempt) reconnectAttempt = 0;
  }
}

Duration reconnectBackoffDelay(int attempt) {
  final seconds = switch (attempt.clamp(0, 5)) {
    0 => 1,
    1 => 2,
    2 => 4,
    3 => 8,
    4 => 16,
    _ => 30,
  };
  return Duration(seconds: seconds);
}

class AgentPage extends StatefulWidget {
  const AgentPage({super.key, this.connection});
  final AgentConnection? connection;

  @override
  State<AgentPage> createState() => _AgentPageState();
}

class _AgentPageState extends State<AgentPage> with WidgetsBindingObserver {
  bool get _autoCollapseHeader =>
      !kIsWeb &&
      (defaultTargetPlatform == TargetPlatform.iOS ||
          defaultTargetPlatform == TargetPlatform.android);

  bool get _showLocalAgentOption =>
      !flatpakFrontend &&
      !kIsWeb &&
      defaultTargetPlatform == TargetPlatform.macOS &&
      localAgentSupported;

  static const thinkingLevels = [
    'off',
    'minimal',
    'low',
    'medium',
    'high',
    'xhigh',
    'max',
  ];
  final List<ConnectionSlot> connections = [];
  final Map<String, TrustedServer> trustedServers = {};
  int activeConnectionIndex = 0;
  AgentConnection get agent => connections[activeConnectionIndex].connection;
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
  String? _approvalDialogRequestID;
  bool _followTranscript = true;
  bool _showScrollToBottom = false;
  bool _headerExpanded = true;
  bool _loadingOlderTranscript = false;
  bool _restoringConnections = false;
  bool _connectionRestoreComplete = false;
  bool _persistScheduled = false;
  Future<SharedPreferences> get _preferences => SharedPreferences.getInstance();
  static const _connectionStateKey = 'forge.connection_state.v1';
  PushRelayClient? _pushRelay;
  PushIdentity? _pushIdentity;
  StreamSubscription<PushEvent>? _pushEvents;
  final Map<AgentConnection, String> _pushAgentIDs = {};
  final Map<AgentConnection, String> _pushConnectionChallenges = {};
  final Map<AgentConnection, Map<String, dynamic>> _pushAgentPublicKeys = {};
  final Set<AgentConnection> _pushSetupStarted = {};
  final Set<AgentConnection> _pushAuthorizedConnections = {};
  bool _pushPairingDialogOpen = false;
  DeviceIdentity? _deviceIdentity;
  final Map<AgentConnection, String> _authClientNonces = {};
  final Map<AgentConnection, ClientSecureSession> _authEphemeralSessions = {};
  bool _serverTrustDialogOpen = false;

  @override
  void initState() {
    super.initState();
    _connectionRestoreComplete = widget.connection != null;
    final initialAgent = widget.connection ?? AgentConnection();
    connectionKind = _showLocalAgentOption
        ? ConnectionKind.local
        : webSocketOnlyClient
        ? ConnectionKind.websocket
        : nativeSocketsSupported
        ? ConnectionKind.unix
        : ConnectionKind.websocket;
    address.text = _defaultAddress(connectionKind);
    connections.add(
      ConnectionSlot(
        connection: initialAgent,
        kind: connectionKind,
        address: address.text,
      ),
    );
    _configureConnection(initialAgent);
    prompt.addListener(_promptChanged);
    HardwareKeyboard.instance.addHandler(_handleKey);
    WidgetsBinding.instance.addObserver(this);
    if (kIsWeb) {
      ClipboardEvents.instance?.registerPasteEventListener(_handleWebPaste);
    }
    unawaited(initializeSessionNotifications());
    if (deviceIdentitySupported) {
      unawaited(_initializeDeviceIdentity());
    }
    if (pushSupported) {
      _pushRelay = PushRelayClient();
      _pushEvents = nativePushEvents.listen(_handlePushEvent);
      unawaited(_initializePush());
    }
    if (widget.connection == null) {
      unawaited(_restoreConnections());
    }
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) {
      unawaited(_reconnectDisconnectedSlots());
    }
  }

  void _configureConnection(AgentConnection connection) {
    connection.onSessionNotification = (session, needsFeedback) =>
        _sessionNotification(connection, session, needsFeedback);
    connection.onConnected = (connection) {
      final slot = _slotForConnection(connection);
      slot?.cancelReconnect();
      _handlePushConnected(connection);
    };
    connection.onAuthenticationMessage = _handleAuthenticationMessage;
    connection.onPushResponse = _handlePushResponse;
    connection.onTranscriptLoaded = (_) => _resetTranscriptScroll();
    connection.onHistoryPrepended = (_) => _preserveScrollAfterHistory();
    connection.onDisconnected = (connection) {
      _authClientNonces.remove(connection);
      _authEphemeralSessions.remove(connection);
      _pushSetupStarted.remove(connection);
      _pushAuthorizedConnections.remove(connection);
      _pushAgentPublicKeys.remove(connection);
      final slot = _slotForConnection(connection);
      if (slot != null &&
          slot.reconnectOnLaunch &&
          slot.kind != ConnectionKind.local) {
        _scheduleReconnect(slot);
      }
    };
    connection.addListener(_changed);
  }

  Future<void> _disconnectWithoutRetry(AgentConnection connection) async {
    final slot = _slotForConnection(connection);
    if (slot != null) _cancelReconnect(slot, disable: true);
    await connection.disconnect();
  }

  Future<void> _initializeDeviceIdentity() async {
    try {
      _deviceIdentity = await getDeviceIdentity();
    } catch (error) {
      if (mounted) agent.setLocalStatus('Device identity setup failed: $error');
    }
  }

  void _handleAuthenticationMessage(
    AgentConnection connection,
    Map<String, dynamic> response,
  ) {
    unawaited(_handleAuthenticationMessageAsync(connection, response));
  }

  Future<void> _handleAuthenticationMessageAsync(
    AgentConnection connection,
    Map<String, dynamic> response,
  ) async {
    try {
      final type = '${response['type'] ?? ''}';
      if (type == 'auth_failed') {
        _authClientNonces.remove(connection);
        _authEphemeralSessions.remove(connection);
        connection.setLocalStatus('Authentication failed');
        await _disconnectWithoutRetry(connection);
        return;
      }
      var identity = _deviceIdentity;
      identity ??= await getDeviceIdentity();
      _deviceIdentity = identity;
      if (identity == null) {
        connection.setLocalStatus('This platform has no device identity');
        await _disconnectWithoutRetry(connection);
        return;
      }
      if (type == 'auth_required') {
        final nonce = await deviceIdentityRandomNonce();
        final ephemeral = await ClientSecureSession.create();
        _authClientNonces[connection] = nonce;
        _authEphemeralSessions[connection] = ephemeral;
        connection.send({
          'id': 'flutter-auth-probe',
          'type': 'auth_probe',
          'authProtocol': 'forge-mutual-p256-v1',
          'deviceId': identity.deviceId,
          'deviceFingerprint': identity.fingerprint,
          'clientNonce': nonce,
          'clientEphemeralKey': ephemeral.publicJwk,
          'cipher': 'P256-HKDF-SHA256-AES-256-GCM',
        });
        return;
      }
      if (type != 'auth_challenge') return;
      final publicKeyValue = response['agentPublicKey'];
      final serverEphemeralValue = response['serverEphemeralKey'];
      if (publicKeyValue is! Map ||
          serverEphemeralValue is! Map ||
          response['cipher'] != 'P256-HKDF-SHA256-AES-256-GCM') {
        throw StateError('Server key exchange parameters are missing');
      }
      final publicKey = publicKeyValue.map(
        (key, value) => MapEntry('$key', value),
      );
      final serverEphemeral = serverEphemeralValue.map(
        (key, value) => MapEntry('$key', value),
      );
      final ephemeral = _authEphemeralSessions[connection];
      if (ephemeral == null) {
        throw StateError('Client ephemeral key is missing');
      }
      final nonce = _authClientNonces[connection] ?? '';
      final connectionId = '${response['connectionId'] ?? ''}';
      final serverNonce = '${response['serverNonce'] ?? ''}';
      final serverAgentId = '${response['agentId'] ?? ''}';
      final fingerprint = '${response['agentFingerprint'] ?? ''}';
      final expiresAt = response['expiresAt'] as int? ?? 0;
      final deviceFingerprint = '${response['deviceFingerprint'] ?? ''}';
      if (nonce.isEmpty ||
          response['clientNonce'] != nonce ||
          response['deviceId'] != identity.deviceId ||
          deviceFingerprint != identity.fingerprint ||
          expiresAt <= DateTime.now().millisecondsSinceEpoch ~/ 1000) {
        throw StateError(
          'Server authentication challenge does not match this connection',
        );
      }
      final expectedFingerprint = await deviceIdentitySHA256(
        Uint8List.fromList(
          utf8.encode('P-256.${publicKey['x']}.${publicKey['y']}'),
        ),
      );
      if (expectedFingerprint != fingerprint) {
        throw StateError('Server public key fingerprint is invalid');
      }
      final serverPayload = [
        'FORGE-SERVER-AUTH-V1',
        connectionId,
        identity.deviceId,
        identity.fingerprint,
        nonce,
        serverNonce,
        serverAgentId,
        fingerprint,
        '$expiresAt',
        '${ephemeral.publicJwk['x']}',
        '${ephemeral.publicJwk['y']}',
        '${serverEphemeral['x']}',
        '${serverEphemeral['y']}',
        'P256-HKDF-SHA256-AES-256-GCM',
      ].join('\n');
      if (!await verifyDeviceIdentitySignature(
        publicKey: publicKey,
        payload: serverPayload,
        signature: '${response['serverSignature'] ?? ''}',
      )) {
        throw StateError('Server identity signature is invalid');
      }
      final slot = connections
          .where((item) => item.connection == connection)
          .firstOrNull;
      if (slot == null) throw StateError('Connection slot no longer exists');
      final endpoint = slot.connection.endpoint.isNotEmpty
          ? slot.connection.endpoint
          : slot.address;
      final rememberedTrust = trustedServers[endpoint];
      if (rememberedTrust != null) {
        slot.trustedServerFingerprint = rememberedTrust.fingerprint;
        slot.trustedServerAgentId = rememberedTrust.agentId;
      }
      if (slot.trustedServerFingerprint.isEmpty) {
        if (_serverTrustDialogOpen || !mounted) {
          await _disconnectWithoutRetry(connection);
          return;
        }
        _serverTrustDialogOpen = true;
        final trusted = await showDialog<bool>(
          context: context,
          barrierDismissible: false,
          builder: (context) => AlertDialog(
            title: const Text('Trust this agent server?'),
            content: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('Endpoint: ${slot.address}'),
                const SizedBox(height: 8),
                SelectableText('Agent ID: $serverAgentId'),
                const SizedBox(height: 8),
                const Text('P-256 fingerprint:'),
                SelectableText(fingerprint),
                const SizedBox(height: 12),
                const Text(
                  'Only trust this server if you recognize this endpoint.',
                ),
              ],
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(context, false),
                child: const Text('Cancel'),
              ),
              FilledButton(
                onPressed: () => Navigator.pop(context, true),
                child: const Text('Trust this server'),
              ),
            ],
          ),
        );
        _serverTrustDialogOpen = false;
        if (trusted != true) {
          await _disconnectWithoutRetry(connection);
          return;
        }
        slot.trustedServerFingerprint = fingerprint;
        slot.trustedServerAgentId = serverAgentId;
        trustedServers[endpoint] = TrustedServer(
          endpoint: endpoint,
          fingerprint: fingerprint,
          agentId: serverAgentId,
          pushAgentId: _pushAgentIDs[connection] ?? slot.pushAgentId,
        );
        // Trust is a security decision. Commit it to durable preferences before
        // sending the device proof or allowing update/termination to intervene.
        await _persistConnectionsNow();
      } else if (slot.trustedServerFingerprint != fingerprint ||
          slot.trustedServerAgentId != serverAgentId) {
        throw StateError(
          'Server identity changed. Expected ${slot.trustedServerFingerprint}, received $fingerprint',
        );
      }
      final devicePayload = [
        'FORGE-DEVICE-AUTH-V1',
        connectionId,
        identity.deviceId,
        identity.fingerprint,
        nonce,
        serverNonce,
        serverAgentId,
        fingerprint,
        '$expiresAt',
        '${ephemeral.publicJwk['x']}',
        '${ephemeral.publicJwk['y']}',
        '${serverEphemeral['x']}',
        '${serverEphemeral['y']}',
        'P256-HKDF-SHA256-AES-256-GCM',
      ].join('\n');
      connection._secureSession = await ephemeral.derive(
        connectionId: connectionId,
        serverPublicJwk: serverEphemeral,
        clientNonce: nonce,
        serverNonce: serverNonce,
        deviceFingerprint: identity.fingerprint,
        serverFingerprint: fingerprint,
      );
      connection._encryptionRequired = true;
      _authClientNonces.remove(connection);
      _authEphemeralSessions.remove(connection);
      // auth_response itself remains plaintext; key confirmation and all later
      // traffic use the newly derived secure session.
      final authTransport = connection._transport;
      authTransport?.send(
        jsonEncode({
          'id': 'flutter-auth-response',
          'type': 'auth_response',
          'authProtocol': 'forge-mutual-p256-v1',
          'connectionId': connectionId,
          'deviceId': identity.deviceId,
          'deviceFingerprint': identity.fingerprint,
          'signature': await signDeviceIdentityPayload(devicePayload),
        }),
      );
    } catch (error, stackTrace) {
      _authClientNonces.remove(connection);
      _authEphemeralSessions.remove(connection);
      debugPrint('Forge authentication failed: $error\n$stackTrace');
      await _disconnectWithoutRetry(connection);
      connection.setLocalStatus('Authentication failed: $error');
    }
  }

  Future<void> _initializePush() async {
    try {
      if (mounted) agent.setLocalStatus('setting up push identity…');
      _pushIdentity = await _pushRelay?.initialize();
      for (final slot in connections) {
        if (slot.connection.connected) _handlePushConnected(slot.connection);
      }
    } catch (error) {
      if (mounted) _showPushStatus(agent, 'Push setup failed: $error');
    }
  }

  void _showPushStatus(AgentConnection connection, String message) {
    connection.setLocalStatus(message);
    if (!mounted) return;
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(message)));
  }

  void _handlePushConnected(AgentConnection connection) {
    final identity = _pushIdentity;
    if (!pushSupported ||
        identity == null ||
        _pushSetupStarted.contains(connection)) {
      return;
    }
    _pushSetupStarted.add(connection);
    connection.setLocalStatus('connected · checking push authorization…');
    unawaited(
      () async {
        final challenge = await deviceIdentityRandomNonce();
        _pushConnectionChallenges[connection] = challenge;
        connection.send({
          'id': 'flutter-push-hello',
          'type': 'push_hello',
          'platform': pushPlatform,
          'deviceId': identity.deviceId,
          'deviceFingerprint': identity.fingerprint,
          'connectionChallenge': challenge,
        });
      }().catchError((Object error) {
        _pushSetupStarted.remove(connection);
        _showPushStatus(connection, 'Push handshake failed: $error');
      }),
    );
  }

  void _handlePushResponse(
    AgentConnection connection,
    Map<String, dynamic> response,
  ) {
    if (!pushSupported) return;
    unawaited(_handlePushResponseAsync(connection, response));
  }

  Future<void> _handlePushResponseAsync(
    AgentConnection connection,
    Map<String, dynamic> response,
  ) async {
    if (response['error'] case final String error when error.isNotEmpty) {
      _showPushStatus(connection, 'Push: $error');
      return;
    }
    switch (response['command']) {
      case 'push_hello':
        final agentId = '${response['agentId'] ?? ''}';
        final publicKeyValue = response['agentPublicKey'];
        final challenge = _pushConnectionChallenges.remove(connection) ?? '';
        final proof = '${response['connectionProof'] ?? ''}';
        if (agentId.isEmpty ||
            publicKeyValue is! Map ||
            challenge.isEmpty ||
            proof.isEmpty) {
          connection.setLocalStatus(
            'Push rejected an incomplete agent identity',
          );
          return;
        }
        final publicKey = publicKeyValue.map(
          (key, value) => MapEntry('$key', value),
        );
        final validProof = await verifyPushSignature(
          publicKey: publicKey,
          payload:
              'FORGE-AGENT-CONNECTION-V1\n$challenge\n${_pushIdentity?.deviceId ?? ''}',
          signature: proof,
        );
        if (!validProof) {
          connection.setLocalStatus('Push rejected the agent identity proof');
          return;
        }
        _pushAgentIDs[connection] = agentId;
        _pushAgentPublicKeys[connection] = publicKey;
        final slot = connections
            .where((slot) => slot.connection == connection)
            .firstOrNull;
        if (slot != null) slot.pushAgentId = agentId;
        _schedulePersistConnections();
        unawaited(_updateVisiblePushSession());
        if (response['pushAuthorized'] == true) {
          if (!await _acceptPushContentKey(
            agentId: agentId,
            agentPublicKey: publicKey,
            response: response,
          )) {
            connection.setLocalStatus('Push rejected the content key');
            return;
          }
          _pushAuthorizedConnections.add(connection);
          connection.setLocalStatus('connected · encrypted push enabled');
        } else {
          connection.send({
            'id': 'flutter-push-pair',
            'type': 'push_pair',
            'deviceId': _pushIdentity?.deviceId ?? '',
          });
        }
      case 'push_pair':
        connection.setLocalStatus('connected · push approval required');
        unawaited(_approvePushPairing(connection, response));
      case 'push_pair_complete':
        final agentId = _pushAgentIDs[connection] ?? '';
        final publicKey = _pushAgentPublicKeys[connection];
        if (agentId.isEmpty ||
            publicKey == null ||
            !await _acceptPushContentKey(
              agentId: agentId,
              agentPublicKey: publicKey,
              response: response,
            )) {
          connection.setLocalStatus('Push rejected the content key');
          return;
        }
        _pushAuthorizedConnections.add(connection);
        connection.setLocalStatus('connected · encrypted push enabled');
    }
  }

  Future<bool> _acceptPushContentKey({
    required String agentId,
    required Map<String, dynamic> agentPublicKey,
    required Map<String, dynamic> response,
  }) async {
    final deviceId = _pushIdentity?.deviceId ?? '';
    final keyId = '${response['pushKeyId'] ?? ''}';
    final key = '${response['pushKey'] ?? ''}';
    final signature = '${response['pushKeySignature'] ?? ''}';
    if (deviceId.isEmpty || keyId.isEmpty || key.isEmpty || signature.isEmpty) {
      return false;
    }
    final payload = [
      'FORGE-PUSH-KEY-V1',
      agentId,
      deviceId,
      keyId,
      key,
    ].join('\n');
    if (!await verifyPushSignature(
      publicKey: agentPublicKey,
      payload: payload,
      signature: signature,
    )) {
      return false;
    }
    try {
      final decoded = base64Url.decode(base64Url.normalize(key));
      if (decoded.length != 32) return false;
    } on FormatException {
      return false;
    }
    await storePushContentKey(
      agentId: agentId,
      deviceId: deviceId,
      keyId: keyId,
      key: key,
    );
    return true;
  }

  Future<void> _approvePushPairing(
    AgentConnection connection,
    Map<String, dynamic> response,
  ) async {
    if (_pushPairingDialogOpen || !mounted) return;
    final pairingId = '${response['pairingId'] ?? ''}';
    final code = '${response['verificationCode'] ?? ''}';
    if (pairingId.isEmpty || code.isEmpty) return;
    try {
      final pairing = await _pushRelay!.getPairing(pairingId);
      final agent = pairing['agent'];
      final agentName = agent is Map
          ? '${agent['displayName'] ?? 'Agent server'}'
          : 'Agent server';
      final fingerprint = agent is Map ? '${agent['fingerprint'] ?? ''}' : '';
      if (!mounted) return;
      _pushPairingDialogOpen = true;
      final approved = await showDialog<bool>(
        context: context,
        barrierDismissible: false,
        builder: (context) => AlertDialog(
          title: const Text('Allow device notifications?'),
          content: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                '$agentName wants to send approval-required and session-completed notifications to this device.',
              ),
              const SizedBox(height: 12),
              SelectableText('Verification code: $code'),
              if (fingerprint.isNotEmpty) ...[
                const SizedBox(height: 8),
                SelectableText('Agent fingerprint: $fingerprint'),
              ],
            ],
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(context, false),
              child: const Text('Not now'),
            ),
            FilledButton(
              onPressed: () => Navigator.pop(context, true),
              child: const Text('Allow notifications'),
            ),
          ],
        ),
      );
      _pushPairingDialogOpen = false;
      if (approved != true) return;
      await _pushRelay!.approvePairing(
        pairingId: pairingId,
        signingPayload: '${pairing['signingPayload'] ?? ''}',
        verificationCode: code,
      );
      connection.send({
        'id': 'flutter-push-pair-complete',
        'type': 'push_pair_complete',
        'pairingId': pairingId,
      });
    } catch (error) {
      _pushPairingDialogOpen = false;
      _showPushStatus(connection, 'Push pairing failed: $error');
    }
  }

  Future<void> _handlePushEvent(PushEvent event) async {
    if (event.type == 'apnsToken' || event.type == 'fcmToken') {
      await _pushRelay?.refreshIdentityAndToken();
      return;
    }
    if (event.type != 'notificationTap') return;
    final agentId = '${event.values['agentId'] ?? ''}';
    final sessionId = '${event.values['sessionId'] ?? ''}';
    if (agentId.isEmpty || sessionId.isEmpty) return;
    final index = connections.indexWhere(
      (slot) => _pushAgentIDs[slot.connection] == agentId,
    );
    if (index < 0) return;
    if (activeConnectionIndex != index && mounted) {
      setState(() {
        activeConnectionIndex = index;
        attachments.clear();
        _syncConnectionEditor();
      });
    }
    final connection = connections[index].connection;
    if (!connection.connected) {
      await connection.connect(
        connections[index].kind,
        connections[index].address,
      );
    }
    connection.switchSession(sessionId);
  }

  Future<void> _updateVisiblePushSession() async {
    if (!pushSupported || connections.isEmpty) return;
    await setVisiblePushSession(
      agentId: _pushAgentIDs[agent] ?? '',
      sessionId: agent.currentSession ?? '',
    );
  }

  Future<void> _restoreConnections() async {
    _restoringConnections = true;
    try {
      final preferences = await _preferences;
      final encoded = preferences.getString(_connectionStateKey);
      if (encoded == null || encoded.isEmpty || !mounted) return;
      final state = jsonDecode(encoded);
      if (state is! Map<String, dynamic>) return;
      final savedSlots = state['connections'];
      if (savedSlots is! List || savedSlots.isEmpty) return;

      trustedServers.clear();
      final savedTrusted = state['trustedServers'];
      if (savedTrusted is List) {
        for (final value in savedTrusted.whereType<Map<String, dynamic>>()) {
          final endpoint = '${value['endpoint'] ?? ''}'.trim();
          final fingerprint = '${value['fingerprint'] ?? ''}';
          if (endpoint.isEmpty || fingerprint.isEmpty) continue;
          trustedServers[endpoint] = TrustedServer(
            endpoint: endpoint,
            fingerprint: fingerprint,
            agentId: '${value['agentId'] ?? ''}',
            pushAgentId: '${value['pushAgentId'] ?? ''}',
          );
        }
      }

      final restored = <ConnectionSlot>[];
      for (final value in savedSlots.take(2)) {
        if (value is! Map<String, dynamic>) continue;
        final kindName = '${value['kind'] ?? ''}';
        final kind = ConnectionKind.values
            .where((item) => item.name == kindName)
            .firstOrNull;
        final endpoint = '${value['address'] ?? ''}'.trim();
        if (kind == null ||
            endpoint.isEmpty ||
            kind == ConnectionKind.local ||
            (flatpakFrontend &&
                (kind != ConnectionKind.websocket ||
                    _isFlatpakLoopbackEndpoint(endpoint)))) {
          continue;
        }
        final connection = AgentConnection()
          ..endpointKind = kind
          ..endpoint = endpoint
          ..currentSession = switch (value['session']) {
            final String session when session.isNotEmpty => session,
            _ => null,
          };
        _configureConnection(connection);
        final legacyFingerprint = '${value['trustedServerFingerprint'] ?? ''}';
        final legacyAgentId = '${value['trustedServerAgentId'] ?? ''}';
        final pushAgentId = '${value['pushAgentId'] ?? ''}';
        if (legacyFingerprint.isNotEmpty &&
            !trustedServers.containsKey(endpoint)) {
          trustedServers[endpoint] = TrustedServer(
            endpoint: endpoint,
            fingerprint: legacyFingerprint,
            agentId: legacyAgentId,
            pushAgentId: pushAgentId,
          );
        }
        final trust = trustedServers[endpoint];
        restored.add(
          ConnectionSlot(
            connection: connection,
            kind: kind,
            address: endpoint,
            reconnectOnLaunch: value['reconnect'] == true,
            pushAgentId: pushAgentId,
            trustedServerFingerprint: trust?.fingerprint ?? '',
            trustedServerAgentId: trust?.agentId ?? '',
          ),
        );
      }
      if (restored.isEmpty || !mounted) {
        for (final slot in restored) {
          slot.cancelReconnect();
          slot.connection.removeListener(_changed);
          slot.connection.dispose();
        }
        return;
      }
      for (final slot in connections) {
        slot.cancelReconnect();
        slot.connection.removeListener(_changed);
        slot.connection.dispose();
      }
      connections
        ..clear()
        ..addAll(restored);
      for (final slot in connections) {
        if (slot.pushAgentId.isNotEmpty) {
          _pushAgentIDs[slot.connection] = slot.pushAgentId;
        }
      }
      final selected = state['activeConnection'];
      activeConnectionIndex = selected is int
          ? selected.clamp(0, connections.length - 1)
          : 0;
      _syncConnectionEditor();
      if (mounted) setState(() {});
      await _reconnectDisconnectedSlots();
    } catch (_) {
      // Ignore corrupt or unavailable preferences and keep the default slot.
    } finally {
      _restoringConnections = false;
      _connectionRestoreComplete = true;
      _schedulePersistConnections();
    }
  }

  Map<String, Object?> _connectionState() => {
    'activeConnection': activeConnectionIndex,
    'trustedServers': [
      for (final trust in trustedServers.values) trust.toJson(),
    ],
    'connections': [
      for (final slot in connections)
        {
          'kind': slot.kind.name,
          'address': slot.connection.endpoint.isNotEmpty
              ? slot.connection.endpoint
              : slot.address,
          'session': slot.connection.currentSession,
          'reconnect': slot.reconnectOnLaunch,
          'pushAgentId': _pushAgentIDs[slot.connection] ?? slot.pushAgentId,
          'trustedServerFingerprint': slot.trustedServerFingerprint,
          'trustedServerAgentId': slot.trustedServerAgentId,
        },
    ],
  };

  Future<void> _persistConnectionsNow() async {
    if (widget.connection != null) return;
    final preferences = await _preferences;
    await preferences.setString(
      _connectionStateKey,
      jsonEncode(_connectionState()),
    );
  }

  void _schedulePersistConnections() {
    if (!_connectionRestoreComplete ||
        _restoringConnections ||
        widget.connection != null ||
        _persistScheduled) {
      return;
    }
    _persistScheduled = true;
    scheduleMicrotask(() async {
      _persistScheduled = false;
      await _persistConnectionsNow();
    });
  }

  ConnectionSlot? _slotForConnection(AgentConnection connection) =>
      connections.where((slot) => slot.connection == connection).firstOrNull;

  void _cancelReconnect(ConnectionSlot slot, {bool disable = false}) {
    if (disable) slot.reconnectOnLaunch = false;
    slot.cancelReconnect();
  }

  void _scheduleReconnect(ConnectionSlot slot, {bool immediate = false}) {
    if (!mounted ||
        !slot.reconnectOnLaunch ||
        slot.kind == ConnectionKind.local ||
        slot.connection.connected ||
        slot.reconnectInProgress ||
        slot.reconnectTimer != null ||
        !connections.contains(slot)) {
      return;
    }
    final delay = immediate
        ? Duration.zero
        : reconnectBackoffDelay(slot.reconnectAttempt++);
    if (delay > Duration.zero) {
      slot.connection.setLocalStatus(
        'disconnected · reconnecting in ${delay.inSeconds}s',
      );
    }
    slot.reconnectTimer = Timer(delay, () {
      slot.reconnectTimer = null;
      unawaited(_attemptReconnect(slot));
    });
  }

  Future<void> _attemptReconnect(ConnectionSlot slot) async {
    if (!mounted ||
        !connections.contains(slot) ||
        !slot.reconnectOnLaunch ||
        slot.kind == ConnectionKind.local ||
        slot.connection.connected ||
        slot.reconnectInProgress) {
      return;
    }
    final endpoint = slot.address.trim();
    if (endpoint.isEmpty) return;
    slot.reconnectInProgress = true;
    try {
      await slot.connection.connect(slot.kind, endpoint);
    } finally {
      slot.reconnectInProgress = false;
    }
    if (slot.connection.connected) {
      slot.cancelReconnect();
    } else {
      _scheduleReconnect(slot);
    }
  }

  Future<void> _reconnectDisconnectedSlots() async {
    for (final slot in List<ConnectionSlot>.from(connections)) {
      if (!mounted ||
          !slot.reconnectOnLaunch ||
          slot.connection.connected ||
          slot.kind == ConnectionKind.local) {
        continue;
      }
      // Resume should not wait for an old background timer. Try immediately,
      // then continue the same capped exponential sequence if it still fails.
      slot.cancelReconnect(resetAttempt: false);
      _scheduleReconnect(slot, immediate: true);
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

  String _approvalBashCommand(
    AgentConnection connection,
    ApprovalRequest request,
  ) {
    if (request.tool != 'bash') return '';
    for (var index = connection.messages.length - 1; index >= 0; index--) {
      final item = connection.messages[index];
      if (item.id != request.toolCallID || item.toolName != 'bash') continue;
      final arguments = item.toolArguments;
      if (arguments is Map) return '${arguments['command'] ?? ''}';
    }
    return '';
  }

  Widget? _approvalMutationPreview(ApprovalRequest request) {
    final details = request.details;
    if (details is! Map) return null;
    final path = '${details['path'] ?? ''}';
    if (request.tool == 'write') {
      return KeyedSubtree(
        key: const ValueKey('approval-write-preview'),
        child: _writeToolPreview(
          TranscriptItem(
            'Tool · write · $path',
            '',
            toolName: 'write',
            toolArguments: {
              'path': path,
              'content': '${details['content'] ?? ''}',
            },
            toolOutput: 'Pending safety approval.',
          ),
        ),
      );
    }
    if (request.tool == 'replace' && details['error'] == null) {
      return KeyedSubtree(
        key: const ValueKey('approval-replace-preview'),
        child: _replaceToolPreview(
          TranscriptItem(
            'Tool · replace · $path',
            '',
            toolName: 'replace',
            toolArguments: {'path': path},
            toolDetails: details,
            toolOutput: 'Pending safety approval.',
          ),
        ),
      );
    }
    if (request.tool == 'cron') {
      return KeyedSubtree(
        key: const ValueKey('approval-cron-preview'),
        child: _cronToolPreview(
          TranscriptItem(
            'Tool · cron',
            '',
            toolName: 'cron',
            toolArguments: details,
            toolDetails: details,
            toolOutput: 'Pending safety approval.',
          ),
        ),
      );
    }
    return null;
  }

  Widget _approvalToast(
    BuildContext context,
    ApprovalRequest request,
    String bashCommand,
  ) {
    final scheme = Theme.of(context).colorScheme;
    final dark = Theme.of(context).brightness == Brightness.dark;
    final syntaxTheme = dark ? atomOneDarkReasonableTheme : atomOneLightTheme;
    final highlighted = _syntaxSpans(bashCommand, 'bash', syntaxTheme);
    final codeStyle = TextStyle(
      fontFamily: 'monospace',
      fontSize: 13,
      height: 1.45,
      color: syntaxTheme['root']?.color,
    );
    final mutationPreview = _approvalMutationPreview(request);
    final bottomInset = MediaQuery.paddingOf(context).bottom;
    const topRadius = Radius.circular(30);

    return Material(
      color: Colors.transparent,
      child: Align(
        alignment: Alignment.bottomCenter,
        child: ClipRRect(
          borderRadius: const BorderRadius.only(
            topLeft: topRadius,
            topRight: topRadius,
          ),
          child: BackdropFilter(
            filter: ui.ImageFilter.blur(sigmaX: 28, sigmaY: 28),
            child: Material(
              key: const ValueKey('approval-toast'),
              color: (dark ? const Color(0xF5222225) : const Color(0xFAF7F7F8))
                  .withValues(alpha: .96),
              child: SizedBox(
                key: const ValueKey('approval-bottom-sheet'),
                width: double.infinity,
                child: ConstrainedBox(
                  constraints: BoxConstraints(
                    maxHeight: MediaQuery.sizeOf(context).height * .86,
                  ),
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      const SizedBox(height: 9),
                      Container(
                        width: 38,
                        height: 5,
                        decoration: BoxDecoration(
                          color: scheme.onSurfaceVariant.withValues(alpha: .35),
                          borderRadius: BorderRadius.circular(99),
                        ),
                      ),
                      Padding(
                        padding: const EdgeInsets.fromLTRB(20, 15, 20, 14),
                        child: Row(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Container(
                              width: 42,
                              height: 42,
                              decoration: BoxDecoration(
                                color: scheme.errorContainer,
                                shape: BoxShape.circle,
                              ),
                              child: Icon(
                                Icons.shield_outlined,
                                color: scheme.onErrorContainer,
                              ),
                            ),
                            const SizedBox(width: 12),
                            const Expanded(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    'Safety classifier approval required?',
                                    style: TextStyle(
                                      fontSize: 18,
                                      fontWeight: FontWeight.w700,
                                      letterSpacing: -.2,
                                    ),
                                  ),
                                  SizedBox(height: 3),
                                  Text(
                                    'Forge needs your permission to continue.',
                                    style: TextStyle(fontSize: 13),
                                  ),
                                ],
                              ),
                            ),
                          ],
                        ),
                      ),
                      Divider(height: 1, color: scheme.outlineVariant),
                      Flexible(
                        child: SingleChildScrollView(
                          padding: const EdgeInsets.fromLTRB(20, 16, 20, 18),
                          child: Column(
                            crossAxisAlignment: CrossAxisAlignment.start,
                            children: [
                              if (bashCommand.isNotEmpty) ...[
                                Row(
                                  children: [
                                    Icon(
                                      Icons.terminal,
                                      size: 16,
                                      color: scheme.onSurfaceVariant,
                                    ),
                                    const SizedBox(width: 6),
                                    Text(
                                      'Bash command',
                                      style: Theme.of(
                                        context,
                                      ).textTheme.labelLarge,
                                    ),
                                  ],
                                ),
                                const SizedBox(height: 7),
                                Container(
                                  key: const ValueKey('approval-bash-code'),
                                  width: double.infinity,
                                  constraints: const BoxConstraints(
                                    maxHeight: 300,
                                  ),
                                  clipBehavior: Clip.antiAlias,
                                  decoration: BoxDecoration(
                                    color:
                                        syntaxTheme['root']?.backgroundColor ??
                                        scheme.surfaceContainerLowest,
                                    borderRadius: BorderRadius.circular(14),
                                    border: Border.all(
                                      color: scheme.outlineVariant,
                                    ),
                                  ),
                                  child: Scrollbar(
                                    child: SingleChildScrollView(
                                      padding: const EdgeInsets.all(13),
                                      child: SingleChildScrollView(
                                        scrollDirection: Axis.horizontal,
                                        child: SelectableText.rich(
                                          TextSpan(
                                            style: codeStyle,
                                            children: highlighted,
                                          ),
                                        ),
                                      ),
                                    ),
                                  ),
                                ),
                              ],
                              if (mutationPreview != null) ...[
                                if (bashCommand.isNotEmpty)
                                  const SizedBox(height: 16),
                                mutationPreview,
                              ],
                              if ((request.tool != 'bash' ||
                                      bashCommand.isEmpty) &&
                                  request.description.trim().isNotEmpty) ...[
                                const SizedBox(height: 14),
                                SelectableText(
                                  request.description,
                                  style: const TextStyle(
                                    fontSize: 14,
                                    height: 1.35,
                                  ),
                                ),
                              ],
                              const SizedBox(height: 14),
                              Text(
                                'Why approval is required',
                                style: Theme.of(context).textTheme.labelLarge,
                              ),
                              const SizedBox(height: 5),
                              SelectableText(
                                request.reason,
                                style: TextStyle(
                                  color: scheme.onSurfaceVariant,
                                  fontSize: 13,
                                  height: 1.35,
                                ),
                              ),
                            ],
                          ),
                        ),
                      ),
                      Divider(height: 1, color: scheme.outlineVariant),
                      Padding(
                        padding: EdgeInsets.fromLTRB(
                          12,
                          10,
                          12,
                          10 + bottomInset,
                        ),
                        child: Row(
                          children: [
                            Expanded(
                              child: SizedBox(
                                height: 54,
                                child: OutlinedButton.icon(
                                  key: const ValueKey('approval-reject'),
                                  onPressed: () =>
                                      Navigator.pop(context, false),
                                  style: OutlinedButton.styleFrom(
                                    foregroundColor: scheme.error,
                                    side: BorderSide(
                                      color: scheme.error.withValues(alpha: .5),
                                    ),
                                    shape: RoundedRectangleBorder(
                                      borderRadius: BorderRadius.circular(16),
                                    ),
                                  ),
                                  icon: const Icon(Icons.close_rounded),
                                  label: const Text('Reject'),
                                ),
                              ),
                            ),
                            const SizedBox(width: 10),
                            Expanded(
                              child: SizedBox(
                                height: 54,
                                child: FilledButton.icon(
                                  key: const ValueKey('approval-approve'),
                                  onPressed: () => Navigator.pop(context, true),
                                  style: FilledButton.styleFrom(
                                    shape: RoundedRectangleBorder(
                                      borderRadius: BorderRadius.circular(16),
                                    ),
                                  ),
                                  icon: const Icon(Icons.check_rounded),
                                  label: const Text('Approve once'),
                                ),
                              ),
                            ),
                          ],
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }

  Future<void> _showApproval(
    AgentConnection connection,
    ApprovalRequest request,
  ) async {
    if (!mounted || approvalDialogOpen) return;
    approvalDialogOpen = true;
    _approvalDialogRequestID = request.id;
    final bashCommand = _approvalBashCommand(connection, request);
    final approved = await showGeneralDialog<bool>(
      context: context,
      barrierDismissible: false,
      barrierLabel: 'Safety approval required',
      barrierColor: Colors.black.withValues(alpha: .34),
      transitionDuration: const Duration(milliseconds: 240),
      pageBuilder: (context, animation, secondaryAnimation) =>
          _approvalToast(context, request, bashCommand),
      transitionBuilder: (context, animation, secondaryAnimation, child) {
        final curved = CurvedAnimation(
          parent: animation,
          curve: Curves.easeOutCubic,
          reverseCurve: Curves.easeInCubic,
        );
        return FadeTransition(
          opacity: curved,
          child: SlideTransition(
            position: Tween<Offset>(
              begin: const Offset(0, .08),
              end: Offset.zero,
            ).animate(curved),
            child: child,
          ),
        );
      },
    );
    approvalDialogOpen = false;
    _approvalDialogRequestID = null;
    if (approved != null && connection.pendingApproval?.id == request.id) {
      connection.resolveApproval(request.id, approved);
    }
    if (shownApprovalID == request.id) shownApprovalID = null;
    _schedulePendingApprovalDialog();
  }

  void _schedulePendingApprovalDialog() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || approvalDialogOpen) return;
      final connection = agent;
      final pending = connection.pendingApproval;
      if (pending == null) return;
      shownApprovalID = pending.id;
      unawaited(_showApproval(connection, pending));
    });
  }

  bool _isFlatpakLoopbackEndpoint(String endpoint) {
    if (!flatpakFrontend) return false;
    final host = Uri.tryParse(endpoint)?.host.toLowerCase() ?? '';
    return host == 'localhost' ||
        host == '127.0.0.1' ||
        host == '::1' ||
        host == '0.0.0.0' ||
        host == '::';
  }

  String _defaultAddress(ConnectionKind kind) => switch (kind) {
    ConnectionKind.local => defaultLocalAddress,
    ConnectionKind.unix => defaultUnixAddress,
    ConnectionKind.tcp => '127.0.0.1:7346',
    ConnectionKind.websocket => defaultWebSocketAddress,
  };

  int get _connectedCount =>
      connections.where((slot) => slot.connection.connected).length;

  List<String> get _trustedEndpoints => trustedServers.keys.toList();

  void _selectTrustedEndpoint(String endpoint) {
    final trust = trustedServers[endpoint];
    if (trust == null) return;
    setState(() {
      address.text = endpoint;
      connectionKind = ConnectionKind.websocket;
      final activeSlot = connections[activeConnectionIndex];
      activeSlot
        ..address = endpoint
        ..kind = ConnectionKind.websocket
        ..trustedServerFingerprint = trust.fingerprint
        ..trustedServerAgentId = trust.agentId
        ..pushAgentId = trust.pushAgentId;
      _pushAgentIDs[activeSlot.connection] = trust.pushAgentId;
    });
    _schedulePersistConnections();
  }

  String _connectionTitle(ConnectionSlot slot, int index) {
    final endpoint = slot.connection.endpoint.isNotEmpty
        ? slot.connection.endpoint
        : slot.address;
    return endpoint.isEmpty
        ? 'Connection ${index + 1}'
        : 'Connection ${index + 1} · $endpoint';
  }

  void _syncConnectionEditor() {
    final slot = connections[activeConnectionIndex];
    connectionKind = slot.kind;
    address.text = slot.connection.endpoint.isNotEmpty
        ? slot.connection.endpoint
        : slot.address;
    model = agent.currentModel;
    thinking = agent.currentThinking;
    showConnectionSettings = !agent.connected;
    wasConnected = agent.connected;
  }

  void _switchConnection(int index) {
    if (index < 0 || index >= connections.length) return;
    _followTranscript = true;
    _showScrollToBottom = false;
    _headerExpanded = true;
    setState(() {
      activeConnectionIndex = index;
      attachments.clear();
      _syncConnectionEditor();
    });
    Navigator.of(context).pop();
    _schedulePersistConnections();
    _changed();
    _resetTranscriptScroll();
  }

  void _addConnection() {
    if (connections.length >= 2) return;
    final kind = ConnectionKind.websocket;
    final connection = AgentConnection();
    _configureConnection(connection);
    setState(() {
      connections.add(
        ConnectionSlot(
          connection: connection,
          kind: kind,
          address: _defaultAddress(kind),
        ),
      );
      activeConnectionIndex = connections.length - 1;
      attachments.clear();
      _syncConnectionEditor();
    });
    Navigator.of(context).pop();
    _schedulePersistConnections();
  }

  Future<void> _closeConnection(int index) async {
    if (index < 0 || index >= connections.length) return;
    final slot = connections[index];
    if (!await _confirmLocalStop(slot)) return;
    _cancelReconnect(slot, disable: true);
    await slot.connection.disconnect();
    if (!mounted) return;
    if (connections.length == 1) {
      slot.reconnectOnLaunch = false;
      setState(() => _syncConnectionEditor());
      _schedulePersistConnections();
      return;
    }
    _authClientNonces.remove(slot.connection);
    _authEphemeralSessions.remove(slot.connection);
    slot.cancelReconnect();
    slot.connection.removeListener(_changed);
    slot.connection.dispose();
    setState(() {
      connections.removeAt(index);
      if (activeConnectionIndex >= connections.length) {
        activeConnectionIndex = connections.length - 1;
      } else if (index < activeConnectionIndex) {
        activeConnectionIndex--;
      }
      attachments.clear();
      _syncConnectionEditor();
    });
    _schedulePersistConnections();
  }

  Future<bool> _confirmLocalStop(ConnectionSlot slot) async {
    if (!slot.connection.connected ||
        slot.connection.endpointKind != ConnectionKind.local) {
      return true;
    }
    return await showDialog<bool>(
          context: context,
          builder: (context) => AlertDialog(
            title: const Text('Stop local agent?'),
            content: const Text(
              'Disconnecting this local connection stops the bundled '
              'pi-go-agent, including any active response or tool. Remote '
              'servers are never stopped when their connection closes.',
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(context, false),
                child: const Text('Keep connected'),
              ),
              FilledButton(
                onPressed: () => Navigator.pop(context, true),
                child: const Text('Disconnect and stop'),
              ),
            ],
          ),
        ) ??
        false;
  }

  Future<void> _disconnectActive() async {
    final slot = connections[activeConnectionIndex];
    if (await _confirmLocalStop(slot)) {
      _cancelReconnect(slot, disable: true);
      _schedulePersistConnections();
      await slot.connection.disconnect();
    }
  }

  Future<void> _connectSelected() async {
    final slot = connections[activeConnectionIndex];
    slot.kind = connectionKind;
    slot.address = address.text.trim();
    if (connectionKind == ConnectionKind.local) {
      final proceed = await showDialog<bool>(
        context: context,
        builder: (context) => AlertDialog(
          title: const Text('Start bundled local agent?'),
          content: const Text(
            'Forge will start its bundled pi-go-agent with your home directory '
            'as the workspace. Disconnecting this local connection or closing '
            'Forge stops that agent and aborts any active work.',
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(context, false),
              child: const Text('Cancel'),
            ),
            FilledButton(
              onPressed: () => Navigator.pop(context, true),
              child: const Text('Start local agent'),
            ),
          ],
        ),
      );
      if (proceed != true) return;
    }
    slot.reconnectOnLaunch = true;
    slot.cancelReconnect();
    _schedulePersistConnections();
    await slot.connection.connect(connectionKind, slot.address);
    if (!slot.connection.connected) _scheduleReconnect(slot);
    _schedulePersistConnections();
  }

  Future<void> _forgetTrustedServer(String endpoint) async {
    final trust = trustedServers[endpoint];
    if (trust == null) return;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('Forget trusted server identity?'),
        content: Text(
          'The next connection to $endpoint will ask you to trust its server fingerprint again.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Forget identity'),
          ),
        ],
      ),
    );
    if (confirmed != true) return;
    for (final slot in connections) {
      final slotEndpoint = slot.connection.endpoint.isNotEmpty
          ? slot.connection.endpoint
          : slot.address;
      if (slotEndpoint != endpoint) continue;
      _cancelReconnect(slot, disable: true);
      await slot.connection.disconnect();
      slot
        ..trustedServerFingerprint = ''
        ..trustedServerAgentId = '';
    }
    trustedServers.remove(endpoint);
    await _persistConnectionsNow();
  }

  Future<void> _showTrustedServers() async {
    await showDialog<void>(
      context: context,
      builder: (dialogContext) => StatefulBuilder(
        builder: (dialogContext, setDialogState) {
          final trusted = trustedServers.values.toList();
          return AlertDialog(
            title: const Row(
              children: [
                Icon(Icons.verified_user_outlined),
                SizedBox(width: 10),
                Expanded(child: Text('Trusted servers')),
              ],
            ),
            content: SizedBox(
              width: 620,
              child: trusted.isEmpty
                  ? const Padding(
                      padding: EdgeInsets.symmetric(vertical: 28),
                      child: Center(
                        child: Text('No trusted server identities'),
                      ),
                    )
                  : ListView.separated(
                      shrinkWrap: true,
                      itemCount: trusted.length,
                      separatorBuilder: (_, _) => const Divider(height: 24),
                      itemBuilder: (context, itemIndex) {
                        final trust = trusted[itemIndex];
                        final endpoint = trust.endpoint;
                        return Column(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Row(
                              children: [
                                const Icon(Icons.dns_outlined, size: 20),
                                const SizedBox(width: 8),
                                Expanded(
                                  child: Text(
                                    endpoint,
                                    style: Theme.of(
                                      context,
                                    ).textTheme.titleSmall,
                                  ),
                                ),
                                IconButton(
                                  tooltip: 'Delete trusted server',
                                  icon: const Icon(Icons.delete_outline),
                                  onPressed: () async {
                                    await _forgetTrustedServer(endpoint);
                                    if (dialogContext.mounted) {
                                      setDialogState(() {});
                                    }
                                  },
                                ),
                              ],
                            ),
                            if (trust.agentId.isNotEmpty) ...[
                              const SizedBox(height: 8),
                              Text(
                                'Agent ID',
                                style: Theme.of(context).textTheme.labelMedium,
                              ),
                              SelectableText(trust.agentId),
                            ],
                            const SizedBox(height: 8),
                            Text(
                              'P-256 fingerprint',
                              style: Theme.of(context).textTheme.labelMedium,
                            ),
                            SelectableText(trust.fingerprint),
                          ],
                        );
                      },
                    ),
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.of(dialogContext).pop(),
                child: const Text('Done'),
              ),
            ],
          );
        },
      ),
    );
  }

  Future<void> _showConnections() async {
    await showDialog<void>(
      context: context,
      builder: (dialogContext) => StatefulBuilder(
        builder: (dialogContext, setDialogState) => AlertDialog(
          title: Row(
            children: [
              const Expanded(child: Text('Connections')),
              IconButton(
                tooltip: 'Manage trusted servers',
                onPressed: _showTrustedServers,
                icon: const Icon(Icons.key_outlined),
              ),
            ],
          ),
          content: SizedBox(
            width: 620,
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                for (var index = 0; index < connections.length; index++)
                  ListTile(
                    selected: index == activeConnectionIndex,
                    leading: Icon(
                      connections[index].connection.connected
                          ? Icons.lan_outlined
                          : Icons.link_off,
                    ),
                    title: Text(_connectionTitle(connections[index], index)),
                    subtitle: Text(
                      connections[index].connection.connected
                          ? connections[index].connection.status
                          : 'Not connected',
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                    ),
                    onTap: () => _switchConnection(index),
                    trailing: IconButton(
                      tooltip: 'Close connection',
                      icon: const Icon(Icons.close),
                      onPressed: () async {
                        await _closeConnection(index);
                        if (!dialogContext.mounted) return;
                        if (connections.isEmpty) {
                          Navigator.of(dialogContext).pop();
                        } else {
                          setDialogState(() {});
                        }
                      },
                    ),
                  ),
              ],
            ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.of(dialogContext).pop(),
              child: const Text('Done'),
            ),
            FilledButton.icon(
              onPressed: connections.length < 2 ? _addConnection : null,
              icon: const Icon(Icons.add),
              label: const Text('New connection'),
            ),
          ],
        ),
      ),
    );
  }

  Widget _connectionManagerButton(bool compact) => Badge(
    isLabelVisible: _connectedCount > 0,
    label: Text('$_connectedCount'),
    child: IconButton.outlined(
      padding: compact ? EdgeInsets.zero : null,
      constraints: compact
          ? const BoxConstraints.tightFor(width: 40, height: 40)
          : null,
      tooltip: 'Manage connections',
      onPressed: _showConnections,
      icon: Icon(agent.connected ? Icons.lan_outlined : Icons.link_off),
    ),
  );

  void _sessionNotification(
    AgentConnection connection,
    String session,
    bool needsFeedback,
  ) {
    if (pushSupported && _pushAuthorizedConnections.contains(connection)) {
      return;
    }
    for (var index = 0; index < connections.length; index++) {
      final slot = connections[index];
      if (slot.connection == connection) {
        final selected =
            index == activeConnectionIndex &&
            connection.currentSession == session;
        unawaited(
          showSessionNotification(
            connection: _connectionTitle(slot, index),
            session: session,
            needsFeedback: needsFeedback,
            suppressWhenForeground: selected,
          ),
        );
        break;
      }
    }
  }

  void _changed() {
    if (!mounted) return;
    model = agent.currentModel;
    thinking = agent.currentThinking;
    if (agent.connected && !wasConnected) {
      showConnectionSettings = false;
    }
    if (!agent.connected) {
      showConnectionSettings = true;
      _headerExpanded = true;
    }
    wasConnected = agent.connected;
    _schedulePersistConnections();
    unawaited(_updateVisiblePushSession());
    setState(() {});
    final approval = agent.pendingApproval;
    if (approvalDialogOpen) {
      final displayedID = _approvalDialogRequestID;
      if (approval?.id != displayedID) {
        WidgetsBinding.instance.addPostFrameCallback((_) {
          // An approval may be resolved by another connected device, or the
          // active session may advance to its next approval. Close only the
          // dialog this callback was scheduled for and never treat it as a
          // rejection.
          if (mounted &&
              approvalDialogOpen &&
              _approvalDialogRequestID == displayedID &&
              agent.pendingApproval?.id != displayedID &&
              Navigator.of(context, rootNavigator: true).canPop()) {
            Navigator.of(context, rootNavigator: true).pop();
          }
        });
      }
    } else if (approval != null && approval.id != shownApprovalID) {
      shownApprovalID = approval.id;
      final connection = agent;
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (mounted &&
            !approvalDialogOpen &&
            connection == agent &&
            connection.pendingApproval?.id == approval.id) {
          unawaited(_showApproval(connection, approval));
        }
      });
    }
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_followTranscript && scroll.hasClients) {
        scroll.animateTo(
          scroll.position.maxScrollExtent,
          duration: const Duration(milliseconds: 180),
          curve: Curves.easeOut,
        );
      }
    });
  }

  void _resetTranscriptScroll() {
    _loadingOlderTranscript = false;
    _followTranscript = true;
    if (mounted && _showScrollToBottom) {
      setState(() => _showScrollToBottom = false);
    } else {
      _showScrollToBottom = false;
    }
    _scrollToBottom(animate: false);
  }

  void _scrollToBottom({bool animate = true}) {
    _followTranscript = true;
    if (mounted && _showScrollToBottom) {
      setState(() => _showScrollToBottom = false);
    } else {
      _showScrollToBottom = false;
    }
    _settleAtTranscriptBottom(animate: animate);
  }

  void _settleAtTranscriptBottom({required bool animate, int attempts = 6}) {
    WidgetsBinding.instance.addPostFrameCallback((_) async {
      if (!mounted || !_followTranscript || !scroll.hasClients) return;
      final bottom = scroll.position.maxScrollExtent;
      if (animate) {
        await scroll.animateTo(
          bottom,
          duration: const Duration(milliseconds: 260),
          curve: Curves.easeOut,
        );
      } else {
        scroll.jumpTo(bottom);
      }
      if (attempts > 1 &&
          mounted &&
          _followTranscript &&
          scroll.hasClients &&
          scroll.position.extentAfter > 1) {
        // A lazily built ListView can discover more extent only after reaching
        // its estimated bottom. Repeat until the real final child is laid out.
        _settleAtTranscriptBottom(animate: false, attempts: attempts - 1);
      }
    });
  }

  double _historyOldOffset = 0;
  double _historyOldExtent = 0;

  void _loadOlderTranscript() {
    if (_loadingOlderTranscript || !scroll.hasClients) return;
    _loadingOlderTranscript = true;
    _historyOldOffset = scroll.offset;
    _historyOldExtent = scroll.position.maxScrollExtent;
    agent.loadOlderTranscript();
    Future<void>.delayed(const Duration(seconds: 5), () {
      if (mounted) _loadingOlderTranscript = false;
    });
  }

  void _preserveScrollAfterHistory() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !scroll.hasClients) {
        _loadingOlderTranscript = false;
        return;
      }
      final addedExtent = scroll.position.maxScrollExtent - _historyOldExtent;
      scroll.jumpTo(
        (_historyOldOffset + addedExtent).clamp(
          scroll.position.minScrollExtent,
          scroll.position.maxScrollExtent,
        ),
      );
      _loadingOlderTranscript = false;
    });
  }

  bool _handleTranscriptScroll(ScrollNotification notification) {
    if (notification.depth != 0) return false;
    final dragDelta = switch (notification) {
      ScrollUpdateNotification(:final dragDetails) => dragDetails?.primaryDelta,
      OverscrollNotification(:final dragDetails) => dragDetails?.primaryDelta,
      _ => null,
    };
    final userScrolled = dragDelta != null;
    // Collapse only for an upward finger gesture. A downward gesture used to
    // revisit older transcript content must leave the full header visible.
    final upwardGesture = userScrolled && dragDelta < 0;
    final atBottom = notification.metrics.extentAfter <= 1;
    if (userScrolled) {
      if (upwardGesture && _autoCollapseHeader && _headerExpanded && mounted) {
        setState(() => _headerExpanded = false);
      }
      _followTranscript = atBottom;
      if (notification.metrics.extentBefore <= 48) {
        _loadOlderTranscript();
      }
    } else if (notification is ScrollEndNotification && atBottom) {
      _followTranscript = true;
    }
    final shouldShow =
        !atBottom &&
        notification.metrics.extentAfter >
            notification.metrics.viewportDimension;
    if (shouldShow != _showScrollToBottom && mounted) {
      setState(() => _showScrollToBottom = shouldShow);
    }
    return false;
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    _pushEvents?.cancel();
    unawaited(_pushRelay?.dispose());
    unawaited(closeSessionNotifications());
    HardwareKeyboard.instance.removeHandler(_handleKey);
    if (kIsWeb) {
      ClipboardEvents.instance?.unregisterPasteEventListener(_handleWebPaste);
    }
    for (final slot in connections) {
      slot.cancelReconnect();
      slot.connection.removeListener(_changed);
      slot.connection.dispose();
    }
    prompt.removeListener(_promptChanged);
    prompt.dispose();
    promptFocus.dispose();
    address.dispose();
    scroll.dispose();
    super.dispose();
  }

  Future<void> _showThinkingLevels() async {
    final selected = await showDialog<String>(
      context: context,
      builder: (dialogContext) => AlertDialog(
        title: const Text('Thinking effort'),
        content: SizedBox(
          width: 320,
          child: ListView(
            shrinkWrap: true,
            children: [
              for (final level in thinkingLevels)
                ListTile(
                  selected: level == agent.currentThinking,
                  leading: Icon(
                    level == agent.currentThinking
                        ? Icons.radio_button_checked
                        : Icons.radio_button_unchecked,
                  ),
                  title: Text(level),
                  onTap: () => Navigator.pop(dialogContext, level),
                ),
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(dialogContext),
            child: const Text('Cancel'),
          ),
        ],
      ),
    );
    if (selected != null && selected != agent.currentThinking) {
      agent.setThinking(selected);
    }
  }

  Future<void> _showAccount() async {
    if (!agent.connected) return;
    agent.refreshProviderConfiguration();
    await showDialog<void>(
      context: context,
      builder: (dialogContext) => ListenableBuilder(
        listenable: agent,
        builder: (context, _) => AlertDialog(
          title: const Text('Providers'),
          content: SizedBox(
            width: 620,
            child: SingleChildScrollView(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Card(
                    child: ListTile(
                      leading: Icon(
                        agent.authenticated
                            ? Icons.account_circle
                            : Icons.login,
                      ),
                      title: const Text('OpenAI Codex'),
                      subtitle: Text(
                        agent.authenticated
                            ? agent.authAccountID.isEmpty
                                  ? 'Signed in'
                                  : 'Signed in · ${agent.authAccountID}'
                            : 'Not signed in',
                      ),
                      trailing: agent.authenticated
                          ? TextButton(
                              onPressed: () => agent.logout(),
                              child: const Text('Sign out'),
                            )
                          : FilledButton(
                              onPressed: () {
                                agent.login();
                                Navigator.pop(dialogContext);
                                _showDeviceLogin();
                              },
                              child: const Text('Sign in'),
                            ),
                    ),
                  ),
                  for (final config in agent.apiProviders)
                    Card(
                      child: ListTile(
                        leading: const Icon(Icons.cloud_outlined),
                        title: Text('(API) ${config.name}'),
                        subtitle: Text(
                          config.configured
                              ? 'API key configured · ${config.models.length} models'
                              : 'API key not configured',
                        ),
                        trailing: FilledButton.tonal(
                          onPressed: () {
                            Navigator.pop(dialogContext);
                            _showAPIProvider(config);
                          },
                          child: const Text('Configure'),
                        ),
                      ),
                    ),
                  const SizedBox(height: 6),
                  SizedBox(
                    width: double.infinity,
                    child: OutlinedButton.icon(
                      onPressed: () {
                        Navigator.pop(dialogContext);
                        _showAPIProvider(null);
                      },
                      icon: const Icon(Icons.add),
                      label: const Text('Add API provider'),
                    ),
                  ),
                ],
              ),
            ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(dialogContext),
              child: const Text('Close'),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> _showAPIProvider(APIProviderConfig? existing) async {
    final name = TextEditingController(text: existing?.name ?? '');
    final apiKey = TextEditingController();
    final openAI = TextEditingController(
      text: existing?.openAIBaseURL ?? 'https://api.example.com/v1',
    );
    final anthropic = TextEditingController(
      text: existing?.anthropicBaseURL ?? 'https://api.example.com',
    );
    final defaultModel = TextEditingController(
      text: existing?.defaultModel ?? '',
    );
    final models = TextEditingController(
      text: existing?.models.join('\n') ?? '',
    );
    var protocol = existing?.protocol ?? 'openai';
    var clearAPIKey = false;
    await showDialog<void>(
      context: context,
      builder: (dialogContext) => StatefulBuilder(
        builder: (context, setDialogState) => AlertDialog(
          title: Text(
            existing == null ? 'Add (API)' : '(API) ${existing.name}',
          ),
          content: SizedBox(
            width: 680,
            child: SingleChildScrollView(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  TextField(
                    controller: name,
                    enabled: existing == null,
                    decoration: const InputDecoration(
                      labelText: 'Provider name',
                      helperText:
                          'Models use Provider-Name/model. Letters, numbers, hyphens, and underscores only.',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 12),
                  Text(
                    existing?.apiKeyConfigured == true
                        ? 'API key configured. Leave empty to keep it.'
                        : 'Enter the API key.',
                  ),
                  const SizedBox(height: 8),
                  TextField(
                    controller: apiKey,
                    obscureText: true,
                    enableSuggestions: false,
                    autocorrect: false,
                    decoration: const InputDecoration(
                      labelText: 'API key (write-only)',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  if (existing != null)
                    CheckboxListTile(
                      contentPadding: EdgeInsets.zero,
                      value: clearAPIKey,
                      onChanged: (value) =>
                          setDialogState(() => clearAPIKey = value == true),
                      title: const Text('Clear stored API key'),
                    ),
                  DropdownButtonFormField<String>(
                    initialValue: protocol,
                    decoration: const InputDecoration(
                      labelText: 'Protocol',
                      border: OutlineInputBorder(),
                    ),
                    items: const [
                      DropdownMenuItem(
                        value: 'openai',
                        child: Text('OpenAI-compatible'),
                      ),
                      DropdownMenuItem(
                        value: 'anthropic',
                        child: Text('Anthropic-compatible (coming next)'),
                      ),
                    ],
                    onChanged: (value) =>
                        setDialogState(() => protocol = value ?? 'openai'),
                  ),
                  const SizedBox(height: 12),
                  TextField(
                    controller: openAI,
                    decoration: const InputDecoration(
                      labelText: 'OpenAI-compatible base URL',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 12),
                  TextField(
                    controller: anthropic,
                    decoration: const InputDecoration(
                      labelText: 'Anthropic-compatible base URL',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 12),
                  TextField(
                    controller: defaultModel,
                    decoration: const InputDecoration(
                      labelText: 'Default model ID',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 12),
                  TextField(
                    controller: models,
                    minLines: 3,
                    maxLines: 8,
                    decoration: const InputDecoration(
                      labelText: 'Supported model IDs (one per line)',
                      border: OutlineInputBorder(),
                    ),
                  ),
                  const SizedBox(height: 8),
                  OutlinedButton.icon(
                    onPressed: existing?.configured == true
                        ? () {
                            agent.fetchAPIProviderModels(existing!.name);
                            Navigator.pop(dialogContext);
                          }
                        : null,
                    icon: const Icon(Icons.refresh),
                    label: const Text('Fetch models from OpenAI endpoint'),
                  ),
                ],
              ),
            ),
          ),
          actions: [
            if (existing != null)
              TextButton(
                onPressed: () {
                  agent.deleteAPIProvider(existing.name);
                  Navigator.pop(dialogContext);
                },
                child: const Text('Delete'),
              ),
            TextButton(
              onPressed: () => Navigator.pop(dialogContext),
              child: const Text('Cancel'),
            ),
            FilledButton(
              onPressed: () {
                agent.saveAPIProvider(
                  name: name.text,
                  apiKey: apiKey.text,
                  protocol: protocol,
                  openAIBaseURL: openAI.text,
                  anthropicBaseURL: anthropic.text,
                  defaultModel: defaultModel.text,
                  models: models.text
                      .split(RegExp(r'[\r\n,]+'))
                      .map((value) => value.trim())
                      .where((value) => value.isNotEmpty)
                      .toList(),
                  clearAPIKey: clearAPIKey,
                );
                Navigator.pop(dialogContext);
              },
              child: const Text('Save'),
            ),
          ],
        ),
      ),
    );
    name.dispose();
    apiKey.dispose();
    openAI.dispose();
    anthropic.dispose();
    defaultModel.dispose();
    models.dispose();
  }

  Future<void> _showDeviceLogin() async {
    await showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (dialogContext) => ListenableBuilder(
        listenable: agent,
        builder: (context, _) => AlertDialog(
          title: const Text('Sign in with OpenAI'),
          content: SizedBox(
            width: 480,
            child: agent.authenticated
                ? const Text('Sign-in complete. You can continue using Forge.')
                : agent.authUserCode.isEmpty
                ? const Row(
                    children: [
                      CircularProgressIndicator(),
                      SizedBox(width: 16),
                      Text('Requesting a device code…'),
                    ],
                  )
                : Column(
                    mainAxisSize: MainAxisSize.min,
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      const Text('Open the OpenAI page and enter this code:'),
                      const SizedBox(height: 16),
                      SelectableText(
                        agent.authUserCode,
                        style: Theme.of(context).textTheme.headlineMedium,
                      ),
                      const SizedBox(height: 10),
                      SelectableText(agent.authVerificationURI),
                      const SizedBox(height: 12),
                      const Text(
                        'Forge stores the refresh token in ~/.pi-go/auth.json '
                        'with owner-only permissions, so future sessions sign in automatically.',
                      ),
                    ],
                  ),
          ),
          actions: [
            if (agent.authUserCode.isNotEmpty && !agent.authenticated)
              FilledButton(
                onPressed: () => launchUrl(
                  Uri.parse(agent.authVerificationURI),
                  mode: LaunchMode.externalApplication,
                ),
                child: const Text('Open OpenAI'),
              ),
            TextButton(
              onPressed: () {
                if (!agent.authenticated && agent.authLoginPending) {
                  agent.cancelLogin();
                }
                Navigator.pop(dialogContext);
              },
              child: Text(agent.authenticated ? 'Done' : 'Cancel'),
            ),
          ],
        ),
      ),
    );
  }

  Future<void> _showModels() async {
    if (!agent.connected || agent.streaming) return;
    agent.refreshProviderConfiguration();
    final selected = await showDialog<String>(
      context: context,
      builder: (dialogContext) => ListenableBuilder(
        listenable: agent,
        builder: (context, _) => AlertDialog(
          title: const Text('Select model'),
          content: SizedBox(
            width: 560,
            child: agent.availableModels.isEmpty
                ? const Text('No configured models are available.')
                : ListView(
                    shrinkWrap: true,
                    children: agent.availableModels
                        .map(
                          (item) => ListTile(
                            selected: item.id == agent.currentModel,
                            leading: Icon(
                              item.id == agent.currentModel
                                  ? Icons.radio_button_checked
                                  : Icons.radio_button_unchecked,
                            ),
                            title: Text(item.label),
                            subtitle: Text(item.id),
                            onTap: () => Navigator.pop(dialogContext, item.id),
                          ),
                        )
                        .toList(),
                  ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(dialogContext),
              child: const Text('Cancel'),
            ),
            TextButton(
              onPressed: () {
                Navigator.pop(dialogContext);
                _showAccount();
              },
              child: const Text('Configure providers'),
            ),
          ],
        ),
      ),
    );
    if (selected != null && selected != agent.currentModel) {
      agent.setModel(selected);
    }
  }

  Future<void> _showClassifierModels() async {
    if (!agent.connected || agent.streaming) return;
    agent.refreshProviderConfiguration();
    final selected = await showDialog<String>(
      context: context,
      builder: (dialogContext) => ListenableBuilder(
        listenable: agent,
        builder: (context, _) => AlertDialog(
          title: const Text('Select safety classifier'),
          content: SizedBox(
            width: 560,
            child: agent.availableModels.isEmpty
                ? const Text('No configured models are available.')
                : ListView(
                    shrinkWrap: true,
                    children: agent.availableModels
                        .map(
                          (item) => ListTile(
                            selected: item.id == agent.classifierModel,
                            leading: Icon(
                              item.id == agent.classifierModel
                                  ? Icons.radio_button_checked
                                  : Icons.radio_button_unchecked,
                            ),
                            title: Text(item.label),
                            subtitle: Text(item.id),
                            onTap: () => Navigator.pop(dialogContext, item.id),
                          ),
                        )
                        .toList(),
                  ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(dialogContext),
              child: const Text('Cancel'),
            ),
          ],
        ),
      ),
    );
    if (selected != null && selected != agent.classifierModel) {
      agent.setClassifierModel(selected);
    }
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
      var page = await agent.listSessions();
      final sessions = <SessionItem>[...page.sessions];
      var nextOffset = page.nextOffset;
      var hasMore = page.hasMore;
      var loadingMore = false;
      if (!mounted) return;
      final selected = await showDialog<String>(
        context: context,
        builder: (dialogContext) => StatefulBuilder(
          builder: (context, setDialogState) => AlertDialog(
            title: const Text('Switch session'),
            content: SizedBox(
              width: 720,
              height: 480,
              child: sessions.isEmpty
                  ? const Center(child: Text('No existing sessions'))
                  : ListView.separated(
                      itemCount: sessions.length + (hasMore ? 1 : 0),
                      separatorBuilder: (_, _) => const Divider(height: 1),
                      itemBuilder: (context, index) {
                        if (index == sessions.length) {
                          return Padding(
                            padding: const EdgeInsets.symmetric(vertical: 12),
                            child: Center(
                              child: OutlinedButton.icon(
                                onPressed: loadingMore
                                    ? null
                                    : () async {
                                        setDialogState(
                                          () => loadingMore = true,
                                        );
                                        try {
                                          page = await agent.listSessions(
                                            offset: nextOffset,
                                          );
                                          if (!dialogContext.mounted) return;
                                          setDialogState(() {
                                            sessions.addAll(page.sessions);
                                            nextOffset = page.nextOffset;
                                            hasMore = page.hasMore;
                                            loadingMore = false;
                                          });
                                        } catch (error) {
                                          if (!dialogContext.mounted) return;
                                          setDialogState(
                                            () => loadingMore = false,
                                          );
                                          ScaffoldMessenger.of(
                                            context,
                                          ).showSnackBar(
                                            SnackBar(
                                              content: Text(
                                                'Could not load more sessions: $error',
                                              ),
                                            ),
                                          );
                                        }
                                      },
                                icon: loadingMore
                                    ? const SizedBox.square(
                                        dimension: 16,
                                        child: CircularProgressIndicator(
                                          strokeWidth: 2,
                                        ),
                                      )
                                    : const Icon(Icons.expand_more),
                                label: Text(
                                  loadingMore
                                      ? 'Loading…'
                                      : 'Load more sessions',
                                ),
                              ),
                            ),
                          );
                        }
                        final session = sessions[index];
                        final localTime = session.lastMessageTime?.toLocal();
                        final timestamp = localTime == null
                            ? 'Time unavailable'
                            : '${localTime.year.toString().padLeft(4, '0')}-'
                                  '${localTime.month.toString().padLeft(2, '0')}-'
                                  '${localTime.day.toString().padLeft(2, '0')}  '
                                  '${localTime.hour.toString().padLeft(2, '0')}:'
                                  '${localTime.minute.toString().padLeft(2, '0')}';
                        return ListTile(
                          title: Row(
                            children: [
                              if (session.active || session.waitingInput) ...[
                                Container(
                                  key: ValueKey(
                                    session.waitingInput
                                        ? 'waiting-session-${session.id}'
                                        : 'active-session-${session.id}',
                                  ),
                                  width: 9,
                                  height: 9,
                                  decoration: BoxDecoration(
                                    color: session.waitingInput
                                        ? Colors.amber
                                        : Colors.green,
                                    shape: BoxShape.circle,
                                  ),
                                ),
                                const SizedBox(width: 8),
                              ],
                              Expanded(child: Text(timestamp)),
                            ],
                          ),
                          subtitle: Text(
                            session.summary.isEmpty
                                ? 'No completed turn summary yet.'
                                : session.summary,
                            maxLines: 2,
                            overflow: TextOverflow.ellipsis,
                          ),
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

  bool get _isNativeMobile =>
      !kIsWeb &&
      (defaultTargetPlatform == TargetPlatform.android ||
          defaultTargetPlatform == TargetPlatform.iOS);

  void _dismissKeyboard() {
    FocusManager.instance.primaryFocus?.unfocus();
  }

  List<CommandSuggestion> _commandSuggestions() {
    final input = prompt.text;
    if (!input.startsWith('/')) return const [];
    if (input.startsWith('/model ')) {
      final prefix = input.substring('/model '.length);
      return agent.availableModels
          .where((value) => value.id.startsWith(prefix))
          .map(
            (value) => CommandSuggestion(
              '/model ${value.id}',
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
        if (parts.length != 2 ||
            !agent.availableModels.any((model) => model.id == parts[1])) {
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
                  : Stack(
                      children: [
                        NotificationListener<ScrollNotification>(
                          onNotification: _handleTranscriptScroll,
                          child: ListView.builder(
                            controller: scroll,
                            padding: const EdgeInsets.all(20),
                            itemCount: agent.messages.length,
                            itemBuilder: (_, index) =>
                                _message(agent.messages[index]),
                          ),
                        ),
                        if (_showScrollToBottom)
                          Positioned(
                            right: 16,
                            bottom: 16,
                            child: FloatingActionButton.small(
                              heroTag: 'scroll-to-transcript-bottom',
                              tooltip: 'Scroll to bottom',
                              onPressed: _scrollToBottom,
                              child: const Icon(
                                Icons.keyboard_double_arrow_down,
                              ),
                            ),
                          ),
                      ],
                    ),
            ),
            const Divider(height: 1),
            _composer(),
          ],
        ),
      ),
    );
  }

  Future<String> _deviceAuthorizationName() async {
    final info = DeviceInfoPlugin();
    switch (defaultTargetPlatform) {
      case TargetPlatform.android:
        final device = await info.androidInfo;
        final model = device.model.trim();
        return model.isEmpty ? 'Android device' : model;
      case TargetPlatform.iOS:
        final device = await info.iosInfo;
        final name = device.name.trim();
        final model = device.modelName.trim();
        if (name.isNotEmpty &&
            name.toLowerCase() != 'iphone' &&
            name.toLowerCase() != 'ipad') {
          return name;
        }
        return model.isEmpty ? device.model : model;
      case TargetPlatform.macOS:
        final device = await info.macOsInfo;
        final name = device.computerName.trim();
        return name.isEmpty ? device.modelName : name;
      default:
        return 'Forge device';
    }
  }

  Future<void> _copyDeviceAuthorizationEntry() async {
    try {
      final identity = _deviceIdentity ?? await getDeviceIdentity();
      if (identity == null) throw StateError('Device identity is unavailable');
      _deviceIdentity = identity;
      final entry = const JsonEncoder.withIndent('  ').convert({
        'name': await _deviceAuthorizationName(),
        'deviceId': identity.deviceId,
        'publicKey': identity.publicKey,
        'fingerprint': identity.fingerprint,
      });
      await Clipboard.setData(ClipboardData(text: entry));
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(
            content: Text('Device whitelist entry copied to clipboard'),
          ),
        );
      }
    } catch (error) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('Could not export device identity: $error')),
        );
      }
    }
  }

  Future<bool> _confirmPushRevocation(String description) async {
    if (!mounted) return false;
    return await showDialog<bool>(
          context: context,
          builder: (context) => AlertDialog(
            title: const Text('Revoke push authorization?'),
            content: Text(
              '$description will stop receiving encrypted notifications until it is paired again.',
            ),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(context, false),
                child: const Text('Cancel'),
              ),
              FilledButton(
                onPressed: () => Navigator.pop(context, true),
                style: FilledButton.styleFrom(
                  backgroundColor: Theme.of(context).colorScheme.error,
                  foregroundColor: Theme.of(context).colorScheme.onError,
                ),
                child: const Text('Revoke'),
              ),
            ],
          ),
        ) ==
        true;
  }

  Future<void> _showPushAuthorizations() async {
    if (!pushSupported) return;
    List<Map<String, dynamic>> deviceAgents = const [];
    List<Map<String, dynamic>> agentDevices = const [];
    Object? deviceError;
    Object? agentError;
    try {
      deviceAgents = await _pushRelay!.listAuthorizations();
    } catch (error) {
      deviceError = error;
    }
    try {
      agentDevices = await agent.listPushAuthorizations();
    } catch (error) {
      agentError = error;
    }
    if (!mounted) return;

    String displayName(Map<String, dynamic> item, String kind) {
      final name = '${item['displayName'] ?? ''}'.trim();
      return name.isEmpty ? kind : name;
    }

    String identityLine(Map<String, dynamic> item, String kind) {
      final name = displayName(item, kind);
      final fingerprint = '${item['fingerprint'] ?? ''}'.trim();
      final platform = '${item['platform'] ?? ''}'.trim();
      final prefix = [name, if (platform.isNotEmpty) platform].join(' · ');
      return fingerprint.isEmpty ? prefix : '$prefix\n$fingerprint';
    }

    await showDialog<void>(
      context: context,
      builder: (context) => StatefulBuilder(
        builder: (context, setDialogState) => AlertDialog(
          title: const Text('Push authorization links'),
          content: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 680, maxHeight: 620),
            child: SingleChildScrollView(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    'Agents authorized by this device',
                    style: Theme.of(context).textTheme.titleSmall,
                  ),
                  const SizedBox(height: 6),
                  if (deviceError != null)
                    Text('Could not query device links: $deviceError')
                  else if (deviceAgents.isEmpty)
                    const Text('No active agent links.')
                  else
                    for (final item in deviceAgents)
                      ListTile(
                        dense: true,
                        contentPadding: EdgeInsets.zero,
                        leading: const Icon(Icons.dns_outlined),
                        title: Text(identityLine(item, 'Forge agent')),
                        subtitle: Text(
                          (item['scopes'] as List? ?? const []).join(', '),
                        ),
                        trailing: IconButton(
                          tooltip: 'Revoke agent push authorization',
                          icon: const Icon(Icons.link_off_outlined),
                          onPressed: () async {
                            final agentId = '${item['agentId'] ?? ''}';
                            final name = displayName(item, 'This agent');
                            if (agentId.isEmpty ||
                                !await _confirmPushRevocation(name)) {
                              return;
                            }
                            try {
                              await _pushRelay!.revokeAuthorization(agentId);
                              final matching = connections.where(
                                (slot) =>
                                    _pushAgentIDs[slot.connection] == agentId,
                              );
                              for (final slot in matching) {
                                if (slot.connection.connected) {
                                  await slot.connection.revokePushAuthorization(
                                    _pushIdentity!.deviceId,
                                  );
                                }
                                _pushAuthorizedConnections.remove(
                                  slot.connection,
                                );
                              }
                              if (context.mounted) {
                                setDialogState(
                                  () => deviceAgents = deviceAgents
                                      .where(
                                        (value) =>
                                            '${value['agentId'] ?? ''}' !=
                                            agentId,
                                      )
                                      .toList(),
                                );
                              }
                            } catch (error) {
                              if (context.mounted) {
                                ScaffoldMessenger.of(context).showSnackBar(
                                  SnackBar(
                                    content: Text(
                                      'Could not revoke push authorization: $error',
                                    ),
                                  ),
                                );
                              }
                            }
                          },
                        ),
                      ),
                  const Divider(height: 28),
                  Text(
                    'Devices authorized for this agent',
                    style: Theme.of(context).textTheme.titleSmall,
                  ),
                  const SizedBox(height: 6),
                  if (agentError != null)
                    Text('Could not query agent links: $agentError')
                  else if (agentDevices.isEmpty)
                    const Text('No active device links.')
                  else
                    for (final item in agentDevices)
                      ListTile(
                        dense: true,
                        contentPadding: EdgeInsets.zero,
                        leading: Icon(
                          item['pushEndpointActive'] == true
                              ? Icons.notifications_active_outlined
                              : Icons.notifications_off_outlined,
                        ),
                        title: Text(identityLine(item, 'Forge device')),
                        subtitle: Text(
                          item['pushEndpointActive'] == true
                              ? 'Push endpoint active'
                              : 'Push endpoint inactive',
                        ),
                        trailing: IconButton(
                          tooltip: 'Revoke device push authorization',
                          icon: const Icon(Icons.link_off_outlined),
                          onPressed: () async {
                            final deviceId = '${item['deviceId'] ?? ''}';
                            final name = displayName(item, 'This device');
                            if (deviceId.isEmpty ||
                                !await _confirmPushRevocation(name)) {
                              return;
                            }
                            try {
                              await agent.revokePushAuthorization(deviceId);
                              if (_pushIdentity?.deviceId == deviceId) {
                                _pushAuthorizedConnections.remove(agent);
                              }
                              if (context.mounted) {
                                setDialogState(
                                  () => agentDevices = agentDevices
                                      .where(
                                        (value) =>
                                            '${value['deviceId'] ?? ''}' !=
                                            deviceId,
                                      )
                                      .toList(),
                                );
                              }
                            } catch (error) {
                              if (context.mounted) {
                                ScaffoldMessenger.of(context).showSnackBar(
                                  SnackBar(
                                    content: Text(
                                      'Could not revoke push authorization: $error',
                                    ),
                                  ),
                                );
                              }
                            }
                          },
                        ),
                      ),
                ],
              ),
            ),
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(context),
              child: const Text('Done'),
            ),
          ],
        ),
      ),
    );
  }

  Widget _controlsPopup() => SizedBox.square(
    dimension: 40,
    child: PopupMenuButton<void>(
      padding: EdgeInsets.zero,
      constraints: const BoxConstraints(minWidth: 280, maxWidth: 360),
      tooltip: 'Model, thinking, and providers',
      icon: const Icon(Icons.tune),
      itemBuilder: (_) => [
        if (deviceIdentityExportSupported)
          PopupMenuItem(
            onTap: _copyDeviceAuthorizationEntry,
            child: const Row(
              children: [
                Icon(Icons.key_outlined, size: 18),
                SizedBox(width: 8),
                Expanded(
                  child: Text(
                    'Copy device whitelist entry',
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
              ],
            ),
          ),
        if (pushSupported)
          PopupMenuItem(
            enabled:
                agent.connected &&
                _pushRelay?.identity != null &&
                !agent.streaming,
            onTap: _showPushAuthorizations,
            child: const Row(
              children: [
                Icon(Icons.notifications_active_outlined, size: 18),
                SizedBox(width: 8),
                Expanded(
                  child: Text(
                    'Push authorization links',
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
              ],
            ),
          ),
        PopupMenuItem(
          enabled: agent.connected && !agent.streaming,
          onTap: _showModels,
          child: Row(
            children: [
              const Icon(Icons.smart_toy_outlined, size: 18),
              const SizedBox(width: 8),
              Flexible(child: Text(model)),
            ],
          ),
        ),
        PopupMenuItem(
          enabled: agent.connected && !agent.streaming,
          onTap: _showClassifierModels,
          child: Row(
            children: [
              const Icon(Icons.shield_outlined, size: 18),
              const SizedBox(width: 8),
              Flexible(
                child: Text(
                  'classifier: ${agent.classifierModel}',
                  overflow: TextOverflow.ellipsis,
                ),
              ),
            ],
          ),
        ),
        PopupMenuItem(
          enabled: agent.connected && !agent.streaming,
          onTap: _showThinkingLevels,
          child: Row(
            children: [
              const Icon(Icons.psychology_outlined, size: 18),
              const SizedBox(width: 8),
              Text('thinking: $thinking'),
            ],
          ),
        ),
        PopupMenuItem(
          enabled: agent.connected,
          onTap: _showAccount,
          child: Row(
            children: [
              Icon(
                agent.authenticated
                    ? Icons.account_circle
                    : Icons.manage_accounts_outlined,
                size: 18,
              ),
              const SizedBox(width: 8),
              const Text('Providers'),
            ],
          ),
        ),
      ],
    ),
  );

  Widget _collapsedConnectionBar() => Material(
    color: Colors.transparent,
    child: InkWell(
      key: const ValueKey('collapsed-header-status'),
      onTap: () => setState(() => _headerExpanded = true),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 9),
        child: Align(
          alignment: Alignment.centerLeft,
          child: Text(
            agent.status,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: TextStyle(
              color: agent.connected
                  ? Theme.of(context).colorScheme.primary
                  : Theme.of(context).colorScheme.onSurfaceVariant,
              fontWeight: FontWeight.w600,
            ),
          ),
        ),
      ),
    ),
  );

  Widget _connectionBar() => AnimatedSize(
    duration: const Duration(milliseconds: 180),
    curve: Curves.easeOut,
    alignment: Alignment.topCenter,
    child: !_autoCollapseHeader || _headerExpanded
        ? _expandedConnectionBar()
        : _collapsedConnectionBar(),
  );

  Widget _expandedConnectionBar() => Padding(
    padding: const EdgeInsets.fromLTRB(16, 10, 16, 10),
    child: Column(
      children: [
        LayoutBuilder(
          builder: (context, constraints) {
            final compact = constraints.maxWidth < 720;
            final headerChildren = <Widget>[
              const Text(
                'Pi Go',
                style: TextStyle(fontSize: 20, fontWeight: FontWeight.w700),
              ),
              SizedBox(width: compact ? 2 : 10),
              _controlsPopup(),
              const Spacer(),
              if (compact)
                IconButton(
                  padding: EdgeInsets.zero,
                  constraints: const BoxConstraints.tightFor(
                    width: 40,
                    height: 40,
                  ),
                  tooltip: agent.yolo
                      ? 'YOLO enabled: bypassing classifier safety gate'
                      : 'Enable YOLO mode and bypass classifier safety gate',
                  onPressed: agent.connected && !agent.streaming
                      ? () => agent.setYOLO(!agent.yolo)
                      : null,
                  style: agent.yolo
                      ? IconButton.styleFrom(
                          backgroundColor: Theme.of(
                            context,
                          ).colorScheme.errorContainer,
                          foregroundColor: Theme.of(
                            context,
                          ).colorScheme.onErrorContainer,
                        )
                      : IconButton.styleFrom(
                          side: BorderSide(
                            color: Theme.of(context).colorScheme.outline,
                          ),
                        ),
                  icon: const Icon(Icons.rocket_launch_outlined),
                )
              else
                agent.yolo
                    ? FilledButton.icon(
                        onPressed: agent.connected && !agent.streaming
                            ? () => agent.setYOLO(false)
                            : null,
                        style: FilledButton.styleFrom(
                          backgroundColor: Theme.of(
                            context,
                          ).colorScheme.errorContainer,
                          foregroundColor: Theme.of(
                            context,
                          ).colorScheme.onErrorContainer,
                        ),
                        icon: const Icon(Icons.rocket_launch_outlined),
                        label: const Text('YOLO'),
                      )
                    : OutlinedButton.icon(
                        onPressed: agent.connected && !agent.streaming
                            ? () => agent.setYOLO(true)
                            : null,
                        icon: const Icon(Icons.rocket_launch_outlined),
                        label: const Text('YOLO'),
                      ),
              SizedBox(width: compact ? 2 : 6),
              if (compact)
                IconButton.outlined(
                  padding: EdgeInsets.zero,
                  constraints: const BoxConstraints.tightFor(
                    width: 40,
                    height: 40,
                  ),
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
              SizedBox(width: compact ? 2 : 6),
              if (!compact) ...[
                IconButton.outlined(
                  tooltip: agent.authenticated
                      ? 'Providers · OpenAI signed in'
                      : 'Configure providers',
                  onPressed: agent.connected ? _showAccount : null,
                  icon: Icon(
                    agent.authenticated ? Icons.account_circle : Icons.login,
                  ),
                ),
                const SizedBox(width: 6),
              ],
              IconButton.outlined(
                padding: compact ? EdgeInsets.zero : null,
                constraints: compact
                    ? const BoxConstraints.tightFor(width: 40, height: 40)
                    : null,
                tooltip: agent.currentCWD.isEmpty
                    ? 'Set agent working directory'
                    : 'Workspace: ${agent.currentCWD}',
                onPressed: agent.connected && !agent.streaming
                    ? _showWorkspace
                    : null,
                icon: const Icon(Icons.folder_outlined),
              ),
              SizedBox(width: compact ? 2 : 6),
              _connectionManagerButton(compact),
            ];
            return Column(
              children: [
                Row(children: headerChildren),
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
                Align(
                  alignment: Alignment.centerLeft,
                  child: Wrap(
                    spacing: 12,
                    runSpacing: 2,
                    children:
                        [
                              Text('session ${agent.total}'),
                              Text('in ${agent.input}'),
                              Text('cache ${agent.cacheRead}'),
                              Text('out ${agent.output}'),
                              Text('context ${agent.contextUsed}/272000'),
                              Text('upstream ${agent.upstreamTransport}'),
                            ]
                            .map(
                              (item) => DefaultTextStyle.merge(
                                style: Theme.of(context).textTheme.bodySmall
                                    ?.copyWith(
                                      color: Theme.of(
                                        context,
                                      ).colorScheme.onSurfaceVariant,
                                    ),
                                child: item,
                              ),
                            )
                            .toList(),
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
                if (_showLocalAgentOption)
                  const ButtonSegment(
                    value: ConnectionKind.local,
                    label: Text('Local'),
                  ),
                if (!flatpakFrontend)
                  ButtonSegment(
                    value: ConnectionKind.unix,
                    label: const Text('Unix'),
                    enabled: nativeSocketsSupported && !webSocketOnlyClient,
                  ),
                if (!flatpakFrontend)
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
                        connections[activeConnectionIndex].kind =
                            connectionKind;
                        connections[activeConnectionIndex].address =
                            address.text;
                      });
                    },
            ),
          ),
          const SizedBox(height: 8),
          Row(
            children: [
              Expanded(
                child: Builder(
                  builder: (context) {
                    final trusted = _trustedEndpoints;
                    return TextField(
                      controller: address,
                      enabled:
                          !agent.connected &&
                          connectionKind != ConnectionKind.local,
                      decoration: InputDecoration(
                        isDense: true,
                        labelText: connectionKind == ConnectionKind.local
                            ? 'Local workspace'
                            : 'Agent server address',
                        helperText: trusted.isNotEmpty && !agent.connected
                            ? 'Select a trusted server or enter another URL'
                            : null,
                        border: const OutlineInputBorder(),
                        suffixIcon:
                            !agent.connected &&
                                connectionKind == ConnectionKind.websocket &&
                                trusted.isNotEmpty
                            ? PopupMenuButton<String>(
                                tooltip: 'Select trusted server',
                                icon: const Icon(Icons.arrow_drop_down),
                                onSelected: _selectTrustedEndpoint,
                                itemBuilder: (context) => [
                                  for (final endpoint in trusted)
                                    PopupMenuItem<String>(
                                      value: endpoint,
                                      child: Row(
                                        children: [
                                          const Icon(
                                            Icons.verified_user_outlined,
                                            size: 18,
                                          ),
                                          const SizedBox(width: 8),
                                          Flexible(
                                            child: Text(
                                              endpoint,
                                              overflow: TextOverflow.ellipsis,
                                            ),
                                          ),
                                        ],
                                      ),
                                    ),
                                ],
                              )
                            : null,
                      ),
                    );
                  },
                ),
              ),
              const SizedBox(width: 10),
              FilledButton(
                onPressed: agent.connected
                    ? _disconnectActive
                    : _connectSelected,
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
              item.safetyMessage ??
                  'Safety classifier is checking this operation…',
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

  Widget _sourcePreviewHeader(String path, String language, IconData icon) {
    final filename = path.replaceAll('\\', '/').split('/').last;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Row(
          children: [
            Icon(icon, size: 17),
            const SizedBox(width: 7),
            Expanded(
              child: SelectableText(
                filename.isEmpty ? path : filename,
                style: const TextStyle(fontWeight: FontWeight.w700),
              ),
            ),
            if (language != 'text')
              Text(
                language,
                style: Theme.of(context).textTheme.labelSmall?.copyWith(
                  color: Theme.of(context).colorScheme.onSurfaceVariant,
                ),
              ),
          ],
        ),
        if (path.isNotEmpty && path != filename) ...[
          const SizedBox(height: 3),
          SelectableText(
            path,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: Theme.of(context).colorScheme.onSurfaceVariant,
              fontFamily: 'monospace',
            ),
          ),
        ],
      ],
    );
  }

  Widget _writeToolPreview(TranscriptItem item) {
    final arguments = item.toolArguments;
    final path = arguments is Map ? '${arguments['path'] ?? ''}' : '';
    final content = arguments is Map ? '${arguments['content'] ?? ''}' : '';
    final normalized = content.replaceAll('\r\n', '\n').replaceAll('\r', '\n');
    final lineCount = '\n'.allMatches(normalized).length + 1;
    final gutterWidth = lineCount.toString().length;
    final numbers = [
      for (var line = 1; line <= lineCount; line++)
        line.toString().padLeft(gutterWidth),
    ].join('\n');
    final language = _sourceLanguageForPath(path);
    final dark = Theme.of(context).brightness == Brightness.dark;
    final syntaxTheme = dark ? atomOneDarkReasonableTheme : atomOneLightTheme;
    final codeStyle = TextStyle(
      fontFamily: 'monospace',
      fontSize: 13,
      height: 1.45,
      color: syntaxTheme['root']?.color,
    );
    final highlighted = _syntaxSpans(normalized, language, syntaxTheme);

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        KeyedSubtree(
          key: const ValueKey('write-preview-filename'),
          child: _sourcePreviewHeader(
            path,
            language,
            Icons.description_outlined,
          ),
        ),
        const SizedBox(height: 9),
        Container(
          width: double.infinity,
          constraints: const BoxConstraints(maxHeight: 480),
          decoration: BoxDecoration(
            color:
                syntaxTheme['root']?.backgroundColor ??
                Theme.of(context).colorScheme.surfaceContainerLow,
            borderRadius: BorderRadius.circular(7),
            border: Border.all(
              color: Theme.of(context).colorScheme.outlineVariant,
            ),
          ),
          child: SingleChildScrollView(
            child: SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              padding: const EdgeInsets.symmetric(vertical: 10),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Container(
                    padding: const EdgeInsets.symmetric(horizontal: 10),
                    decoration: BoxDecoration(
                      border: Border(
                        right: BorderSide(
                          color: Theme.of(context).colorScheme.outlineVariant,
                        ),
                      ),
                    ),
                    child: Text(
                      numbers,
                      key: const ValueKey('write-preview-line-numbers'),
                      style: codeStyle.copyWith(
                        color: Theme.of(context).colorScheme.onSurfaceVariant,
                      ),
                      textAlign: TextAlign.right,
                    ),
                  ),
                  Padding(
                    padding: const EdgeInsets.symmetric(horizontal: 12),
                    child: SelectableText.rich(
                      TextSpan(style: codeStyle, children: highlighted),
                      key: const ValueKey('write-preview-code'),
                    ),
                  ),
                ],
              ),
            ),
          ),
        ),
        const SizedBox(height: 9),
        Text('Result', style: Theme.of(context).textTheme.labelMedium),
        const SizedBox(height: 3),
        SelectableText(
          item.toolOutput.isEmpty ? 'Writing file…' : item.toolOutput,
          key: const ValueKey('write-preview-result'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
      ],
    );
  }

  Widget _replaceToolPreview(TranscriptItem item) {
    final arguments = item.toolArguments;
    final details = item.toolDetails;
    final path = arguments is Map
        ? '${arguments['path'] ?? ''}'
        : details is Map
        ? '${details['path'] ?? ''}'
        : '';
    final language = _sourceLanguageForPath(path);
    final oldText = details is Map
        ? '${details['oldText'] ?? ''}'
        : arguments is Map
        ? '${arguments['oldText'] ?? ''}'
        : '';
    final newText = details is Map
        ? '${details['newText'] ?? ''}'
        : arguments is Map
        ? '${arguments['newText'] ?? ''}'
        : '';
    final startLine = details is Map && details['startLine'] is num
        ? (details['startLine'] as num).toInt()
        : 1;
    final oldLines = oldText.replaceAll('\r\n', '\n').split('\n');
    final newLines = newText.replaceAll('\r\n', '\n').split('\n');
    final dark = Theme.of(context).brightness == Brightness.dark;
    final syntaxTheme = dark ? atomOneDarkReasonableTheme : atomOneLightTheme;
    final codeStyle = TextStyle(
      fontFamily: 'monospace',
      fontSize: 13,
      height: 1.45,
      color: syntaxTheme['root']?.color,
    );

    Widget diffBlock({
      required String sign,
      required List<String> lines,
      required Color background,
      required ValueKey<String> key,
    }) {
      final source = lines.join('\n');
      return Container(
        key: key,
        color: background,
        padding: const EdgeInsets.symmetric(vertical: 8),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SizedBox(
              width: 28,
              child: Text(
                List.filled(lines.length, sign).join('\n'),
                style: codeStyle.copyWith(fontWeight: FontWeight.w700),
                textAlign: TextAlign.center,
              ),
            ),
            Container(
              width: 44,
              padding: const EdgeInsets.only(right: 9),
              decoration: BoxDecoration(
                border: Border(
                  right: BorderSide(
                    color: Theme.of(context).colorScheme.outlineVariant,
                  ),
                ),
              ),
              child: Text(
                [
                  for (var index = 0; index < lines.length; index++)
                    '${startLine + index}',
                ].join('\n'),
                style: codeStyle.copyWith(
                  color: Theme.of(context).colorScheme.onSurfaceVariant,
                ),
                textAlign: TextAlign.right,
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 12),
              child: SelectableText.rich(
                TextSpan(
                  style: codeStyle,
                  children: _syntaxSpans(source, language, syntaxTheme),
                ),
              ),
            ),
          ],
        ),
      );
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        KeyedSubtree(
          key: const ValueKey('replace-preview-filename'),
          child: _sourcePreviewHeader(
            path,
            language,
            Icons.difference_outlined,
          ),
        ),
        const SizedBox(height: 9),
        Container(
          width: double.infinity,
          constraints: const BoxConstraints(maxHeight: 480),
          clipBehavior: Clip.antiAlias,
          decoration: BoxDecoration(
            color:
                syntaxTheme['root']?.backgroundColor ??
                Theme.of(context).colorScheme.surfaceContainerLow,
            borderRadius: BorderRadius.circular(7),
            border: Border.all(
              color: Theme.of(context).colorScheme.outlineVariant,
            ),
          ),
          child: SingleChildScrollView(
            child: SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              child: IntrinsicWidth(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    diffBlock(
                      sign: '-',
                      lines: oldLines,
                      background: dark
                          ? const Color(0x443F1D24)
                          : const Color(0xFFFFEBE9),
                      key: const ValueKey('replace-preview-removed'),
                    ),
                    diffBlock(
                      sign: '+',
                      lines: newLines,
                      background: dark
                          ? const Color(0x4433472B)
                          : const Color(0xFFDAFBE1),
                      key: const ValueKey('replace-preview-added'),
                    ),
                  ],
                ),
              ),
            ),
          ),
        ),
        const SizedBox(height: 9),
        Text('Result', style: Theme.of(context).textTheme.labelMedium),
        const SizedBox(height: 3),
        SelectableText(
          item.toolOutput.isEmpty ? 'Matching and replacing…' : item.toolOutput,
          key: const ValueKey('replace-preview-result'),
          style: Theme.of(context).textTheme.bodySmall,
        ),
      ],
    );
  }

  Widget _cronToolPreview(TranscriptItem item) {
    final arguments = item.toolArguments is Map
        ? item.toolArguments as Map
        : const <String, dynamic>{};
    final details = item.toolDetails is Map
        ? item.toolDetails as Map
        : const <String, dynamic>{};
    final action = '${details['action'] ?? arguments['action'] ?? 'list'}';
    final jobValue = details['job'];
    final job = jobValue is Map ? jobValue : arguments;
    final jobsValue = details['jobs'];
    final jobs = jobsValue is List
        ? jobsValue.whereType<Map>().toList()
        : const <Map>[];

    Widget field(String label, Object? value, {bool selectable = true}) {
      final text = '${value ?? ''}';
      if (text.isEmpty) return const SizedBox.shrink();
      return Padding(
        padding: const EdgeInsets.only(bottom: 8),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(label, style: Theme.of(context).textTheme.labelMedium),
            const SizedBox(height: 2),
            if (selectable) SelectableText(text) else Text(text),
          ],
        ),
      );
    }

    Widget jobCard(Map value) => Card(
      margin: const EdgeInsets.only(bottom: 8),
      child: Padding(
        padding: const EdgeInsets.all(10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              'Cron job #${value['id'] ?? 'pending'}',
              style: const TextStyle(fontWeight: FontWeight.w700),
            ),
            const SizedBox(height: 6),
            field('Schedule', value['schedule']),
            field('Timezone', value['timezone']),
            field('Prompt', value['prompt']),
            if ('${value['nextRunAt'] ?? ''}'.isNotEmpty)
              field('Next run', value['nextRunAt']),
            if ('${value['lastStatus'] ?? ''}'.isNotEmpty)
              field('Last status', value['lastStatus']),
          ],
        ),
      ),
    );

    return Container(
      key: const ValueKey('cron-preview'),
      width: double.infinity,
      padding: const EdgeInsets.all(10),
      decoration: BoxDecoration(
        color: Theme.of(context).colorScheme.surfaceContainerLow,
        borderRadius: BorderRadius.circular(8),
        border: Border.all(color: Theme.of(context).colorScheme.outlineVariant),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              const Icon(Icons.schedule_outlined, size: 18),
              const SizedBox(width: 7),
              Text(
                'Cron · $action',
                style: const TextStyle(fontWeight: FontWeight.w700),
              ),
            ],
          ),
          const SizedBox(height: 9),
          if (action == 'list')
            if (jobs.isEmpty)
              const Text('No cron jobs for this session.')
            else
              for (final value in jobs) jobCard(value)
          else if (action == 'delete' || action == 'deregister')
            field('Job ID', details['id'] ?? arguments['id'])
          else
            jobCard(job),
          if (item.toolOutput.isNotEmpty) ...[
            const SizedBox(height: 3),
            field('Result', item.toolOutput),
          ],
        ],
      ),
    );
  }

  void _observeMessageFonts(TranscriptItem item) {
    final fonts = ForgeFontScope.maybeOf(context);
    if (fonts == null) return;
    final locale = Localizations.localeOf(context);

    if (!item.fontRoleScanned) {
      item.fontRoleScanned = true;
      fonts.observeText(item.role, locale: locale);
    }

    void observeNewText(
      String value,
      int alreadyScanned,
      void Function(int) save,
    ) {
      if (alreadyScanned > value.length) alreadyScanned = 0;
      if (alreadyScanned < value.length) {
        fonts.observeText(
          alreadyScanned == 0 ? value : value.substring(alreadyScanned),
          locale: locale,
        );
      }
      save(value.length);
    }

    observeNewText(
      item.text,
      item.fontTextScanned,
      (length) => item.fontTextScanned = length,
    );
    observeNewText(
      item.thinking,
      item.fontThinkingScanned,
      (length) => item.fontThinkingScanned = length,
    );
  }

  Widget _message(TranscriptItem item) {
    _observeMessageFonts(item);
    final collapsible =
        item.role.startsWith('Tool ·') || item.role == 'Compaction summary';
    final card = Container(
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
          Row(
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
              if (collapsible)
                Icon(
                  item.collapsed ? Icons.expand_more : Icons.expand_less,
                  size: 19,
                  color: Theme.of(context).colorScheme.onSurfaceVariant,
                ),
            ],
          ),
          if (item.safetyStatus != null) _safetyBanner(item),
          if (item.thinking.isNotEmpty && !item.collapsed) ...[
            const SizedBox(height: 8),
            Container(
              width: double.infinity,
              decoration: BoxDecoration(
                color: Theme.of(
                  context,
                ).colorScheme.surfaceContainerLow.withValues(alpha: .72),
                borderRadius: BorderRadius.circular(8),
                border: Border.all(
                  color: Theme.of(context).colorScheme.outlineVariant,
                ),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  InkWell(
                    borderRadius: BorderRadius.circular(8),
                    onTap: () => setState(
                      () => item.thinkingCollapsed = !item.thinkingCollapsed,
                    ),
                    child: Padding(
                      padding: const EdgeInsets.symmetric(
                        horizontal: 10,
                        vertical: 8,
                      ),
                      child: Row(
                        children: [
                          Icon(
                            Icons.psychology_outlined,
                            size: 17,
                            color: Theme.of(
                              context,
                            ).colorScheme.onSurfaceVariant,
                          ),
                          const SizedBox(width: 7),
                          Expanded(
                            child: Text(
                              item.thinkingCollapsed
                                  ? 'Thinking · tap to expand'
                                  : 'Thinking',
                              style: TextStyle(
                                color: Theme.of(
                                  context,
                                ).colorScheme.onSurfaceVariant,
                                fontSize: 12,
                                fontWeight: FontWeight.w700,
                              ),
                            ),
                          ),
                          Icon(
                            item.thinkingCollapsed
                                ? Icons.expand_more
                                : Icons.expand_less,
                            size: 19,
                            color: Theme.of(
                              context,
                            ).colorScheme.onSurfaceVariant,
                          ),
                        ],
                      ),
                    ),
                  ),
                  if (!item.thinkingCollapsed) ...[
                    Divider(
                      height: 1,
                      color: Theme.of(context).colorScheme.outlineVariant,
                    ),
                    Padding(
                      padding: const EdgeInsets.all(10),
                      child: MarkdownBody(
                        data: item.thinking,
                        selectable: true,
                        styleSheet:
                            MarkdownStyleSheet.fromTheme(
                              Theme.of(context),
                            ).copyWith(
                              p: TextStyle(
                                color: Theme.of(
                                  context,
                                ).colorScheme.onSurfaceVariant,
                                fontSize: 13,
                                height: 1.35,
                              ),
                              code: const TextStyle(fontFamily: 'monospace'),
                            ),
                      ),
                    ),
                  ],
                ],
              ),
            ),
          ],
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
          if (collapsible && item.collapsed) ...[
            const SizedBox(height: 5),
            Text(
              item.role == 'Compaction summary'
                  ? 'Summary folded · tap panel to expand'
                  : 'Input and output folded · tap panel to expand',
              style: TextStyle(
                color: Theme.of(context).colorScheme.onSurfaceVariant,
                fontSize: 12,
              ),
            ),
          ],
          if (item.text.isNotEmpty && !item.collapsed) ...[
            const SizedBox(height: 7),
            if (item.toolName == 'write')
              _writeToolPreview(item)
            else if (item.toolName == 'replace' && item.toolDetails is Map)
              _replaceToolPreview(item)
            else if (item.toolName == 'cron')
              _cronToolPreview(item)
            else if (collapsible)
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
    );
    final child = collapsible
        ? GestureDetector(
            key: ValueKey('tool-panel-${item.id ?? item.hashCode}'),
            behavior: HitTestBehavior.opaque,
            onTap: () {
              if (item.role.startsWith('Tool ·')) {
                agent.toggleToolCollapsed(item);
              } else {
                setState(() => item.collapsed = !item.collapsed);
              }
            },
            child: card,
          )
        : card;
    return Align(
      alignment: item.role == 'You'
          ? Alignment.centerRight
          : Alignment.centerLeft,
      child: child,
    );
  }

  Widget _composerActionButton() {
    if (agent.streaming) {
      return FilledButton.icon(
        key: const ValueKey('composer-stop'),
        onPressed: agent.abort,
        style: FilledButton.styleFrom(
          backgroundColor: Theme.of(context).colorScheme.error,
          foregroundColor: Theme.of(context).colorScheme.onError,
        ),
        icon: const Icon(Icons.stop),
        label: const Text('Stop'),
      );
    }
    return FilledButton.icon(
      key: const ValueKey('composer-send'),
      onPressed: agent.connected ? _submit : null,
      style: FilledButton.styleFrom(
        backgroundColor: Colors.green.shade700,
        foregroundColor: Colors.white,
      ),
      icon: const Icon(Icons.arrow_upward),
      label: const Text('Send'),
    );
  }

  Widget _composer() => Padding(
    padding: const EdgeInsets.fromLTRB(16, 10, 16, 14),
    child: Column(
      children: [
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
                  final desktopInput =
                      !_isNativeMobile &&
                      MediaQuery.sizeOf(context).width >= 720;
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
            if (_isNativeMobile)
              Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  IconButton.outlined(
                    tooltip: 'Close keyboard',
                    onPressed: _dismissKeyboard,
                    icon: const Icon(Icons.keyboard_arrow_down),
                  ),
                  const SizedBox(height: 4),
                  _composerActionButton(),
                ],
              )
            else
              _composerActionButton(),
          ],
        ),
      ],
    ),
  );
}
