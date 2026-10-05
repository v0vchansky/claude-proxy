import Foundation
import CryptoKit
import Security

/// Хранилище клиентских пар ключей AmneziaWG.
/// Приватные ключи лежат в Keychain и не покидают устройство; наружу — только публичные.
///
/// Два независимых ключа (бесшовное переключение Прокси↔Полный VPN):
/// - Прокси (адрес 10.77.0.2) — account "wireguard-private-key" (как было, не пересоздаём).
/// - Полный VPN (адрес 10.77.0.3) — account "wireguard-private-key-vpn" (новый).
/// Разные ключи = разные peer'ы на сервере: туннели не конфликтуют, новый поднимается,
/// пока старый ещё работает, без разрыва для новых запросов.
enum ClientKey {
    private static let service = "com.claudeproxy.client"
    /// Прокси-ключ (адрес .2) — существующий, сохраняем обратную совместимость.
    private static let proxyAccount = "wireguard-private-key"
    /// Ключ Полного VPN (адрес .3) — новый, создаётся при первом обращении.
    private static let vpnAccount = "wireguard-private-key-vpn"

    // MARK: - Прокси-ключ (как было)

    /// Возвращает приватный ключ прокси (base64), создавая новую пару при первом запуске.
    static func loadOrCreatePrivateKeyBase64() throws -> String {
        try loadOrCreatePrivateKeyBase64(account: proxyAccount)
    }

    /// Публичный ключ прокси (base64) из хранимого приватного.
    static func publicKeyBase64() throws -> String {
        try publicKeyBase64(account: proxyAccount)
    }

    // MARK: - Ключ Полного VPN (новый)

    /// Возвращает приватный ключ Полного VPN (base64), создавая новую пару при первом запуске.
    static func loadOrCreatePrivateKeyFullBase64() throws -> String {
        try loadOrCreatePrivateKeyBase64(account: vpnAccount)
    }

    /// Публичный ключ Полного VPN (base64) из хранимого приватного.
    static func publicKeyFullBase64() throws -> String {
        try publicKeyBase64(account: vpnAccount)
    }

    enum KeychainError: Error { case notFound; case osStatus(OSStatus) }

    // MARK: - Обобщённая реализация по account

    private static func loadOrCreatePrivateKeyBase64(account: String) throws -> String {
        if let existing = try readPrivateKeyBase64(account: account) {
            return existing
        }
        // X25519/Curve25519 = формат ключей WireGuard.
        let key = Curve25519.KeyAgreement.PrivateKey()
        let b64 = key.rawRepresentation.base64EncodedString()
        try store(privateKeyBase64: b64, account: account)
        return b64
    }

    private static func publicKeyBase64(account: String) throws -> String {
        guard let privB64 = try readPrivateKeyBase64(account: account),
              let raw = Data(base64Encoded: privB64) else {
            throw KeychainError.notFound
        }
        let key = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw)
        return key.publicKey.rawRepresentation.base64EncodedString()
    }

    private static func readPrivateKeyBase64(account: String) throws -> String? {
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

    private static func store(privateKeyBase64: String, account: String) throws {
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
