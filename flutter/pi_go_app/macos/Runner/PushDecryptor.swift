import Foundation
import CryptoKit

@objc(ForgePushDecryptor)
final class PushDecryptor: NSObject {
  @objc(decryptUserInfo:)
  static func decryptUserInfo(_ userInfo: NSDictionary) -> NSDictionary? {
    do {
      let decrypted = try PushContentCrypto.decrypt(userInfo: userInfo as? [AnyHashable: Any] ?? [:])
      return [
        "agentId": decrypted.agentID,
        "sessionId": decrypted.sessionID,
        "eventId": decrypted.eventID,
        "event": decrypted.type,
        "title": decrypted.title,
        "body": decrypted.body,
      ]
    } catch {
      return nil
    }
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
  static func decrypt(userInfo: [AnyHashable: Any]) throws -> DecryptedPush {
    guard (userInfo["version"] as? NSNumber)?.intValue == 1,
          let eventID = userInfo["eventId"] as? String,
          let keyID = userInfo["keyId"] as? String,
          let nonceText = userInfo["nonce"] as? String,
          let ciphertextText = userInfo["ciphertext"] as? String else { throw CryptoError.invalid }
    let record = try loadRecord(keyID: keyID)
    let agentID = record.agentId, deviceID = record.deviceId
    let key = SymmetricKey(data: try decodeBase64URL(record.key))
    let nonce = try AES.GCM.Nonce(data: decodeBase64URL(nonceText))
    let combined = try decodeBase64URL(ciphertextText)
    guard combined.count >= 16 else { throw CryptoError.invalid }
    let box = try AES.GCM.SealedBox(
      nonce: nonce,
      ciphertext: combined.dropLast(16),
      tag: combined.suffix(16)
    )
    let aad = ["FORGE-PUSH-CONTENT-V1", agentID, deviceID, eventID, keyID]
      .joined(separator: "\n")
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
    return DecryptedPush(
      agentID: agentID, sessionID: sessionID, eventID: eventID,
      type: type, title: title, body: body
    )
  }

  private struct StoredKey: Decodable {
    let agentId: String
    let deviceId: String
    let key: String
  }

  private static func loadRecord(keyID: String) throws -> StoredKey {
    guard keyID.count >= 16, keyID.count <= 128,
          keyID.allSatisfy({ $0.isLetter || $0.isNumber || $0 == "_" || $0 == "-" }) else {
      throw CryptoError.invalid
    }
    let manager = FileManager.default
    let support = try manager.url(
      for: .applicationSupportDirectory, in: .userDomainMask,
      appropriateFor: nil, create: false
    )
    let url = support.appendingPathComponent("Forge/PushContent/\(keyID).json")
    let attributes = try manager.attributesOfItem(atPath: url.path)
    guard let permissions = attributes[.posixPermissions] as? NSNumber,
          permissions.intValue & 0o077 == 0 else { throw CryptoError.invalid }
    let data = try Data(contentsOf: url, options: [.mappedIfSafe])
    guard data.count <= 2048 else { throw CryptoError.invalid }
    return try JSONDecoder().decode(StoredKey.self, from: data)
  }

  private static func decodeBase64URL(_ value: String) throws -> Data {
    var normalized = value.replacingOccurrences(of: "-", with: "+")
      .replacingOccurrences(of: "_", with: "/")
    normalized += String(repeating: "=", count: (4 - normalized.count % 4) % 4)
    guard let data = Data(base64Encoded: normalized) else { throw CryptoError.invalid }
    return data
  }

  private enum CryptoError: Error { case invalid }
}
