import Foundation

/// Запускает и держит дочерний процесс ядра (claude-proxy-core).
final class CoreProcess {
    private var process: Process?
    private let socketPath: String
    private let proxyAddr: String

    init(socketPath: String, proxyAddr: String) {
        self.socketPath = socketPath
        self.proxyAddr = proxyAddr
    }

    var isRunning: Bool { process?.isRunning ?? false }

    /// Запускает ядро, если оно ещё не запущено. Возвращает false, если бинарь не найден.
    @discardableResult
    func start() -> Bool {
        if isRunning { return true }
        guard let bin = AppPaths.coreBinary() else { return false }

        // Снять возможный stale-сокет.
        try? FileManager.default.removeItem(atPath: socketPath)

        let p = Process()
        p.executableURL = URL(fileURLWithPath: bin)
        p.arguments = ["-sock", socketPath, "-proxy", proxyAddr]
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
}
