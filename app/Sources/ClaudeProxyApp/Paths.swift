import Foundation

/// Пути приложения в Application Support.
enum AppPaths {
    static var supportDir: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first!
        let dir = base.appendingPathComponent("ClaudeProxy", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    static var controlSocket: URL { supportDir.appendingPathComponent("control.sock") }
    static var serversJSON: URL { supportDir.appendingPathComponent("servers.json") }

    /// Путь к бинарю ядра: в бандле (Contents/Helpers) или dev-fallback рядом с репозиторием.
    static func coreBinary() -> String? {
        if let helper = Bundle.main.url(forAuxiliaryExecutable: "claude-proxy-core") {
            return helper.path
        }
        // Contents/Helpers/claude-proxy-core
        let helpers = Bundle.main.bundleURL
            .appendingPathComponent("Contents/Helpers/claude-proxy-core")
        if FileManager.default.isExecutableFile(atPath: helpers.path) {
            return helpers.path
        }
        // dev-fallback: переменная окружения
        if let env = ProcessInfo.processInfo.environment["CLAUDE_PROXY_CORE"],
           FileManager.default.isExecutableFile(atPath: env) {
            return env
        }
        return nil
    }
}
