import Foundation

/// Состояние подключения — зеркало ConnState в ядре (docs/control-protocol.md).
enum ConnState: String, Codable {
    case disconnected, connecting, connected, switching, error

    var title: String {
        switch self {
        case .disconnected: return "Disconnected"
        case .connecting:   return "Connecting"
        case .connected:    return "Connected"
        case .switching:    return "Switching…"
        case .error:        return "Error"
        }
    }
}

/// Снимок состояния из ядра.
struct CoreState: Codable, Equatable {
    var state: ConnState = .disconnected
    var profileId: String = ""
    var serverName: String = ""
    var serverHost: String = ""
    var serverPort: Int = 0
    var localProxy: String = "127.0.0.1:8118"
    var pingMs: Int = -1
    var lastCheckUnix: Int64 = 0
    var connectedSinceUnix: Int64 = 0
    var lastHandshakeUnix: Int64 = 0
    var rxBytes: Int64 = 0
    var txBytes: Int64 = 0
    var lastError: String = ""
}

/// Server profile. Кодируется ровно теми ключами, которые ждёт ядро.
struct ServerProfile: Codable, Identifiable, Equatable {
    var id: String
    var displayName: String
    var country: String
    var provider: String
    var host: String
    var port: Int
    var serverPublicKey: String
    var clientVpnAddress: String
    var serverVpnAddress: String
    var dns: [String]
    var mtu: Int
    var persistentKeepalive: Int

    var jc: Int
    var jmin: Int
    var jmax: Int
    var s1: Int
    var s2: Int
    var s3: Int
    var s4: Int
    var h1: UInt32
    var h2: UInt32
    var h3: UInt32
    var h4: UInt32

    /// Профиль HOSTKEY из ТЗ (без приватного ключа — он в Keychain).
    static let defaultHostkey = ServerProfile(
        id: "nl-hostkey",
        displayName: "Netherlands / HOSTKEY",
        country: "Netherlands",
        provider: "HOSTKEY",
        host: "222.167.208.108",
        port: 51820,
        serverPublicKey: "WyhFpvvdzC2OBhpbcRAMzVZXsyWgToZ/4vtVkgBo4F4=",
        clientVpnAddress: "10.77.0.2",
        serverVpnAddress: "10.77.0.1",
        dns: ["1.1.1.1", "8.8.8.8"],
        mtu: 1420,
        persistentKeepalive: 25,
        jc: 5, jmin: 50, jmax: 1000,
        s1: 64, s2: 128, s3: 0, s4: 0,
        h1: 1000001, h2: 1000002, h3: 1000003, h4: 1000004
    )
}

/// Запрос к ядру.
struct ControlRequest: Encodable {
    var id: Int
    var cmd: String
    var profile: ServerProfile?
    var privateKey: String?
}

/// Ответ ядра.
struct ControlResponse<T: Decodable>: Decodable {
    var id: Int
    var ok: Bool
    var result: T?
    var error: String?
}

struct LogsResult: Decodable {
    var lines: [String]
}
