import Foundation

/// Клиент control-сокета ядра: JSON Lines поверх unix domain socket.
/// Каждый запрос — отдельное соединение (ядро обрабатывает любое количество).
/// Все вызовы блокирующие; дёргать с фонового потока.
final class ControlClient: @unchecked Sendable {
    let socketPath: String
    private var nextID = 0
    private let idLock = NSLock()

    init(socketPath: String) {
        self.socketPath = socketPath
    }

    enum ControlError: Error, LocalizedError {
        case connectFailed(String)
        case ioFailed(String)
        case decodeFailed(String)
        case core(String)

        var errorDescription: String? {
            switch self {
            case .connectFailed(let s): return "Core не отвечает: \(s)"
            case .ioFailed(let s):      return "Ошибка обмена с core: \(s)"
            case .decodeFailed(let s):  return "Некорректный ответ core: \(s)"
            case .core(let s):          return s
            }
        }
    }

    private func allocID() -> Int {
        idLock.lock(); defer { idLock.unlock() }
        nextID += 1
        return nextID
    }

    /// Отправляет запрос и декодирует result указанного типа.
    /// recvTimeout — секунды ожидания ответа (connect/switch долгие).
    /// Бросает `.core`, если ядро вернуло `ok:false`.
    func send<T: Decodable>(_ cmd: String,
                            profile: ServerProfile? = nil,
                            privateKey: String? = nil,
                            ssh: SSHConfig? = nil,
                            provision: ProvisionParams? = nil,
                            clientPublicKey: String? = nil,
                            as type: T.Type,
                            recvTimeout: TimeInterval = 30) throws -> T {
        let resp = try exchange(cmd, profile: profile, privateKey: privateKey,
                                ssh: ssh, provision: provision, clientPublicKey: clientPublicKey,
                                as: type, recvTimeout: recvTimeout)
        if !resp.ok {
            throw ControlError.core(resp.error ?? "неизвестная ошибка core")
        }
        guard let result = resp.result else {
            throw ControlError.decodeFailed("нет result")
        }
        return result
    }

    /// Нижний уровень: обмен по сокету, возвращает ответ целиком (включая `result`
    /// при `ok:false` — нужно для provision, где частичный результат несёт лог).
    private func exchange<T: Decodable>(_ cmd: String,
                                        profile: ServerProfile? = nil,
                                        privateKey: String? = nil,
                                        ssh: SSHConfig? = nil,
                                        provision: ProvisionParams? = nil,
                                        clientPublicKey: String? = nil,
                                        as type: T.Type,
                                        recvTimeout: TimeInterval = 30) throws -> ControlResponse<T> {
        let req = ControlRequest(id: allocID(), cmd: cmd, profile: profile, privateKey: privateKey,
                                 ssh: ssh, provision: provision, clientPublicKey: clientPublicKey)
        let data = try JSONEncoder().encode(req)

        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        if fd < 0 { throw ControlError.connectFailed("socket() errno \(errno)") }
        defer { close(fd) }

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let pathBytes = socketPath.utf8CString
        guard pathBytes.count <= MemoryLayout.size(ofValue: addr.sun_path) else {
            throw ControlError.connectFailed("путь к сокету слишком длинный")
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
        if connRes != 0 { throw ControlError.connectFailed("connect() errno \(errno)") }

        // Таймаут на чтение.
        var tv = timeval(tv_sec: Int(recvTimeout), tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        // Запись строки запроса + \n.
        var payload = data
        payload.append(0x0A)
        try payload.withUnsafeBytes { raw in
            var off = 0
            let base = raw.bindMemory(to: UInt8.self).baseAddress!
            while off < payload.count {
                let n = write(fd, base + off, payload.count - off)
                if n <= 0 { throw ControlError.ioFailed("write errno \(errno)") }
                off += n
            }
        }

        // Чтение до первого \n.
        var line = Data()
        var buf = [UInt8](repeating: 0, count: 4096)
        readLoop: while true {
            let n = read(fd, &buf, buf.count)
            if n < 0 { throw ControlError.ioFailed("read errno \(errno)") }
            if n == 0 { break }
            for i in 0..<n {
                if buf[i] == 0x0A { break readLoop }
                line.append(buf[i])
            }
        }
        if line.isEmpty { throw ControlError.ioFailed("пустой ответ") }

        do {
            return try JSONDecoder().decode(ControlResponse<T>.self, from: line)
        } catch {
            throw ControlError.decodeFailed("\(error)")
        }
    }

    /// Ошибка провижининга с частичным логом шагов (для показа пользователю).
    struct ProvisionFailure: Error {
        let message: String
        let log: [String]
    }

    // Удобные обёртки.
    func status() throws -> CoreState { try send("status", as: CoreState.self, recvTimeout: 5) }
    func ping() throws -> Bool {
        struct Pong: Decodable { var pong: Bool }
        return (try send("ping", as: Pong.self, recvTimeout: 3)).pong
    }
    func connect(_ p: ServerProfile, privateKey: String) throws -> CoreState {
        try send("connect", profile: p, privateKey: privateKey, as: CoreState.self, recvTimeout: 25)
    }
    func switchServer(_ p: ServerProfile, privateKey: String) throws -> CoreState {
        try send("switch", profile: p, privateKey: privateKey, as: CoreState.self, recvTimeout: 25)
    }
    func disconnect() throws -> CoreState { try send("disconnect", as: CoreState.self, recvTimeout: 10) }
    func healthcheck() throws -> CoreState { try send("healthcheck", as: CoreState.self, recvTimeout: 12) }
    func logs() throws -> [String] { (try send("logs", as: LogsResult.self, recvTimeout: 5)).lines }

    /// Разворачивает сервер по SSH. Долгая операция (установка пакета) — таймаут 240 с.
    /// При ошибке бросает `ProvisionFailure` с частичным логом шагов.
    func provision(ssh: SSHConfig, params: ProvisionParams, clientPublicKey: String) throws -> ProvisionResult {
        let resp = try exchange("provision", ssh: ssh, provision: params, clientPublicKey: clientPublicKey,
                                as: ProvisionResult.self, recvTimeout: 240)
        if resp.ok, let result = resp.result {
            return result
        }
        throw ProvisionFailure(message: resp.error ?? "неизвестная ошибка core",
                               log: resp.result?.log ?? [])
    }
}
