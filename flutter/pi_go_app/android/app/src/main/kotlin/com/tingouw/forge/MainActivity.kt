package com.tingouw.forge

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.util.Base64
import java.math.BigInteger
import java.nio.charset.StandardCharsets
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.SecureRandom
import java.security.Signature
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPublicKeySpec
import java.security.interfaces.ECPublicKey
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {
    companion object {
        private const val FEEDBACK_CHANNEL = "com.tingouw.forge/feedback"
        private const val IDENTITY_CHANNEL = "com.tingouw.forge/push_identity"
        private const val IDENTITY_EVENTS_CHANNEL = "com.tingouw.forge/push_identity_events"
        private const val IDENTITY_ALIAS = "forge-device-identity-v1"
        private const val DEVICE_ID_KEY = "forge.device.identity.id.v1"
        private const val FEEDBACK_NOTIFICATION_CHANNEL = "forge_feedback"
        private const val NOTIFICATION_PERMISSION_REQUEST = 7347
    }

    private var appForeground = false
    private var pushEventSink: EventChannel.EventSink? = null
    private val pendingPushEvents = mutableListOf<Map<String, Any>>()

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            getSystemService(NotificationManager::class.java)
                .deleteNotificationChannel("forge_connection")
        }
        EventChannel(flutterEngine.dartExecutor.binaryMessenger, IDENTITY_EVENTS_CHANNEL)
            .setStreamHandler(object : EventChannel.StreamHandler {
                override fun onListen(arguments: Any?, events: EventChannel.EventSink) {
                    pushEventSink = events
                    pendingPushEvents.forEach(events::success)
                    pendingPushEvents.clear()
                }
                override fun onCancel(arguments: Any?) { pushEventSink = null }
            })
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, IDENTITY_CHANNEL)
            .setMethodCallHandler { call, result ->
                try {
                    when (call.method) {
                        "getIdentity" -> result.success(identityDictionary())
                        "sign" -> result.success(signPayload(call.argument<String>("payload") ?: ""))
                        "verify" -> result.success(
                            verifyPayload(
                                call.argument<Map<String, Any>>("publicKey") ?: emptyMap(),
                                call.argument<String>("payload") ?: "",
                                call.argument<String>("signature") ?: "",
                            ),
                        )
                        "randomNonce" -> result.success(base64Url(ByteArray(32).also(SecureRandom()::nextBytes)))
                        "sha256" -> {
                            val bytes = call.argument<ByteArray>("bytes") ?: ByteArray(0)
                            result.success(base64Url(MessageDigest.getInstance("SHA-256").digest(bytes)))
                        }
                        "storePushKey" -> {
                            ForgePushContentCrypto.storeKey(
                                applicationContext,
                                call.argument<String>("agentId") ?: "",
                                call.argument<String>("deviceId") ?: "",
                                call.argument<String>("keyId") ?: "",
                                call.argument<String>("key") ?: "",
                            )
                            result.success(null)
                        }
                        "setVisibleSession" -> result.success(null)
                        else -> result.notImplemented()
                    }
                } catch (error: Exception) {
                    result.error("device_identity_error", error.message, null)
                }
            }
        refreshFcmToken()
        emitNotificationTap(intent)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, FEEDBACK_CHANNEL)
            .setMethodCallHandler { call, result ->
                when (call.method) {
                    "initialize" -> {
                        createFeedbackNotificationChannel()
                        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
                        ) {
                            requestPermissions(
                                arrayOf(Manifest.permission.POST_NOTIFICATIONS),
                                NOTIFICATION_PERMISSION_REQUEST,
                            )
                        }
                        result.success(null)
                    }
                    "show" -> {
                        val suppress = call.argument<Boolean>("suppressWhenForeground") == true
                        if (!appForeground || !suppress) {
                            showSessionNotification(
                                call.argument<String>("connection") ?: "Forge server",
                                call.argument<String>("session") ?: "",
                                call.argument<Boolean>("needsFeedback") == true,
                            )
                        }
                        result.success(null)
                    }
                    else -> result.notImplemented()
                }
            }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        emitNotificationTap(intent)
    }

    private fun refreshFcmToken() {
        com.google.firebase.messaging.FirebaseMessaging.getInstance().token
            .addOnSuccessListener { token ->
                if (token.isEmpty()) return@addOnSuccessListener
                getSharedPreferences("forge_fcm", MODE_PRIVATE).edit().putString("token", token).apply()
                val event = mapOf<String, Any>("type" to "fcmToken")
                pushEventSink?.success(event) ?: pendingPushEvents.add(event)
            }
    }

    private fun emitNotificationTap(intent: Intent?) {
        if (intent?.getBooleanExtra("forge.notificationTap", false) != true) return
        val event = mapOf<String, Any>(
            "type" to "notificationTap",
            "agentId" to (intent.getStringExtra("forge.agentId") ?: ""),
            "sessionId" to (intent.getStringExtra("forge.sessionId") ?: ""),
        )
        intent.removeExtra("forge.notificationTap")
        pushEventSink?.success(event) ?: pendingPushEvents.add(event)
    }

    override fun onResume() {
        super.onResume()
        appForeground = true
        refreshFcmToken()
    }

    override fun onPause() {
        appForeground = false
        super.onPause()
    }

    private fun identityDictionary(): Map<String, Any> {
        val keyPair = identityKeyPair()
        val publicKey = keyPair.public as ECPublicKey
        val x = fixedCoordinate(publicKey.w.affineX)
        val y = fixedCoordinate(publicKey.w.affineY)
        val xEncoded = base64Url(x)
        val yEncoded = base64Url(y)
        val fingerprint = base64Url(
            MessageDigest.getInstance("SHA-256")
                .digest("P-256.$xEncoded.$yEncoded".toByteArray(StandardCharsets.UTF_8)),
        )
        val preferences = getSharedPreferences("forge_identity", MODE_PRIVATE)
        var deviceId = preferences.getString(DEVICE_ID_KEY, null)
        if (deviceId == null) {
            deviceId = base64Url(ByteArray(18).also(SecureRandom()::nextBytes))
            preferences.edit().putString(DEVICE_ID_KEY, deviceId).apply()
        }
        return mapOf(
            "deviceId" to deviceId,
            "publicKey" to mapOf("kty" to "EC", "crv" to "P-256", "x" to xEncoded, "y" to yEncoded),
            "fingerprint" to fingerprint,
            "apnsToken" to getSharedPreferences("forge_fcm", MODE_PRIVATE).getString("token", "").orEmpty(),
            "apnsEnvironment" to "production",
        )
    }

    private fun identityKeyPair(): java.security.KeyPair {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if (store.containsAlias(IDENTITY_ALIAS)) {
            val privateKey = store.getKey(IDENTITY_ALIAS, null) as java.security.PrivateKey
            return java.security.KeyPair(store.getCertificate(IDENTITY_ALIAS).publicKey, privateKey)
        }
        val generator = KeyPairGenerator.getInstance("EC", "AndroidKeyStore")
        val specification = android.security.keystore.KeyGenParameterSpec.Builder(
            IDENTITY_ALIAS,
            android.security.keystore.KeyProperties.PURPOSE_SIGN or
                android.security.keystore.KeyProperties.PURPOSE_VERIFY,
        )
            .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
            .setDigests(android.security.keystore.KeyProperties.DIGEST_SHA256)
            .build()
        generator.initialize(specification)
        return generator.generateKeyPair()
    }

    private fun signPayload(payload: String): String {
        val signer = Signature.getInstance("SHA256withECDSA")
        signer.initSign(identityKeyPair().private)
        signer.update(payload.toByteArray(StandardCharsets.UTF_8))
        return base64Url(derToRaw(signer.sign()))
    }

    private fun verifyPayload(publicJwk: Map<String, Any>, payload: String, signature: String): Boolean {
        if (publicJwk["kty"] != "EC" || publicJwk["crv"] != "P-256") return false
        val x = BigInteger(1, decodeBase64Url(publicJwk["x"] as? String ?: return false))
        val y = BigInteger(1, decodeBase64Url(publicJwk["y"] as? String ?: return false))
        val parameters = (identityKeyPair().public as ECPublicKey).params
        val key = KeyFactory.getInstance("EC").generatePublic(ECPublicKeySpec(ECPoint(x, y), parameters))
        val verifier = Signature.getInstance("SHA256withECDSA")
        verifier.initVerify(key)
        verifier.update(payload.toByteArray(StandardCharsets.UTF_8))
        return verifier.verify(rawToDer(decodeBase64Url(signature)))
    }

    private fun fixedCoordinate(value: BigInteger): ByteArray {
        val source = value.toByteArray().dropWhile { it == 0.toByte() }.toByteArray()
        require(source.size <= 32)
        return ByteArray(32 - source.size) + source
    }

    private fun derToRaw(der: ByteArray): ByteArray {
        var index = 1
        readDerLength(der, intArrayOf(index)).also { index = it.second }
        require(der[index++] == 0x02.toByte())
        val rInfo = readDerLength(der, intArrayOf(index)); val rLength = rInfo.first; index = rInfo.second
        val r = der.copyOfRange(index, index + rLength); index += rLength
        require(der[index++] == 0x02.toByte())
        val sInfo = readDerLength(der, intArrayOf(index)); val sLength = sInfo.first; index = sInfo.second
        val s = der.copyOfRange(index, index + sLength)
        return fixedInteger(r) + fixedInteger(s)
    }

    private fun readDerLength(bytes: ByteArray, holder: IntArray): Pair<Int, Int> {
        var index = holder[0]
        val first = bytes[index++].toInt() and 0xff
        if (first < 0x80) return first to index
        val count = first and 0x7f
        var length = 0
        repeat(count) { length = (length shl 8) or (bytes[index++].toInt() and 0xff) }
        return length to index
    }

    private fun fixedInteger(value: ByteArray): ByteArray {
        val source = value.dropWhile { it == 0.toByte() }.toByteArray()
        require(source.size <= 32)
        return ByteArray(32 - source.size) + source
    }

    private fun rawToDer(raw: ByteArray): ByteArray {
        require(raw.size == 64)
        fun integer(value: ByteArray): ByteArray {
            var stripped = value.dropWhile { it == 0.toByte() }.toByteArray()
            if (stripped.isEmpty()) stripped = byteArrayOf(0)
            if ((stripped[0].toInt() and 0x80) != 0) stripped = byteArrayOf(0) + stripped
            return byteArrayOf(0x02, stripped.size.toByte()) + stripped
        }
        val r = integer(raw.copyOfRange(0, 32)); val s = integer(raw.copyOfRange(32, 64))
        val body = r + s
        return byteArrayOf(0x30, body.size.toByte()) + body
    }

    private fun base64Url(value: ByteArray): String =
        Base64.encodeToString(value, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)

    private fun decodeBase64Url(value: String): ByteArray =
        Base64.decode(value, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)

    private fun createFeedbackNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            getSystemService(NotificationManager::class.java).createNotificationChannel(
                NotificationChannel(
                    FEEDBACK_NOTIFICATION_CHANNEL,
                    "Agent feedback requests",
                    NotificationManager.IMPORTANCE_HIGH,
                ).apply { description = "Encrypted Forge agent notifications" },
            )
        }
    }

    private fun showSessionNotification(
        connection: String,
        session: String,
        needsFeedback: Boolean,
    ) {
        val manager = getSystemService(NotificationManager::class.java)
        createFeedbackNotificationChannel()
        val launchIntent = packageManager.getLaunchIntentForPackage(packageName)
            ?: Intent(this, MainActivity::class.java)
        val pendingIntent = PendingIntent.getActivity(
            this,
            1,
            launchIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, FEEDBACK_NOTIFICATION_CHANNEL)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }
        manager.notify(
            (session.ifEmpty { connection }).hashCode(),
            builder
                .setSmallIcon(R.mipmap.ic_launcher)
                .setContentTitle(
                    if (needsFeedback) "Forge needs your feedback" else "Forge session completed",
                )
                .setContentText(
                    if (needsFeedback) "$connection · approval required" else connection,
                )
                .setContentIntent(pendingIntent)
                .setCategory(Notification.CATEGORY_MESSAGE)
                .setAutoCancel(true)
                .build(),
        )
    }
}
