import Foundation

/// Запускает и держит дочерний процесс ядра (claude-proxy-core).
final class CoreProcess {
    private var process: Process?
    private let socketPath: String
    private let proxyAddr: String
    private let logPath: String

    /// Предупреждения о посторонних ядрах, найденных при последнем start()
    /// (см. ForeignCores). Пусто — чужих ядер нет.
    private(set) var foreignWarnings: [String] = []

    init(socketPath: String, proxyAddr: String, logPath: String) {
        self.socketPath = socketPath
        self.proxyAddr = proxyAddr
        self.logPath = logPath
    }

    var isRunning: Bool { process?.isRunning ?? false }

    /// Запускает ядро, если оно ещё не запущено. Возвращает false, если бинарь не найден.
    @discardableResult
    func start() -> Bool {
        if isRunning { return true }
        guard let bin = AppPaths.coreBinary() else { return false }

        // Убить возможное осиротевшее прокси-ядро, держащее порт 8118 (например после
        // аварийного завершения приложения): иначе новое ядро не сможет поднять listener.
        // Матчим по уникальному пути контрол-сокета — демон vpnd (/var/run/...) не затрагивается.
        killStaleProxyCore()

        // Посторонние ядра на других сокетах/портах (ручной тестовый запуск и т.п.):
        // предупредить, а мешающие из нашего бандла/core/bin — завершить.
        foreignWarnings = ForeignCores.scanAndHandle(ourSock: socketPath, ourProxy: proxyAddr,
                                                     ourCore: bin)

        // Снять возможный stale-сокет.
        try? FileManager.default.removeItem(atPath: socketPath)

        let p = Process()
        p.executableURL = URL(fileURLWithPath: bin)
        // Журнал передаём явно: ядро без -log пишет только в память (ручной запуск
        // не должен мешать боевой diagnostics.log).
        p.arguments = ["-sock", socketPath, "-proxy", proxyAddr, "-log", logPath]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        do {
            try p.run()
            process = p
            return true
        } catch {
            return false
        }
    }

    func stop() {
        guard let p = process, p.isRunning else { return }
        p.terminate()
        process = nil
    }

    /// pkill осиротевшего прокси-ядра по уникальному пути контрол-сокета.
    private func killStaleProxyCore() {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/pkill")
        p.arguments = ["-f", "claude-proxy-core -sock \(socketPath)"]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        try? p.run()
        p.waitUntilExit()
    }
}
