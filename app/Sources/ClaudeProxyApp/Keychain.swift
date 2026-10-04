import Foundation
import CryptoKit
import Security

/// Хранилище клиентской пары ключей AmneziaWG.
/// Приватный ключ лежит в Keychain и не покидает устройство; наружу — только публичный.
enum ClientKey {
    private static let service = "com.claudeproxy.client"
    private static let account = "wireguard-private-key"

    /// Возвращает приватный ключ (base64), создавая новую пару при первом запуске.
    static func loadOrCreatePrivateKeyBase64() throws -> String {
        if let existing = try readPrivateKeyBase64() {
            return existing
        }
        // X25519/Curve25519 = формат ключей WireGuard.
        let key = Curve25519.KeyAgreement.PrivateKey()
        let b64 = key.rawRepresentation.base64EncodedString()
        try store(privateKeyBase64: b64)
        return b64
    }

    /// Публичный ключ (base64) из хранимого приватного.
    static func publicKeyBase64() throws -> String {
        guard let privB64 = try readPrivateKeyBase64(),
              let raw = Data(base64Encoded: privB64) else {
            throw KeychainError.notFound
        }
        let key = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw)
        return key.publicKey.rawRepresentation.base64EncodedString()
    }

    enum KeychainError: Error { case notFound; case osStatus(OSStatus) }

    private static func readPrivateKeyBase64() throws -> String? {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        let st = SecItemCopyMatching(q as CFDictionary, &item)
        if st == errSecItemNotFound { return nil }
        if st != errSecSuccess { throw KeychainError.osStatus(st) }
        guard let data = item as? Data, let s = String(data: data, encoding: .utf8) else {
            return nil
        }
        return s
    }

    private static func store(privateKeyBase64: String) throws {
        let data = Data(privateKeyBase64.utf8)
        // Удаляем возможный старый item, затем добавляем.
        let base: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(base as CFDictionary)
        var add = base
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        let st = SecItemAdd(add as CFDictionary, nil)
        if st != errSecSuccess { throw KeychainError.osStatus(st) }
    }
}
