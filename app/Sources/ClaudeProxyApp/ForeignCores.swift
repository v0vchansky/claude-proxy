import Foundation

/// Чужие процессы ядра (claude-proxy-core) текущего пользователя, найденные при
/// старте приложения. Инцидент: тестовое ядро, вручную запущенное на своём -sock/
/// -proxy, часами висело подключённым тем же ключом, что и ядро приложения, — сервер
/// перекидывал WG-peer между двумя endpoint'ами, прокси «висел». Старый pkill по
/// нашему сокету такое ядро не видит.
///
/// Политика: о каждом чужом ядре (не vpnd, не наш дочерний процесс) — предупреждение
/// в UI. Завершаем (SIGTERM → SIGKILL) только ядро из нашего бандла или из core/bin
/// репозитория, и только если оно мешает: подключено (status через его -sock) или
/// занимает наш порт/сокет. Остальные — только предупреждение; реальную защиту от
/// двойного подключения даёт лок ключа в самом ядре (keylock).
struct CoreProcInfo: Equatable {
    let pid: Int32
    let ppid: Int32
    let uid: UInt32
    /// Полная командная строка из ps.
    let command: String
    /// Путь к бинарю (может содержать пробелы: «…/Claude Proxy.app/…»).
    let executable: String
    /// Хвост командной строки после бинаря.
    let args: String

    var flags: [String: String] { ForeignCores.parseFlags(args) }
    var mode: String { flags["mode"] ?? "proxy" }
    var sock: String? { flags["sock"] }
    /// Адрес локального прокси; без -proxy ядро слушает 127.0.0.1:8118.
    var proxy: String { flags["proxy"] ?? "127.0.0.1:8118" }
}

enum ForeignCores {
    static let binaryName = "claude-proxy-core"

    // MARK: - Чистые функции (покрыты тестами)

    /// Разбирает вывод `ps -axww -o pid=,ppid=,uid=,command=` и оставляет только
    /// процессы, у которых исполняемый файл — claude-proxy-core.
    static func parsePS(_ output: String) -> [CoreProcInfo] {
        var res: [CoreProcInfo] = []
        for raw in output.split(separator: "\n", omittingEmptySubsequences: true) {
            let parts = raw.split(separator: " ", maxSplits: 3, omittingEmptySubsequences: true)
            guard parts.count == 4,
                  let pid = Int32(parts[0]), let ppid = Int32(parts[1]), let uid = UInt32(parts[2])
            else { continue }
            let command = String(parts[3]).trimmingCharacters(in: .whitespaces)
            guard let (exe, args) = splitExecutable(command) else { continue }
            res.append(CoreProcInfo(pid: pid, ppid: ppid, uid: uid, command: command,
                                    executable: exe, args: args))
        }
        return res
    }

    /// Отделяет путь бинаря claude-proxy-core от аргументов. nil — если командная
    /// строка не запуск самого ядра (pkill/grep/sh -c с упоминанием имени и т.п.):
    /// имя должно стоять в начале строки или после «/», заканчиваться пробелом или
    /// концом строки, а префикс-путь — не содержать « -» (признак чужих аргументов).
    static func splitExecutable(_ command: String) -> (String, String)? {
        var searchFrom = command.startIndex
        while let r = command.range(of: binaryName, range: searchFrom..<command.endIndex) {
            searchFrom = r.upperBound
            let startOK = r.lowerBound == command.startIndex || command[command.index(before: r.lowerBound)] == "/"
            let endOK = r.upperBound == command.endIndex || command[r.upperBound] == " "
            guard startOK, endOK else { continue }
            let exe = String(command[..<r.upperBound])
            if exe.contains(" -") { return nil }
            if !(exe == binaryName || exe.hasPrefix("/") || exe.hasPrefix(".")) { return nil }
            let args = String(command[r.upperBound...]).trimmingCharacters(in: .whitespaces)
            return (exe, args)
        }
        return nil
    }

    /// Разбирает флаги ядра (`-name value`, `--name value`, `-name=value`; булевы —
    /// без значения). Значение — все токены до следующего флага: пути с пробелами
    /// («Application Support») склеиваются обратно.
    static func parseFlags(_ args: String) -> [String: String] {
        var res: [String: String] = [:]
        var current: String?
        var value: [String] = []
        func flush() {
            if let c = current { res[c] = value.joined(separator: " ") }
            current = nil
            value = []
        }
        for tok in args.split(separator: " ", omittingEmptySubsequences: true).map(String.init) {
            if isFlagToken(tok) {
                flush()
                let name = String(tok.drop(while: { $0 == "-" }))
                if let eq = name.firstIndex(of: "=") {
                    res[String(name[..<eq])] = String(name[name.index(after: eq)...])
                    continue
                }
                current = name
            } else if current != nil {
                value.append(tok)
            }
        }
        flush()
        return res
    }

    private static func isFlagToken(_ tok: String) -> Bool {
        let body = tok.drop(while: { $0 == "-" })
        let dashes = tok.count - body.count
        guard dashes == 1 || dashes == 2, let first = body.first else { return false }
        return first.isLetter
    }

