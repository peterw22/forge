package com.tingouw.forge

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Base64
import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import java.nio.charset.StandardCharsets
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec
import org.json.JSONObject

object ForgePushContentCrypto {
    private const val TAG = "ForgeEncryptedPush"
    private const val KEYSTORE = "AndroidKeyStore"
    private const val KEY_ALIAS_PREFIX = "forge-push-content-v1-"
    private const val PREFERENCES = "forge_push_content_v1"
    private const val CHANNEL_ID = "forge_feedback"

    fun storeKey(
        context: Context,
        agentId: String,
        deviceId: String,
        keyId: String,
        encodedKey: String,
    ) {
        requireIdentifier(agentId, "agent ID")
        requireIdentifier(deviceId, "device ID")
        requireIdentifier(keyId, "key ID")
        val raw = decodeBase64Url(encodedKey)
        require(raw.size == 32) { "Push content key must contain 256 bits" }
        val store = KeyStore.getInstance(KEYSTORE).apply { load(null) }
        val protection = android.security.keystore.KeyProtection.Builder(
            android.security.keystore.KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(android.security.keystore.KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(android.security.keystore.KeyProperties.ENCRYPTION_PADDING_NONE)
            .build()
        store.setEntry(
            keyAlias(keyId),
            KeyStore.SecretKeyEntry(SecretKeySpec(raw, "AES")),
            protection,
        )
        // Only non-secret routing metadata is stored in preferences. The AES key
        // remains a non-exportable Android Keystore entry after import.
        context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
            .edit()
            .putString("meta.$keyId", "$agentId\n$deviceId")
            .putString("current.$agentId", keyId)
            .apply()
        raw.fill(0)
    }

    fun decrypt(context: Context, data: Map<String, String>): DecryptedPush {
        require(data["version"] == "1") { "Unsupported encrypted push version" }
        val keyId = data["keyId"] ?: error("Missing push key ID")
        val eventId = data["eventId"] ?: error("Missing push event ID")
        requireIdentifier(keyId, "key ID")
        requireIdentifier(eventId, "event ID")
        val metadata = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
            .getString("meta.$keyId", null) ?: error("Unknown push key")
        val parts = metadata.split('\n', limit = 2)
        require(parts.size == 2) { "Invalid push key metadata" }
        val agentId = parts[0]
        val deviceId = parts[1]
        requireIdentifier(agentId, "agent ID")
        requireIdentifier(deviceId, "device ID")
        val nonce = decodeBase64Url(data["nonce"] ?: error("Missing push nonce"))
        val ciphertext = decodeBase64Url(data["ciphertext"] ?: error("Missing push ciphertext"))
        require(nonce.size == 12) { "Push nonce must contain 96 bits" }
        require(ciphertext.size in 17..8192) { "Invalid encrypted push size" }
        val store = KeyStore.getInstance(KEYSTORE).apply { load(null) }
        val key = store.getKey(keyAlias(keyId), null) as? SecretKey ?: error("Push key unavailable")
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, nonce))
        val aad = listOf(
            "FORGE-PUSH-CONTENT-V1",
            agentId,
            deviceId,
            eventId,
            keyId,
        ).joinToString("\n")
        cipher.updateAAD(aad.toByteArray(StandardCharsets.UTF_8))
        val plaintext = cipher.doFinal(ciphertext)
        require(plaintext.size <= 4096) { "Decrypted push is too large" }
        val json = JSONObject(String(plaintext, StandardCharsets.UTF_8))
        val type = json.getString("type")
        require(type == "approval_required" || type == "session_completed") {
            "Unsupported push event"
        }
        val sessionId = json.getString("sessionId")
        val title = json.getString("title")
        val body = json.getString("body")
        require(sessionId.isNotEmpty() && sessionId.length <= 200) { "Invalid push session" }
        require(title.isNotEmpty() && title.length <= 100) { "Invalid push title" }
        require(body.isNotEmpty() && body.length <= 300) { "Invalid push body" }
        return DecryptedPush(agentId, sessionId, eventId, type, title, body)
    }

    fun showNotification(context: Context, push: DecryptedPush) {
        val manager = context.getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            manager.createNotificationChannel(
                NotificationChannel(
                    CHANNEL_ID,
                    "Agent feedback requests",
                    NotificationManager.IMPORTANCE_HIGH,
                ).apply { description = "Encrypted Forge agent notifications" },
            )
        }
        val launchIntent = context.packageManager.getLaunchIntentForPackage(context.packageName)
            ?: Intent(context, MainActivity::class.java)
        launchIntent.apply {
            flags = Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP
            putExtra("forge.notificationTap", true)
            putExtra("forge.agentId", push.agentId)
            putExtra("forge.sessionId", push.sessionId)
        }
        val pendingIntent = PendingIntent.getActivity(
            context,
            push.eventId.hashCode(),
            launchIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(context, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(context)
        }
        manager.notify(
            push.eventId.hashCode(),
            builder
                .setSmallIcon(R.mipmap.ic_launcher)
                .setContentTitle(push.title)
                .setContentText(push.body)
                .setStyle(Notification.BigTextStyle().bigText(push.body))
                .setContentIntent(pendingIntent)
                .setCategory(Notification.CATEGORY_MESSAGE)
                .setAutoCancel(true)
                .build(),
        )
    }

    private fun keyAlias(keyId: String) = KEY_ALIAS_PREFIX + keyId

    private fun requireIdentifier(value: String, name: String) {
        require(value.length in 16..128 && value.all { it.isLetterOrDigit() || it == '_' || it == '-' }) {
            "Invalid $name"
        }
    }

    private fun decodeBase64Url(value: String): ByteArray =
        Base64.decode(value, Base64.URL_SAFE or Base64.NO_WRAP or Base64.NO_PADDING)
}

data class DecryptedPush(
    val agentId: String,
    val sessionId: String,
    val eventId: String,
    val type: String,
    val title: String,
    val body: String,
)

class ForgeEncryptedMessagingService : FirebaseMessagingService() {
    override fun onMessageReceived(message: RemoteMessage) {
        try {
            val push = ForgePushContentCrypto.decrypt(applicationContext, message.data)
            ForgePushContentCrypto.showNotification(applicationContext, push)
        } catch (error: Exception) {
            // Never log ciphertext, key identifiers, routing metadata, or plaintext.
            Log.w("ForgeEncryptedPush", "Encrypted push rejected: ${error.javaClass.simpleName}")
        }
    }

    override fun onNewToken(token: String) {
        getSharedPreferences("forge_fcm", Context.MODE_PRIVATE)
            .edit().putString("token", token).apply()
        // The main app re-reads and signs token registration on the next launch/resume.
    }
}
