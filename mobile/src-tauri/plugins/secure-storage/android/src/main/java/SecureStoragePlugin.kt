package net.j_code.mobile.securestorage

import android.app.Activity
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import app.tauri.annotation.Command
import app.tauri.annotation.InvokeArg
import app.tauri.annotation.TauriPlugin
import app.tauri.plugin.Invoke
import app.tauri.plugin.JSObject
import app.tauri.plugin.Plugin
import java.nio.ByteBuffer
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

@InvokeArg
class KeyArgs { lateinit var key: String }

@InvokeArg
class SetArgs {
    lateinit var key: String
    lateinit var value: String
}

@TauriPlugin
class SecureStoragePlugin(private val activity: Activity) : Plugin(activity) {
    private val preferences by lazy {
        activity.getSharedPreferences("jcode_secure_storage", Activity.MODE_PRIVATE)
    }

    private fun secretKey(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        generator.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .build(),
        )
        return generator.generateKey()
    }

    private fun encrypt(value: String): String {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, secretKey())
        val ciphertext = cipher.doFinal(value.toByteArray(Charsets.UTF_8))
        val packed = ByteBuffer.allocate(4 + cipher.iv.size + ciphertext.size)
            .putInt(cipher.iv.size)
            .put(cipher.iv)
            .put(ciphertext)
            .array()
        return Base64.encodeToString(packed, Base64.NO_WRAP)
    }

    private fun decrypt(value: String): String {
        val packed = ByteBuffer.wrap(Base64.decode(value, Base64.NO_WRAP))
        val iv = ByteArray(packed.int).also { packed.get(it) }
        val ciphertext = ByteArray(packed.remaining()).also { packed.get(it) }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(128, iv))
        return String(cipher.doFinal(ciphertext), Charsets.UTF_8)
    }

    @Command
    fun get(invoke: Invoke) {
        val args = invoke.parseArgs(KeyArgs::class.java)
        val result = JSObject()
        val encrypted = preferences.getString(args.key, null)
        if (encrypted == null) {
            result.put("value", null)
        } else {
            try {
                result.put("value", decrypt(encrypted))
            } catch (_: Exception) {
                // A restored preference cannot be decrypted by a newly-created
                // hardware key. Remove it and let the app re-authenticate/pair.
                preferences.edit().remove(args.key).apply()
                result.put("value", null)
            }
        }
        invoke.resolve(result)
    }

    @Command
    fun set(invoke: Invoke) {
        val args = invoke.parseArgs(SetArgs::class.java)
        try {
            if (preferences.edit().putString(args.key, encrypt(args.value)).commit()) {
                invoke.resolve()
            } else {
                invoke.reject("failed to persist secret")
            }
        } catch (error: Exception) {
            invoke.reject(error.message ?: "failed to store secret")
        }
    }

    @Command
    fun delete(invoke: Invoke) {
        val args = invoke.parseArgs(KeyArgs::class.java)
        if (preferences.edit().remove(args.key).commit()) invoke.resolve()
        else invoke.reject("failed to delete secret")
    }

    companion object {
        private const val KEY_ALIAS = "net.j-code.mobile.secure-storage"
    }
}
