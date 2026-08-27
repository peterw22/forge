import Cocoa
import CryptoKit
import FlutterMacOS
import Security
import UserNotifications

private struct MacDeviceIdentityRecord: Codable {
  let deviceID: String
  let privateKey: String
}

class MainFlutterWindow: NSWindow {
  private let identityService = "com.tingouw.forge.device-identity"
  private let identityAccount = "device-identity-v1"

  override func awakeFromNib() {
    let flutterViewController = FlutterViewController()
    let windowFrame = self.frame
    self.contentViewController = flutterViewController
    self.setFrame(windowFrame, display: true)

    RegisterGeneratedPlugins(registry: flutterViewController)

    let identityChannel = FlutterMethodChannel(
      name: "com.tingouw.forge/push_identity",
      binaryMessenger: flutterViewController.engine.binaryMessenger
    )
    identityChannel.setMethodCallHandler { [weak self] call, result in
      guard let self else { result(nil); return }
      do {
        switch call.method {
        case "getIdentity": result(try self.identityDictionary())
        case "sign":
          let arguments = call.arguments as? [String: Any]
          result(try self.sign(payload: Data((arguments?["payload"] as? String ?? "").utf8)))
        case "verify":
          let arguments = call.arguments as? [String: Any]
          guard let publicKey = arguments?["publicKey"] as? [String: Any],
                let x = publicKey["x"] as? String,
                let y = publicKey["y"] as? String,
                let payload = arguments?["payload"] as? String,
                let signature = arguments?["signature"] as? String else {
            throw MacIdentityError.invalidArguments
          }
          let representation = Data([0x04]) + (try self.decodeBase64URL(x)) + (try self.decodeBase64URL(y))
          let key = try P256.Signing.PublicKey(x963Representation: representation)
          let rawSignature = try P256.Signing.ECDSASignature(rawRepresentation: self.decodeBase64URL(signature))
          result(key.isValidSignature(rawSignature, for: Data(payload.utf8)))
        case "sha256":
          let arguments = call.arguments as? [String: Any]
          guard let typed = arguments?["bytes"] as? FlutterStandardTypedData else {
            throw MacIdentityError.invalidArguments
          }
          result(self.base64URL(Data(SHA256.hash(data: typed.data))))
        case "randomNonce":
          var bytes = [UInt8](repeating: 0, count: 32)
          guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else {
            throw MacIdentityError.randomFailure
          }
          result(self.base64URL(Data(bytes)))
        case "storePushKey":
          let arguments = call.arguments as? [String: Any]
          guard let agentID = arguments?["agentId"] as? String,
                let deviceID = arguments?["deviceId"] as? String,
                let keyID = arguments?["keyId"] as? String,
                let encodedKey = arguments?["key"] as? String else {
            throw MacIdentityError.invalidArguments
          }
          try self.storePushContentKey(
            agentID: agentID, deviceID: deviceID, keyID: keyID, encodedKey: encodedKey
          )
          result(nil)
        case "setVisibleSession":
          let arguments = call.arguments as? [String: Any]
          (NSApp.delegate as? AppDelegate)?.setVisibleSession(
            agentID: arguments?["agentId"] as? String ?? "",
            sessionID: arguments?["sessionId"] as? String ?? ""
          )
          result(nil)
        default: result(FlutterMethodNotImplemented)
        }
      } catch {
        result(FlutterError(code: "device_identity_error", message: error.localizedDescription, details: nil))
      }
    }

    let pushEventsChannel = FlutterEventChannel(
      name: "com.tingouw.forge/push_identity_events",
      binaryMessenger: flutterViewController.engine.binaryMessenger
    )
    if let appDelegate = NSApp.delegate as? AppDelegate {
      pushEventsChannel.setStreamHandler(appDelegate)
    }

    let feedbackChannel = FlutterMethodChannel(
      name: "com.tingouw.forge/feedback",
      binaryMessenger: flutterViewController.engine.binaryMessenger
    )
    feedbackChannel.setMethodCallHandler { [weak self] call, result in
      if call.method == "initialize" {
        guard let appDelegate = NSApp.delegate as? AppDelegate else {
          result(FlutterError(code: "notification_setup_failed", message: "App delegate is unavailable", details: nil))
          return
        }
        appDelegate.initializeRemoteNotifications { granted, error in
          if let error {
            result(FlutterError(code: "notification_permission_failed", message: error.localizedDescription, details: nil))
          } else {
            result(granted)
          }
        }
        return
      }
      guard call.method == "show" else {
        result(FlutterMethodNotImplemented)
        return
      }
      let arguments = call.arguments as? [String: Any]
      let suppress = arguments?["suppressWhenForeground"] as? Bool ?? false
      if suppress && self?.isKeyWindow == true && NSApp.isActive {
        result(nil)
        return
      }
      let connection = arguments?["connection"] as? String ?? "Forge server"
      let needsFeedback = arguments?["needsFeedback"] as? Bool ?? false
      let content = UNMutableNotificationContent()
      content.title = needsFeedback ? "Forge needs your feedback" : "Forge session completed"
      content.body = needsFeedback ? "\(connection) · approval required" : connection
      content.sound = .default
      let request = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
      let center = UNUserNotificationCenter.current()
      center.requestAuthorization(options: [.alert, .sound]) { granted, _ in
        guard granted else { return }
        center.add(request)
      }
      result(nil)
    }

    super.awakeFromNib()
  }

