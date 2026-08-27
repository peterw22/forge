import CryptoKit
import Flutter
import Security
import UIKit
import UserNotifications

private struct PushIdentityRecord: Codable {
  let deviceID: String
  let privateKey: String
}

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  private let feedbackChannelName = "com.tingouw.forge/feedback"
  private let pushIdentityChannelName = "com.tingouw.forge/push_identity"
  private let pushEventsChannelName = "com.tingouw.forge/push_identity_events"
  private let keychainService = "com.tingouw.forge.push-identity"
  private let identityAccount = "device-identity-v1"
  private let apnsTokenKey = "forge.push.apns-token.v1"
  private var appForeground = false
  private var visibleAgentID = ""
  private var visibleSessionID = ""
  private var pushEventSink: FlutterEventSink?
  private var pendingPushEvents: [[String: Any]] = []

  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    UNUserNotificationCenter.current().delegate = self
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  func didInitializeImplicitFlutterEngine(_ engineBridge: FlutterImplicitEngineBridge) {
    GeneratedPluginRegistrant.register(with: engineBridge.pluginRegistry)
    let messenger = engineBridge.applicationRegistrar.messenger()
    let feedbackChannel = FlutterMethodChannel(name: feedbackChannelName, binaryMessenger: messenger)
    feedbackChannel.setMethodCallHandler { [weak self] call, result in
      guard let self else { result(nil); return }
      switch call.method {
      case "initialize": self.initializeNotifications(result: result)
      case "show": self.showSessionNotification(call: call, result: result)
      default: result(FlutterMethodNotImplemented)
      }
    }

    let identityChannel = FlutterMethodChannel(name: pushIdentityChannelName, binaryMessenger: messenger)
    identityChannel.setMethodCallHandler { [weak self] call, result in
      guard let self else { result(nil); return }
      do {
        switch call.method {
        case "getIdentity": result(try self.identityDictionary())
        case "sign":
          let arguments = call.arguments as? [String: Any]
          let payload = arguments?["payload"] as? String ?? ""
          result(try self.sign(payload: Data(payload.utf8)))
        case "verify":
          let arguments = call.arguments as? [String: Any]
          guard let publicKey = arguments?["publicKey"] as? [String: Any],
                let x = publicKey["x"] as? String,
                let y = publicKey["y"] as? String,
                let payload = arguments?["payload"] as? String,
                let signature = arguments?["signature"] as? String else {
            throw PushIdentityError.invalidArguments
          }
          let representation = Data([0x04]) + (try self.decodeBase64URL(x)) + (try self.decodeBase64URL(y))
          let key = try P256.Signing.PublicKey(x963Representation: representation)
          let rawSignature = try P256.Signing.ECDSASignature(rawRepresentation: self.decodeBase64URL(signature))
          result(key.isValidSignature(rawSignature, for: Data(payload.utf8)))
        case "sha256":
          let arguments = call.arguments as? [String: Any]
          guard let typed = arguments?["bytes"] as? FlutterStandardTypedData else {
            throw PushIdentityError.invalidArguments
          }
          result(self.base64URL(Data(SHA256.hash(data: typed.data))))
        case "randomNonce":
          var bytes = [UInt8](repeating: 0, count: 32)
          guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else {
            throw PushIdentityError.randomFailure
          }
          result(self.base64URL(Data(bytes)))
        case "storePushKey":
          let arguments = call.arguments as? [String: Any]
          guard let agentID = arguments?["agentId"] as? String,
                let deviceID = arguments?["deviceId"] as? String,
                let keyID = arguments?["keyId"] as? String,
                let encodedKey = arguments?["key"] as? String else {
            throw PushIdentityError.invalidArguments
          }
          try self.storePushContentKey(
            agentID: agentID, deviceID: deviceID, keyID: keyID, encodedKey: encodedKey
          )
          result(nil)
        case "setVisibleSession":
          let arguments = call.arguments as? [String: Any]
          self.visibleAgentID = arguments?["agentId"] as? String ?? ""
          self.visibleSessionID = arguments?["sessionId"] as? String ?? ""
          result(nil)
        default: result(FlutterMethodNotImplemented)
        }
      } catch {
        result(FlutterError(code: "push_identity_error", message: error.localizedDescription, details: nil))
      }
    }

    let events = FlutterEventChannel(name: pushEventsChannelName, binaryMessenger: messenger)
    events.setStreamHandler(self)
  }

  override func applicationDidBecomeActive(_ application: UIApplication) {
    appForeground = true
    super.applicationDidBecomeActive(application)
  }

  override func applicationWillResignActive(_ application: UIApplication) {
    appForeground = false
    super.applicationWillResignActive(application)
  }

  private func initializeNotifications(result: @escaping FlutterResult) {
    UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) {
      granted, error in
      DispatchQueue.main.async {
        if let error {
          result(FlutterError(code: "notification_permission_failed", message: error.localizedDescription, details: nil))
          return
        }
        if granted { UIApplication.shared.registerForRemoteNotifications() }
        result(granted)
      }
    }
  }

  override func application(
    _ application: UIApplication,
    didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
  ) {
    let token = deviceToken.map { String(format: "%02x", $0) }.joined()
    UserDefaults.standard.set(token, forKey: apnsTokenKey)
    emitPushEvent(["type": "apnsToken", "token": token, "environment": apnsEnvironment])
    super.application(application, didRegisterForRemoteNotificationsWithDeviceToken: deviceToken)
  }

  override func application(
    _ application: UIApplication,
    didFailToRegisterForRemoteNotificationsWithError error: Error
  ) {
    emitPushEvent(["type": "apnsError", "message": error.localizedDescription])
    super.application(application, didFailToRegisterForRemoteNotificationsWithError: error)
  }

  private var apnsEnvironment: String {
    #if DEBUG
    return "development"
    #else
    guard let profileURL = Bundle.main.url(forResource: "embedded", withExtension: "mobileprovision"),
          let profile = try? String(contentsOf: profileURL, encoding: .isoLatin1) else {
      return "production"
    }
    return profile.contains("<string>development</string>") ? "development" : "production"
    #endif
  }

  private func identityDictionary() throws -> [String: Any] {
    let record = try identityRecord()
    let key = try P256.Signing.PrivateKey(rawRepresentation: decodeBase64URL(record.privateKey))
    let representation = key.publicKey.x963Representation
    guard representation.count == 65, representation.first == 0x04 else {
      throw PushIdentityError.invalidPublicKey
    }
    let x = representation.subdata(in: 1..<33)
    let y = representation.subdata(in: 33..<65)
    let fingerprint = base64URL(Data(SHA256.hash(data: Data("P-256.\(base64URL(x)).\(base64URL(y))".utf8))))
    return [
      "deviceId": record.deviceID,
      "publicKey": ["kty": "EC", "crv": "P-256", "x": base64URL(x), "y": base64URL(y)],
      "fingerprint": fingerprint,
      "apnsToken": UserDefaults.standard.string(forKey: apnsTokenKey) ?? "",
      "apnsEnvironment": apnsEnvironment,
    ]
  }

  private func identityRecord() throws -> PushIdentityRecord {
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: keychainService,
      kSecAttrAccount as String: identityAccount,
      kSecReturnData as String: true,
      kSecMatchLimit as String: kSecMatchLimitOne,
    ]
    var item: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &item)
    if status == errSecSuccess, let data = item as? Data {
      do {
        return try JSONDecoder().decode(PushIdentityRecord.self, from: data)
      } catch {
        throw PushIdentityError.invalidStoredIdentity
      }
    }
    if status != errSecItemNotFound { throw PushIdentityError.keychain(status) }

    let key = P256.Signing.PrivateKey()
    var idBytes = [UInt8](repeating: 0, count: 18)
    guard SecRandomCopyBytes(kSecRandomDefault, idBytes.count, &idBytes) == errSecSuccess else {
      throw PushIdentityError.randomFailure
    }
    let record = PushIdentityRecord(
      deviceID: base64URL(Data(idBytes)),
      privateKey: base64URL(key.rawRepresentation)
    )
    let encoded = try JSONEncoder().encode(record)
    let add: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: keychainService,
      kSecAttrAccount as String: identityAccount,
      kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
      kSecValueData as String: encoded,
    ]
    let addStatus = SecItemAdd(add as CFDictionary, nil)
    if addStatus != errSecSuccess { throw PushIdentityError.keychain(addStatus) }
    return record
  }

  private func storePushContentKey(
    agentID: String, deviceID: String, keyID: String, encodedKey: String
  ) throws {
    let valid = { (value: String) in
      value.count >= 16 && value.count <= 128 &&
        value.allSatisfy { $0.isLetter || $0.isNumber || $0 == "_" || $0 == "-" }
    }
    guard valid(agentID), valid(deviceID), valid(keyID) else { throw PushIdentityError.invalidArguments }
    let key = try decodeBase64URL(encodedKey)
    guard key.count == 32 else { throw PushIdentityError.invalidArguments }
    let metadata = Data("\(agentID)\n\(deviceID)".utf8)
    try upsertPushContent(account: "key.\(keyID)", data: key)
    try upsertPushContent(account: "meta.\(keyID)", data: metadata)
  }

  private func upsertPushContent(account: String, data: Data) throws {
    let accessGroup = "QJ6C3M6J85.com.tingouw.forge.push-content"
    let service = "com.tingouw.forge.push-content-v1"
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: service,
      kSecAttrAccount as String: account,
      kSecAttrAccessGroup as String: accessGroup,
    ]
    let status = SecItemCopyMatching(query as CFDictionary, nil)
    if status == errSecSuccess {
      let update = SecItemUpdate(query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
      if update != errSecSuccess { throw PushIdentityError.keychain(update) }
    } else if status == errSecItemNotFound {
      var add = query
      add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
      add[kSecValueData as String] = data
      let addStatus = SecItemAdd(add as CFDictionary, nil)
      if addStatus != errSecSuccess { throw PushIdentityError.keychain(addStatus) }
    } else {
      throw PushIdentityError.keychain(status)
    }
  }

  private func sign(payload: Data) throws -> String {
    let record = try identityRecord()
    let key = try P256.Signing.PrivateKey(rawRepresentation: decodeBase64URL(record.privateKey))
    return base64URL(try key.signature(for: payload).rawRepresentation)
  }

  private func decodeBase64URL(_ value: String) throws -> Data {
    var normalized = value.replacingOccurrences(of: "-", with: "+")
      .replacingOccurrences(of: "_", with: "/")
    normalized += String(repeating: "=", count: (4 - normalized.count % 4) % 4)
    guard let data = Data(base64Encoded: normalized) else { throw PushIdentityError.invalidArguments }
    return data
  }

  private func base64URL(_ data: Data) -> String {
    data.base64EncodedString().replacingOccurrences(of: "+", with: "-")
      .replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
  }

  private func showSessionNotification(call: FlutterMethodCall, result: @escaping FlutterResult) {
    let arguments = call.arguments as? [String: Any]
    let suppress = arguments?["suppressWhenForeground"] as? Bool == true
    if appForeground && suppress { result(nil); return }
    let connection = arguments?["connection"] as? String ?? "Forge server"
    let session = arguments?["session"] as? String ?? ""
    let needsFeedback = arguments?["needsFeedback"] as? Bool == true
    let content = UNMutableNotificationContent()
    content.title = needsFeedback ? "Forge needs your feedback" : "Forge session completed"
    content.body = needsFeedback ? "\(connection) · approval required" : connection
    content.sound = .default
    content.threadIdentifier = session.isEmpty ? connection : session
    content.userInfo = ["session": session]
    let identifier = "forge.session.\(session.isEmpty ? connection : session)"
    UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: identifier, content: content, trigger: nil)) { error in
      DispatchQueue.main.async {
        if let error { result(FlutterError(code: "notification_failed", message: error.localizedDescription, details: nil)) }
        else { result(nil) }
      }
    }
  }

  override func userNotificationCenter(
    _ center: UNUserNotificationCenter,
    willPresent notification: UNNotification,
    withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
  ) {
    let info = notification.request.content.userInfo
    let agentID = info["agentId"] as? String ?? ""
    let sessionID = info["sessionId"] as? String ?? ""
    if !agentID.isEmpty && agentID == visibleAgentID && sessionID == visibleSessionID {
      completionHandler([])
      return
    }
    if #available(iOS 14.0, *) { completionHandler([.banner, .sound]) }
    else { completionHandler([.alert, .sound]) }
  }

  override func userNotificationCenter(
    _ center: UNUserNotificationCenter,
    didReceive response: UNNotificationResponse,
    withCompletionHandler completionHandler: @escaping () -> Void
  ) {
    var event: [String: Any] = [:]
    for (key, value) in response.notification.request.content.userInfo {
      if let key = key as? String { event[key] = value }
    }
    event["type"] = "notificationTap"
    emitPushEvent(event)
    super.userNotificationCenter(center, didReceive: response, withCompletionHandler: completionHandler)
  }

  private func emitPushEvent(_ event: [String: Any]) {
    DispatchQueue.main.async {
      if let sink = self.pushEventSink { sink(event) }
      else { self.pendingPushEvents.append(event) }
    }
  }
}