    /// Чужие ядра: процессы того же пользователя, не vpnd, не мы и не наши дочерние.
    static func foreign(_ procs: [CoreProcInfo], uid: UInt32, selfPid: Int32,
                        excludePids: Set<Int32>) -> [CoreProcInfo] {
        procs.filter { p in
            p.uid == uid && p.mode != "vpnd" && p.pid != selfPid && p.ppid != selfPid
                && !excludePids.contains(p.pid)
        }
    }

    /// Бинарь из нашего бандла или из core/bin репозитория (dev/тестовые сборки).
    static func isOurBinary(_ exe: String, bundlePath: String, ourCore: String?) -> Bool {
        if let c = ourCore, exe == c { return true }
        if !bundlePath.isEmpty, exe.hasPrefix(bundlePath.hasSuffix("/") ? bundlePath : bundlePath + "/") {
            return true
        }
        return exe.hasSuffix("/core/bin/\(binaryName)")
    }

    /// Порт из адреса host:port (`127.0.0.1:8118`, `:8118`, `[::1]:8118`).
    static func port(_ addr: String) -> String? {
        guard let i = addr.lastIndex(of: ":") else { return nil }
        let p = String(addr[addr.index(after: i)...])
        return p.isEmpty ? nil : p
    }

    /// Мешает ли процесс приложению: занимает наш порт прокси или наш сокет.
    static func occupiesOurs(_ p: CoreProcInfo, ourProxy: String, ourSock: String) -> Bool {
        if let s = p.sock, s == ourSock { return true }
        if let a = port(p.proxy), let b = port(ourProxy), a == b { return true }
        return false
    }

    /// Решение «завершать или только предупредить».
    static func shouldKill(_ p: CoreProcInfo, connected: Bool, ourProxy: String, ourSock: String,
                           bundlePath: String, ourCore: String?) -> Bool {
        guard isOurBinary(p.executable, bundlePath: bundlePath, ourCore: ourCore) else { return false }
        return connected || occupiesOurs(p, ourProxy: ourProxy, ourSock: ourSock)
    }

    static func warningText(_ p: CoreProcInfo, killed: Bool) -> String {
        let action = killed
            ? "завершено (подключено или занимает порт/сокет приложения)"
            : "не тронуто — завершите вручную, если не нужно"
        return "Найдено постороннее ядро claude-proxy-core (pid \(p.pid): \(p.command)) — \(action)"
    }

    // MARK: - Побочные эффекты

    /// Находит чужие ядра, завершает мешающие и возвращает тексты предупреждений.
    /// Блокирующий (ps + короткие status-запросы); вызывать до запуска своего ядра.
    static func scanAndHandle(ourSock: String, ourProxy: String, ourCore: String?,
                              excludePids: Set<Int32> = []) -> [String] {
        guard let out = runPS() else { return [] }
        let found = foreign(parsePS(out), uid: getuid(), selfPid: getpid(), excludePids: excludePids)
        let bundlePath = Bundle.main.bundlePath
        var warnings: [String] = []
        for p in found {
            let ours = isOurBinary(p.executable, bundlePath: bundlePath, ourCore: ourCore)
            // status нужен только нашему бинарю, который не решён по порту/сокету.
            let connected = ours && !occupiesOurs(p, ourProxy: ourProxy, ourSock: ourSock)
                && isConnected(sock: p.sock)
            let kill = shouldKill(p, connected: connected, ourProxy: ourProxy, ourSock: ourSock,
                                  bundlePath: bundlePath, ourCore: ourCore)
            if kill { terminate(p.pid) }
            // Осиротевшее ядро на нашем сокете — штатный случай (его же гасит pkill
            // в CoreProcess, ps мог застать его до выхода): без предупреждения.
            if kill && p.sock == ourSock { continue }
            warnings.append(warningText(p, killed: kill))
        }
        return warnings
    }

    private static func runPS() -> String? {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/ps")
        p.arguments = ["-axww", "-o", "pid=,ppid=,uid=,command="]
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return nil }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return String(data: data, encoding: .utf8)
    }

    /// Держит ли ядро туннель: status через его control-сокет (таймаут 1 с).
    private static func isConnected(sock: String?) -> Bool {
        guard let sock, !sock.isEmpty else { return false }
        guard let st = try? ControlClient(socketPath: sock).send("status", as: CoreState.self, recvTimeout: 1)
        else { return false }
        return st.state == .connected || st.state == .connecting || st.state == .switching
    }

    /// SIGTERM, ожидание до 2 с, затем SIGKILL.
    private static func terminate(_ pid: Int32) {
        guard kill(pid, SIGTERM) == 0 else { return }
        let deadline = Date().addingTimeInterval(2)
        while Date() < deadline {
            if kill(pid, 0) != 0 { return }
            usleep(100_000)
        }
        _ = kill(pid, SIGKILL)
    }
}
