import Foundation

/// Состояние full-tunnel VPN — зеркало State, который отдаёт root-демон (vpnd).
/// Демон переиспользует тот же ConnState, что и прокси-ядро, но набор значений в
/// UI сведён к четырём: все переходные фазы (switching/preparing/tearing-down/…)
/// показываем как `connecting`, штатный простой демона — как `off`.
enum VpnState: String, Codable {
    case off, connecting, connected, error

    /// Терпимое декодирование: демон может прислать как сведённые значения
    /// (off/connecting/connected/error), так и «сырые» фазы ядра
    /// (disconnected/switching/preparing/…). Любое неизвестное → off.
    init(from decoder: Decoder) throws {
        let raw = (try? decoder.singleValueContainer().decode(String.self)) ?? ""
        switch raw {
        case "connected":
            self = .connected
        case "connecting", "switching", "preparing", "routed",
             "dns-set", "pf-set", "tearing-down":
            self = .connecting
        case "error":
            self = .error
        default: // off, disconnected, clean, пусто и всё неизвестное
            self = .off
        }
    }

    var title: String {
        switch self {
        case .off:        return "Выкл"
        case .connecting: return "Подключение…"
        case .connected:  return "Подключено"
        case .error:      return "Ошибка"
        }
    }
}

/// Снимок состояния full-tunnel VPN из команды `status-full`.
/// Поля сверены с docs/full-vpn-design.md §5 (JSON, который отдаёт демон):
/// State + `utun`, `killSwitch`, `dnsOverridden`, `doubleVpnWarning`, `lastError`.
/// Все поля с дефолтами — недостающие ключи декодятся в пустое/false.
struct VpnStatus: Decodable, Equatable {
    var state: VpnState = .off
    var utun: String = ""
    var serverHost: String = ""
    var killSwitch: Bool = false
    var dnsOverridden: Bool = false
    var doubleVpnWarning: Bool = false
    var lastError: String = ""

    // Явные CodingKeys: демон шлёт camelCase (как прокси-ядро), фиксируем контракт.
    enum CodingKeys: String, CodingKey {
        case state, utun, serverHost, killSwitch, dnsOverridden, doubleVpnWarning, lastError
    }

    init() {}

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = (try? c.decode(VpnState.self, forKey: .state)) ?? .off
        utun = (try? c.decode(String.self, forKey: .utun)) ?? ""
        serverHost = (try? c.decode(String.self, forKey: .serverHost)) ?? ""
        killSwitch = (try? c.decode(Bool.self, forKey: .killSwitch)) ?? false
        dnsOverridden = (try? c.decode(Bool.self, forKey: .dnsOverridden)) ?? false
        doubleVpnWarning = (try? c.decode(Bool.self, forKey: .doubleVpnWarning)) ?? false
        lastError = (try? c.decode(String.self, forKey: .lastError)) ?? ""
    }
}

/// Клиент сокета root-демона vpnd: тот же протокол JSON Lines, что у control.sock,
/// но сокет другой — `/var/run/claude-proxy-vpnd.sock` (owner root, chmod 0600 на
/// установившего пользователя). Команды: ping / status-full / connect-full /
/// disconnect-full (docs/full-vpn-design.md §5). Все вызовы блокирующие — звать с фона.
final class VpnClient: @unchecked Sendable {
    let socketPath: String
    private var nextID = 0
    private let idLock = NSLock()

    init(socketPath: String = "/var/run/claude-proxy-vpnd.sock") {
        self.socketPath = socketPath
    }

    enum VpnError: Error, LocalizedError {
        /// Сокет недоступен: демон не установлен или не запущен.
        case notInstalled
        case connectFailed(String)
        case ioFailed(String)
        case decodeFailed(String)
        case core(String)

        var errorDescription: String? {
            switch self {
            case .notInstalled:          return "VPN-хелпер не установлен"
            case .connectFailed(let s):  return "Демон vpnd не отвечает: \(s)"
            case .ioFailed(let s):       return "Ошибка обмена с vpnd: \(s)"
            case .decodeFailed(let s):   return "Некорректный ответ vpnd: \(s)"
            case .core(let s):           return s
            }
        }
    }

    private func allocID() -> Int {
        idLock.lock(); defer { idLock.unlock() }
        nextID += 1
        return nextID
    }

    // MARK: - Публичные команды