extension AppDelegate: FlutterStreamHandler {
  func onListen(withArguments arguments: Any?, eventSink events: @escaping FlutterEventSink) -> FlutterError? {
    pushEventSink = events
    for event in pendingPushEvents { events(event) }
    pendingPushEvents.removeAll()
    return nil
  }

  func onCancel(withArguments arguments: Any?) -> FlutterError? {
    pushEventSink = nil
    return nil
  }
}

enum PushIdentityError: LocalizedError {
  case invalidArguments, randomFailure, publicKeyUnavailable, invalidPublicKey
  case keychain(OSStatus), keyGeneration, signingFailure, invalidSignature, invalidStoredIdentity

  var errorDescription: String? {
    switch self {
    case .invalidArguments: return "Invalid push identity arguments"
    case .randomFailure: return "Could not generate secure random bytes"
    case .publicKeyUnavailable: return "Push identity public key is unavailable"
    case .invalidPublicKey: return "Push identity public key is invalid"
    case .keychain(let status): return "Keychain error \(status)"
    case .keyGeneration: return "Could not generate push identity key"
    case .signingFailure: return "Could not sign push identity payload"
    case .invalidSignature: return "Push identity signature is invalid"
    case .invalidStoredIdentity: return "Stored push identity is invalid"
    }
  }
}
