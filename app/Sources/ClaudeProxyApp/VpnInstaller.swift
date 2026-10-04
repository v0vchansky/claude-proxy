import Foundation

/// Установка/снятие root-демона vpnd (LaunchDaemon) при ad-hoc подписи.
/// SMAppService недоступен (ad-hoc, docs/full-vpn-design.md §6), поэтому ставим
/// скриптом через `osascript … with administrator privileges` — GUI-запрос пароля.
/// Скрипты лежат в бандле Contents/Resources (кладёт build-app.sh).
final class VpnInstaller: @unchecked Sendable {
    static let plistPath = "/Library/LaunchDaemons/com.claudeproxy.vpnd.plist"
    static let socketPath = "/var/run/claude-proxy-vpnd.sock"

    enum InstallError: Error, LocalizedError {
        case scriptMissing(String)
        case coreMissing
        case cancelled
        case failed(String)

        var errorDescription: String? {
            switch self {
            case .scriptMissing(let s): return "В бандле нет скрипта установщика (\(s))"
            case .coreMissing:          return "Не найден бинарь ядра для установки демона"
            case .cancelled:            return "Установка отменена (пароль не введён)"
            case .failed(let s):        return "Установка не удалась: \(s)"
            }
        }
    }

    /// Демон считается установленным, если есть plist LaunchDaemon ИЛИ сокет на месте.
    /// (plist переживает ребут; сокет появляется, когда демон запущен.)
    func isInstalled() -> Bool {
        let fm = FileManager.default
        return fm.fileExists(atPath: Self.plistPath) || fm.fileExists(atPath: Self.socketPath)
    }

    /// Ставит демон: копирует ядро в /Library/PrivilegedHelperTools, пишет plist,
    /// грузит через launchctl. Один запрос пароля администратора. Блокирующий —
    /// звать с фонового потока. Бросает `.cancelled` при отмене пароля.
    func install() throws {
        guard let core = AppPaths.coreBinary() else { throw InstallError.coreMissing }
        let script = try scriptPath("install-vpnd")
        // Передаём путь к ядру-источнику и uid пользователя (для peercred-забора §5).
        let shell = "/bin/sh \(Self.q(script)) \(Self.q(core)) \(getuid())"
        try runPrivileged(shell,
                          prompt: "Claude Proxy установит системный VPN-компонент (нужен пароль администратора).")
    }

    /// Снимает демон: bootout + удаление файлов. Один запрос пароля.
    func uninstall() throws {
        let script = try scriptPath("uninstall-vpnd")
        let shell = "/bin/sh \(Self.q(script))"
        try runPrivileged(shell,
                          prompt: "Claude Proxy удалит системный VPN-компонент (нужен пароль администратора).")
    }

    // MARK: - Внутреннее

    private func scriptPath(_ name: String) throws -> String {
        guard let url = Bundle.main.url(forResource: name, withExtension: "sh") else {
            throw InstallError.scriptMissing("\(name).sh")
        }
        return url.path
    }

    /// Выполняет shell-команду под root через osascript. Отмену пароля (-128)
    /// и прочие ошибки AppleScript разбирает по stderr osascript.
    private func runPrivileged(_ shell: String, prompt: String) throws {
        let source = "do shell script \(Self.appleQuote(shell)) " +
                     "with administrator privileges with prompt \(Self.appleQuote(prompt))"

        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        proc.arguments = ["-e", source]
        let errPipe = Pipe()
        proc.standardOutput = FileHandle.nullDevice
        proc.standardError = errPipe

        do {
            try proc.run()
        } catch {
            throw InstallError.failed(error.localizedDescription)
        }
        let errData = errPipe.fileHandleForReading.readDataToEndOfFile()
        proc.waitUntilExit()

        if proc.terminationStatus == 0 { return }

        let stderr = String(data: errData, encoding: .utf8) ?? ""
        // Отмена пароля: osascript пишет "User canceled. (-128)".
        if stderr.contains("-128") || stderr.localizedCaseInsensitiveContains("User canceled") {
            throw InstallError.cancelled
        }
        let msg = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
        throw InstallError.failed(msg.isEmpty ? "код \(proc.terminationStatus)" : msg)
    }

    /// Квотирование пути в одинарные кавычки для /bin/sh (на случай пробелов в пути бандла).
    private static func q(_ s: String) -> String {
        "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    /// Квотирование строки для литерала AppleScript (двойные кавычки).
    private static func appleQuote(_ s: String) -> String {
        let esc = s.replacingOccurrences(of: "\\", with: "\\\\")
                   .replacingOccurrences(of: "\"", with: "\\\"")
        return "\"" + esc + "\""
    }
}