  private func identityDictionary() throws -> [String: Any] {
    let record = try identityRecord()
    let key = try P256.Signing.PrivateKey(rawRepresentation: decodeBase64URL(record.privateKey))
    let publicKey = key.publicKey.x963Representation
    guard publicKey.count == 65, publicKey.first == 0x04 else { throw MacIdentityError.invalidPublicKey }
    let x = publicKey.subdata(in: 1..<33)
    let y = publicKey.subdata(in: 33..<65)
    let xEncoded = base64URL(x)
    let yEncoded = base64URL(y)
    let fingerprint = base64URL(Data(SHA256.hash(data: Data("P-256.\(xEncoded).\(yEncoded)".utf8))))
    return [
      "deviceId": record.deviceID,
      "publicKey": ["kty": "EC", "crv": "P-256", "x": xEncoded, "y": yEncoded],
      "fingerprint": fingerprint,
      "apnsToken": (NSApp.delegate as? AppDelegate)?.apnsToken ?? "",
      "apnsEnvironment": (NSApp.delegate as? AppDelegate)?.apnsEnvironment ?? "production",
    ]
  }

  private func identityRecord() throws -> MacDeviceIdentityRecord {
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: identityService,
      kSecAttrAccount as String: identityAccount,
      kSecReturnData as String: true,
      kSecMatchLimit as String: kSecMatchLimitOne,
    ]
    var item: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &item)
    if status == errSecSuccess, let data = item as? Data {
      return try JSONDecoder().decode(MacDeviceIdentityRecord.self, from: data)
    }
    if status != errSecItemNotFound { throw MacIdentityError.keychain(status) }
    let key = P256.Signing.PrivateKey()
    var bytes = [UInt8](repeating: 0, count: 18)
    guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else {
      throw MacIdentityError.randomFailure
    }
    let record = MacDeviceIdentityRecord(deviceID: base64URL(Data(bytes)), privateKey: base64URL(key.rawRepresentation))
    let data = try JSONEncoder().encode(record)
    let add: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: identityService,
      kSecAttrAccount as String: identityAccount,
      kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
      kSecValueData as String: data,
    ]
    let addStatus = SecItemAdd(add as CFDictionary, nil)
    if addStatus != errSecSuccess { throw MacIdentityError.keychain(addStatus) }
    return record
  }

  private func storePushContentKey(
    agentID: String, deviceID: String, keyID: String, encodedKey: String
  ) throws {
    let valid = { (value: String) in
      value.count >= 16 && value.count <= 128 &&
        value.allSatisfy { $0.isLetter || $0.isNumber || $0 == "_" || $0 == "-" }
    }
    guard valid(agentID), valid(deviceID), valid(keyID),
          try decodeBase64URL(encodedKey).count == 32 else { throw MacIdentityError.invalidArguments }
    let manager = FileManager.default
    let support = try manager.url(
      for: .applicationSupportDirectory, in: .userDomainMask,
      appropriateFor: nil, create: true
    )
    let directory = support.appendingPathComponent("Forge/PushContent", isDirectory: true)
    try manager.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
    try manager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
    let record: [String: String] = ["agentId": agentID, "deviceId": deviceID, "key": encodedKey]
    let data = try JSONSerialization.data(withJSONObject: record, options: [.sortedKeys])
    let destination = directory.appendingPathComponent("\(keyID).json")
    let temporary = directory.appendingPathComponent(".\(keyID).\(UUID().uuidString).tmp")
    guard manager.createFile(atPath: temporary.path, contents: data, attributes: [.posixPermissions: 0o600]) else {
      throw MacIdentityError.invalidArguments
    }
    if manager.fileExists(atPath: destination.path) { try manager.removeItem(at: destination) }
    try manager.moveItem(at: temporary, to: destination)
    try manager.setAttributes([.posixPermissions: 0o600], ofItemAtPath: destination.path)
  }

  private func sign(payload: Data) throws -> String {
    let record = try identityRecord()
    let key = try P256.Signing.PrivateKey(rawRepresentation: decodeBase64URL(record.privateKey))
    return base64URL(try key.signature(for: payload).rawRepresentation)
  }

  private func decodeBase64URL(_ value: String) throws -> Data {
    var normalized = value.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
    normalized += String(repeating: "=", count: (4 - normalized.count % 4) % 4)
    guard let data = Data(base64Encoded: normalized) else { throw MacIdentityError.invalidArguments }
    return data
  }

  private func base64URL(_ data: Data) -> String {
    data.base64EncodedString().replacingOccurrences(of: "+", with: "-")
      .replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
  }
}

enum MacIdentityError: LocalizedError {
  case invalidArguments, randomFailure, invalidPublicKey, keychain(OSStatus)
  var errorDescription: String? {
    switch self {
    case .invalidArguments: return "Invalid device identity arguments"
    case .randomFailure: return "Could not generate secure random bytes"
    case .invalidPublicKey: return "Device public key is invalid"
    case .keychain(let status): return "Keychain error \(status)"
    }
  }
}
