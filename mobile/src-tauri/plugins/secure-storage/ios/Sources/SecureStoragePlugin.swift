import Security
import Tauri

struct KeyArgs: Decodable { let key: String }
struct SetArgs: Decodable { let key: String; let value: String }

class SecureStoragePlugin: Plugin {
  private let service = "net.j-code.mobile.secure-storage"

  private func query(_ key: String) -> [String: Any] {
    [kSecClass as String: kSecClassGenericPassword,
     kSecAttrService as String: service,
     kSecAttrAccount as String: key]
  }

  @objc public func get(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(KeyArgs.self)
    var q = query(args.key)
    q[kSecReturnData as String] = true
    q[kSecMatchLimit as String] = kSecMatchLimitOne
    var item: CFTypeRef?
    let status = SecItemCopyMatching(q as CFDictionary, &item)
    if status == errSecItemNotFound {
      invoke.resolve(["value": NSNull()])
      return
    }
    guard status == errSecSuccess, let data = item as? Data, let value = String(data: data, encoding: .utf8) else {
      invoke.reject("Keychain read failed (\(status))")
      return
    }
    invoke.resolve(["value": value])
  }

  @objc public func set(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(SetArgs.self)
    let data = Data(args.value.utf8)
    let q = query(args.key)
    let update = [kSecValueData as String: data]
    var status = SecItemUpdate(q as CFDictionary, update as CFDictionary)
    if status == errSecItemNotFound {
      var add = q
      add[kSecValueData as String] = data
      add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
      status = SecItemAdd(add as CFDictionary, nil)
    }
    if status == errSecSuccess { invoke.resolve() }
    else { invoke.reject("Keychain write failed (\(status))") }
  }

  @objc public func delete(_ invoke: Invoke) throws {
    let args = try invoke.parseArgs(KeyArgs.self)
    let status = SecItemDelete(query(args.key) as CFDictionary)
    if status == errSecSuccess || status == errSecItemNotFound { invoke.resolve() }
    else { invoke.reject("Keychain delete failed (\(status))") }
  }
}

@_cdecl("init_plugin_jcode_secure_storage")
func initPlugin() -> Plugin { SecureStoragePlugin() }