    /// Пинг демона. Возвращает (mode, version) или nil, если сокет недоступен.
    /// Используется для детекта «демон жив» и version-handshake после обновления app.
    func ping() -> (mode: String, version: String)? {
        struct Pong: Decodable { var pong: Bool; var version: String?; var mode: String? }
        guard let p = try? send("ping", as: Pong.self, recvTimeout: 3), p.pong else { return nil }
        return (p.mode ?? "vpnd", p.version ?? "")
    }

    func statusFull() throws -> VpnStatus {
        try send("status-full", as: VpnStatus.self, recvTimeout: 5)
    }

    /// Поднять full-tunnel. Долгая операция (utun + handshake + маршруты + PF) — 30 с.
    func connectFull(profile: ServerProfile, privateKey: String) throws -> VpnStatus {
        try send("connect-full", profile: profile, privateKey: privateKey,
                 as: VpnStatus.self, recvTimeout: 30)
    }

    func disconnectFull() throws -> VpnStatus {
        try send("disconnect-full", as: VpnStatus.self, recvTimeout: 15)
    }

    // MARK: - Транспорт

    /// Отправляет запрос, распаковывает `result`. Бросает `.core` при `ok:false`,
    /// `.notInstalled` — если сокета нет/некому слушать.
    private func send<T: Decodable>(_ cmd: String,
                                    profile: ServerProfile? = nil,
                                    privateKey: String? = nil,
                                    as type: T.Type,
                                    recvTimeout: TimeInterval) throws -> T {
        // Профиль и privateKey кодируются ровно как в connect прокси (ControlRequest).
        let req = ControlRequest(id: allocID(), cmd: cmd, profile: profile, privateKey: privateKey,
                                 ssh: nil, provision: nil, clientPublicKey: nil)
        let data = try JSONEncoder().encode(req)
        let line = try exchange(payload: data, recvTimeout: recvTimeout)
        let resp: ControlResponse<T>
        do {
            resp = try JSONDecoder().decode(ControlResponse<T>.self, from: line)
        } catch {
            throw VpnError.decodeFailed("\(error)")
        }
        if !resp.ok { throw VpnError.core(resp.error ?? "неизвестная ошибка vpnd") }
        guard let result = resp.result else { throw VpnError.decodeFailed("нет result") }
        return result
    }

    /// Нижний уровень: одно соединение, запись строки, чтение до \n.
    private func exchange(payload data: Data, recvTimeout: TimeInterval) throws -> Data {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        if fd < 0 { throw VpnError.connectFailed("socket() errno \(errno)") }
        defer { close(fd) }

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let pathBytes = socketPath.utf8CString
        guard pathBytes.count <= MemoryLayout.size(ofValue: addr.sun_path) else {
            throw VpnError.connectFailed("путь к сокету слишком длинный")
        }
        withUnsafeMutablePointer(to: &addr.sun_path) { ptr in
            ptr.withMemoryRebound(to: CChar.self, capacity: pathBytes.count) { dst in
                pathBytes.withUnsafeBufferPointer { src in
                    dst.update(from: src.baseAddress!, count: pathBytes.count)
                }
            }
        }

        let connRes = withUnsafePointer(to: &addr) { p -> Int32 in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { sp in
                Foundation.connect(fd, sp, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        if connRes != 0 {
            // Сокета нет (ENOENT) или некому слушать (ECONNREFUSED) → демон не
            // установлен/не запущен. Остальное — обычная ошибка подключения.
            if errno == ENOENT || errno == ECONNREFUSED {
                throw VpnError.notInstalled
            }
            throw VpnError.connectFailed("connect() errno \(errno)")
        }

        var tv = timeval(tv_sec: Int(recvTimeout), tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        var payload = data
        payload.append(0x0A)
        try payload.withUnsafeBytes { raw in
            var off = 0
            let base = raw.bindMemory(to: UInt8.self).baseAddress!
            while off < payload.count {
                let n = write(fd, base + off, payload.count - off)
                if n <= 0 { throw VpnError.ioFailed("write errno \(errno)") }
                off += n
            }
        }

        var line = Data()
        var buf = [UInt8](repeating: 0, count: 4096)
        readLoop: while true {
            let n = read(fd, &buf, buf.count)
            if n < 0 { throw VpnError.ioFailed("read errno \(errno)") }
            if n == 0 { break }
            for i in 0..<n {
                if buf[i] == 0x0A { break readLoop }
                line.append(buf[i])
            }
        }
        if line.isEmpty { throw VpnError.ioFailed("пустой ответ") }
        return line
    }
}
