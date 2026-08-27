import UserNotifications
import CryptoKit
import Security

final class NotificationService: UNNotificationServiceExtension {
  private var contentHandler: ((UNNotificationContent) -> Void)?
  private var bestAttemptContent: UNMutableNotificationContent?

  override func didReceive(
    _ request: UNNotificationRequest,
    withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void
  ) {
    self.contentHandler = contentHandler
    guard let content = request.content.mutableCopy() as? UNMutableNotificationContent else {
      contentHandler(request.content)
      return
    }
    bestAttemptContent = content
    do {
      let decrypted = try PushContentCrypto.decrypt(userInfo: content.userInfo)
      content.title = decrypted.title
      content.body = decrypted.body
      content.threadIdentifier = decrypted.agentID
      NSLog("Forge encrypted push decrypted and authenticated")
      content.userInfo = [
        "agentId": decrypted.agentID,
        "sessionId": decrypted.sessionID,
        "event": decrypted.type,
        "eventId": decrypted.eventID,
      ]
      contentHandler(content)
    } catch {
      NSLog("Forge encrypted push rejected: %@", String(describing: type(of: error)))
      // Never display unauthenticated or undecryptable content.
      content.title = "Forge"
      content.body = "Encrypted notification unavailable"
      content.userInfo = [:]
      contentHandler(content)
    }
  }

  override func serviceExtensionTimeWillExpire() {
    if let contentHandler, let bestAttemptContent { contentHandler(bestAttemptContent) }
  }
}

private struct DecryptedPush {
  let agentID: String
  let sessionID: String
  let eventID: String
  let type: String
  let title: String
  let body: String
}

private enum PushContentCrypto {
  static let accessGroup = "QJ6C3M6J85.com.tingouw.forge.push-content"
  static let keychainService = "com.tingouw.forge.push-content-v1"

  static func decrypt(userInfo: [AnyHashable: Any]) throws -> DecryptedPush {
    guard (userInfo["version"] as? NSNumber)?.intValue == 1,
          let eventID = userInfo["eventId"] as? String,
          let keyID = userInfo["keyId"] as? String,
          let nonceText = userInfo["nonce"] as? String,
          let ciphertextText = userInfo["ciphertext"] as? String else { throw CryptoError.invalid }
    let metadata = try load(account: "meta.\(keyID)")
    guard let metadataText = String(data: metadata, encoding: .utf8) else { throw CryptoError.invalid }
    let parts = metadataText.split(separator: "\n", maxSplits: 1).map(String.init)
    guard parts.count == 2 else { throw CryptoError.invalid }
    let agentID = parts[0], deviceID = parts[1]
    let key = SymmetricKey(data: try load(account: "key.\(keyID)"))
    let nonce = try AES.GCM.Nonce(data: decodeBase64URL(nonceText))
    let combined = try decodeBase64URL(ciphertextText)
    guard combined.count >= 16 else { throw CryptoError.invalid }
    let ciphertext = combined.dropLast(16)
    let tag = combined.suffix(16)
    let box = try AES.GCM.SealedBox(nonce: nonce, ciphertext: ciphertext, tag: tag)
    let aad = ["FORGE-PUSH-CONTENT-V1", agentID, deviceID, eventID, keyID].joined(separator: "\n")
    let plaintext = try AES.GCM.open(box, using: key, authenticating: Data(aad.utf8))
    guard let json = try JSONSerialization.jsonObject(with: plaintext) as? [String: Any],
          let type = json["type"] as? String,
          ["approval_required", "session_completed"].contains(type),
          let sessionID = json["sessionId"] as? String,
          let title = json["title"] as? String,
          let body = json["body"] as? String,
          !sessionID.isEmpty, sessionID.count <= 200,
          !title.isEmpty, title.count <= 100,
          !body.isEmpty, body.count <= 300 else { throw CryptoError.invalid }
    return DecryptedPush(agentID: agentID, sessionID: sessionID, eventID: eventID, type: type, title: title, body: body)
  }

  private static func load(account: String) throws -> Data {
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: keychainService,
      kSecAttrAccount as String: account,
      kSecAttrAccessGroup as String: accessGroup,
      kSecReturnData as String: true,
      kSecMatchLimit as String: kSecMatchLimitOne,
    ]
    var result: CFTypeRef?
    guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
          let data = result as? Data else { throw CryptoError.invalid }
    return data
  }

  private static func decodeBase64URL(_ value: String) throws -> Data {
    var normalized = value.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
    normalized += String(repeating: "=", count: (4 - normalized.count % 4) % 4)
    guard let data = Data(base64Encoded: normalized) else { throw CryptoError.invalid }
    return data
  }

  private enum CryptoError: Error { case invalid }
}
