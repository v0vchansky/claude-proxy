import Foundation

/// Состояние подключения — зеркало ConnState в ядре (docs/control-protocol.md).
enum ConnState: String, Codable {
    case disconnected, connecting, connected, switching, error

    var title: String {
        switch self {
        case .disconnected: return "Отключено"
        case .connecting:   return "Подключение…"
        case .connected:    return "Подключено"
        case .switching:    return "Переключение…"
        case .error:        return "Ошибка"
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
    // Режим форвардинга локального прокси 8118: tunnel (через свой WG-туннель),
    // direct (напрямую — трафик заворачивает системный utun Полного VPN), off.
    // Контракт варианта А: status теперь всегда содержит это поле.
    var forwardMode: String = "off"

    init() {}

    enum CodingKeys: String, CodingKey {
        case state, profileId, serverName, serverHost, serverPort, localProxy,
             pingMs, lastCheckUnix, connectedSinceUnix, lastHandshakeUnix,
             rxBytes, txBytes, lastError, forwardMode
    }

    // Терпимое декодирование (как у VpnStatus): недостающий/битый ключ → дефолт,
    // чтобы один новый/старый ключ не ронял весь разбор статуса в фоне.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = (try? c.decode(ConnState.self, forKey: .state)) ?? .disconnected
        profileId = (try? c.decode(String.self, forKey: .profileId)) ?? ""
        serverName = (try? c.decode(String.self, forKey: .serverName)) ?? ""
        serverHost = (try? c.decode(String.self, forKey: .serverHost)) ?? ""
        serverPort = (try? c.decode(Int.self, forKey: .serverPort)) ?? 0
        localProxy = (try? c.decode(String.self, forKey: .localProxy)) ?? "127.0.0.1:8118"
        pingMs = (try? c.decode(Int.self, forKey: .pingMs)) ?? -1
        lastCheckUnix = (try? c.decode(Int64.self, forKey: .lastCheckUnix)) ?? 0
        connectedSinceUnix = (try? c.decode(Int64.self, forKey: .connectedSinceUnix)) ?? 0
        lastHandshakeUnix = (try? c.decode(Int64.self, forKey: .lastHandshakeUnix)) ?? 0
        rxBytes = (try? c.decode(Int64.self, forKey: .rxBytes)) ?? 0
        txBytes = (try? c.decode(Int64.self, forKey: .txBytes)) ?? 0
        lastError = (try? c.decode(String.self, forKey: .lastError)) ?? ""
        forwardMode = (try? c.decode(String.self, forKey: .forwardMode)) ?? "off"
    }
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

/// Параметры SSH-доступа для автоматического развёртывания сервера.
/// `password` и `privateKeyPath` опциональны, но хотя бы одно должно быть задано.
/// Сериализуется ровно теми ключами, что ждёт ядро; nil-поля JSONEncoder опускает.
struct SSHConfig: Codable {
    var host: String
    var port: Int = 22
    var user: String = "root"
    var password: String?
    var privateKeyPath: String?
    var passphrase: String?
}

/// Параметры развёртывания AmneziaWG. Все поля с дефолтами из контракта провижининга.
struct ProvisionParams: Codable {
    var awgPort: Int = 51820
    var serverVpnAddress: String = "10.77.0.1"
    var clientVpnAddress: String = "10.77.0.2"
    var jc: Int = 5
    var jmin: Int = 50
    var jmax: Int = 1000
    var s1: Int = 64
    var s2: Int = 128
    var h1: UInt32 = 1000001
    var h2: UInt32 = 1000002
    var h3: UInt32 = 1000003
    var h4: UInt32 = 1000004
}

/// Результат развёртывания (provision.Result в ядре).
/// `log` — пошаговый журнал; при ошибке приходит частичный результат с заполненным `log`.
struct ProvisionResult: Decodable {
    var serverPublicKey: String = ""
    var host: String = ""
    var port: Int = 51820
    var serverVpnAddress: String = "10.77.0.1"
    var clientVpnAddress: String = "10.77.0.2"
    var jc: Int = 0
    var jmin: Int = 0
    var jmax: Int = 0
    var s1: Int = 0
    var s2: Int = 0
    var s3: Int = 0
    var s4: Int = 0
    var h1: UInt32 = 0
    var h2: UInt32 = 0
    var h3: UInt32 = 0
    var h4: UInt32 = 0
    var adopted: Bool = false
    var log: [String] = []
}

/// Запрос к ядру.
struct ControlRequest: Encodable {
    var id: Int
    var cmd: String
    var profile: ServerProfile?
    var privateKey: String?
    // Поле команды `forward`: tunnel|direct|off; при nil JSONEncoder его опускает.
    var mode: String?
    // Поля для команды `provision`; при nil JSONEncoder их опускает.
    var ssh: SSHConfig?
    var provision: ProvisionParams?
    var clientPublicKey: String?
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
