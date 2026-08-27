import Cocoa
import FlutterMacOS
import UserNotifications

@main
class AppDelegate: FlutterAppDelegate, UNUserNotificationCenterDelegate, FlutterStreamHandler {
  private let apnsTokenKey = "forge.push.apns-token.v1"
  private var pushEventSink: FlutterEventSink?
  private var pendingPushEvents: [[String: Any]] = []
  private var visibleAgentID = ""
  private var visibleSessionID = ""

  override func applicationDidFinishLaunching(_ notification: Notification) {
    UNUserNotificationCenter.current().delegate = self
    super.applicationDidFinishLaunching(notification)
  }

  func initializeRemoteNotifications(completion: @escaping (Bool, Error?) -> Void) {
    UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) {
      granted, error in
      DispatchQueue.main.async {
        if granted { NSApplication.shared.registerForRemoteNotifications() }
        completion(granted, error)
      }
    }
  }

  override func application(
    _ application: NSApplication,
    didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
  ) {
    let token = deviceToken.map { String(format: "%02x", $0) }.joined()
    UserDefaults.standard.set(token, forKey: apnsTokenKey)
    emitPushEvent(["type": "apnsToken", "token": token, "environment": apnsEnvironment])
  }

  override func application(
    _ application: NSApplication,
    didFailToRegisterForRemoteNotificationsWithError error: Error
  ) {
    emitPushEvent(["type": "apnsError", "message": error.localizedDescription])
  }

  var apnsToken: String {
    UserDefaults.standard.string(forKey: apnsTokenKey) ?? ""
  }

  var apnsEnvironment: String {
    #if DEBUG
    return "development"
    #else
    return "production"
    #endif
  }

  func setVisibleSession(agentID: String, sessionID: String) {
    visibleAgentID = agentID
    visibleSessionID = sessionID
  }

  override func application(
    _ application: NSApplication,
    didReceiveRemoteNotification userInfo: [String: Any]
  ) {
    guard let decrypted = PushDecryptor.decryptUserInfo(userInfo as NSDictionary),
          let agentID = decrypted["agentId"] as? String,
          let sessionID = decrypted["sessionId"] as? String,
          let eventID = decrypted["eventId"] as? String,
          let type = decrypted["event"] as? String,
          let title = decrypted["title"] as? String,
          let body = decrypted["body"] as? String else {
      NSLog("Forge encrypted background push rejected")
      return
    }
    NSLog("Forge encrypted background push decrypted")
    let content = UNMutableNotificationContent()
    content.title = title
    content.body = body
    content.sound = .default
    content.threadIdentifier = agentID
    content.userInfo = [
      "agentId": agentID, "sessionId": sessionID,
      "eventId": eventID, "event": type,
    ]
    UNUserNotificationCenter.current().add(
      UNNotificationRequest(identifier: eventID, content: content, trigger: nil)
    )
  }

  func userNotificationCenter(
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
    if #available(macOS 11.0, *) { completionHandler([.banner, .sound]) }
    else { completionHandler([.alert, .sound]) }
  }

  func userNotificationCenter(
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
    completionHandler()
  }

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

  private func emitPushEvent(_ event: [String: Any]) {
    DispatchQueue.main.async {
      if let sink = self.pushEventSink { sink(event) }
      else { self.pendingPushEvents.append(event) }
    }
  }

  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    return true
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }
}
