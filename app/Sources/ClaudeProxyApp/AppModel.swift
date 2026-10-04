import Foundation
import SwiftUI
import AppKit

@MainActor
final class AppModel: ObservableObject {
    static let shared = AppModel()

    @Published var core = CoreState()
    @Published var profiles: [ServerProfile] = []
    @Published var activeID: String = ""
    @Published var publicKey: String = ""
    @Published var uiError: String = ""
    @Published var busy: Bool = false
    @Published var coreAvailable: Bool = true

    // Журнал логов для встроенного просмотрщика (LogsView).
    @Published var logLines: [String] = []

    // Full VPN (full-tunnel) — отдельный root-демон vpnd. §8: взаимно исключает Proxy.
    @Published var vpnStatus = VpnStatus()
    @Published var vpnBusy: Bool = false
    @Published var vpnInstalled: Bool = false
    @Published var showVpndOnboarding: Bool = false

    // Прогресс автоматического развёртывания (provision).
    @Published var provisionBusy: Bool = false
    @Published var provisionLog: [String] = []
    @Published var provisionError: String = ""
    @Published var provisionedID: String = ""

    @Published var launchAtLogin: Bool = LaunchAtLogin.isEnabled {
        didSet { LaunchAtLogin.set(launchAtLogin) }
    }
    @Published var enableOnLaunch: Bool = UserDefaults.standard.bool(forKey: "enableOnLaunch") {
        didSet { UserDefaults.standard.set(enableOnLaunch, forKey: "enableOnLaunch") }
    }

    private let store = ProfileStore()
    private let coreProc: CoreProcess
    private let client: ControlClient
    private let vpnClient = VpnClient()
    private let vpnInstaller = VpnInstaller()
    private let work = DispatchQueue(label: "claudeproxy.control")
    private var statusTimer: Timer?
    private var provisionTimer: Timer?
    private var privateKeyB64: String = ""

    init() {
        let sock = AppPaths.controlSocket.path
        self.coreProc = CoreProcess(socketPath: sock, proxyAddr: "127.0.0.1:8118")
        self.client = ControlClient(socketPath: sock)
        self.profiles = store.profiles
        self.activeID = store.activeID
    }

    var active: ServerProfile? { store.active }

    var claudeCommand: String {
        let addr = core.localProxy.isEmpty ? "127.0.0.1:8118" : core.localProxy
        return "HTTP_PROXY=http://\(addr) HTTPS_PROXY=http://\(addr) claude"
    }

    // MARK: - Lifecycle

    func bootstrap() {
        // Ядро поднимаем СРАЗУ и НЕ зависим от Keychain: доступ к ключу может показать
        // системный диалог (особенно после пересборки с новой подписью) — он не должен
        // блокировать запуск ядра/сокета/UI на главном потоке. Ключи грузим в фоне.
        coreAvailable = coreProc.start()
        if !coreAvailable {
            uiError = "Не найден бинарь ядра (claude-proxy-core)"
        }

        vpnInstalled = vpnInstaller.isInstalled()

        startStatusTimer()
        bringUpAsync()

        NotificationCenter.default.addObserver(
            forName: NSApplication.willTerminateNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.shutdown() }
        }
    }

    /// Фоновая инициализация: загрузка ключей (может показать диалог Keychain),
    /// ожидание готовности ядра и, при включённой опции, автоподключение.
    private func bringUpAsync() {
        let client = self.client
        let shouldAuto = self.enableOnLaunch
        let p = self.active
        let pubFile = AppPaths.supportDir.appendingPathComponent("client-public.key")
        work.async { [weak self] in
            // 1. Ключи (в фоне, чтобы диалог Keychain не морозил приложение).
            var priv = "", pub = ""
            do {
                priv = try ClientKey.loadOrCreatePrivateKeyBase64()
                pub = try ClientKey.publicKeyBase64()
                try? (pub + "\n").data(using: .utf8)?.write(to: pubFile)
            } catch {
                DispatchQueue.main.async { self?.uiError = "Ошибка ключа: \(error.localizedDescription)" }
            }
            if !priv.isEmpty {
                DispatchQueue.main.async { self?.privateKeyB64 = priv; self?.publicKey = pub }
            }

            // 2. Ждём готовности ядра (~5с).
            var ok = false
            for _ in 0..<50 {
                if (try? client.ping()) == true { ok = true; break }
                usleep(100_000)
            }
            guard ok else { return }

            // 3. Автоподключение или просто начальный статус.
            if shouldAuto, !priv.isEmpty, let p {
                let result = Result { try client.connect(p, privateKey: priv) }
                DispatchQueue.main.async { self?.apply(result) }
            } else if let st = try? client.status() {
                DispatchQueue.main.async { self?.core = st }
            }
        }
    }

    func shutdown() {
        statusTimer?.invalidate()
        _ = try? client.disconnect()
        coreProc.stop()
    }

    // MARK: - Actions

    func toggle(on: Bool) {
        if on, active == nil {
            uiError = "Нет активного сервера — добавьте сервер в Серверы…"
            return
        }
        busy = true
        let client = self.client
        let p = self.active
        let key = self.privateKeyB64
        work.async { [weak self] in
            let result: Result<CoreState, Error>
            if on, let p {
                result = Result { try client.connect(p, privateKey: key) }
            } else {
                result = Result { try client.disconnect() }
            }
            DispatchQueue.main.async { self?.apply(result) }
        }
    }

    func switchServer(_ id: String) {
        store.setActive(id)
        activeID = id
        let wasConnected = core.state == .connected || core.state == .error || core.state == .switching
        if !wasConnected { return }
        busy = true
        let client = self.client
        let p = self.active
        let key = self.privateKeyB64
        work.async { [weak self] in
            guard let p else { DispatchQueue.main.async { self?.busy = false }; return }
            let result = Result { try client.switchServer(p, privateKey: key) }
            DispatchQueue.main.async { self?.apply(result) }
        }
    }

    func refresh() {
        busy = true
        let client = self.client
        work.async { [weak self] in
            let result = Result { try client.healthcheck() }
            DispatchQueue.main.async { self?.apply(result) }
        }
    }

    private func apply(_ result: Result<CoreState, Error>) {
        busy = false
        switch result {
        case .success(let st):
            core = st
            uiError = st.lastError
        case .failure(let err):
            uiError = (err as? ControlClient.ControlError)?.errorDescription ?? err.localizedDescription
        }
    }

    // MARK: - Modes (Off / Proxy / Full VPN) — §8 взаимное исключение

    enum AppMode: Hashable { case off, proxy, full }

    /// Текущий режим по состояниям обоих подключений (для сегмента в UI).
    /// Full VPN имеет приоритет отображения; ошибочные состояния падают в .off,
    /// а текст ошибки показывает uiError.
    var currentMode: AppMode {
        if vpnStatus.state == .connected || vpnStatus.state == .connecting { return .full }
        if core.state == .connected || core.state == .connecting || core.state == .switching { return .proxy }
        return .off
    }

    var vpnConnecting: Bool { vpnStatus.state == .connecting }

    /// Переключение режима из UI. Встречный режим гасится принудительно (§8:
    /// один WG-ключ = один endpoint, две сессии флапают).
    func setMode(_ mode: AppMode) {
        switch mode {
        case .off:   turnAllOff()
        case .proxy: enterProxy()
        case .full:  requestFullVPN()
        }
    }

    /// Выключить оба режима.
    func turnAllOff() {
        if vpnStatus.state == .connected || vpnStatus.state == .connecting { exitFullVPN() }
        if core.state != .disconnected { toggle(on: false) }
    }

    /// Включить Proxy, предварительно погасив Full VPN.
    func enterProxy() {
        if active == nil {
            uiError = "Нет активного сервера — добавьте сервер в Серверы…"
            return
        }
        busy = true
        uiError = ""
        let client = self.client
        let vpn = self.vpnClient
        let p = self.active
        let key = self.privateKeyB64
        let vpnWasUp = vpnStatus.state == .connected || vpnStatus.state == .connecting
        work.async { [weak self] in
            var vpnState: VpnStatus?
            if vpnWasUp { vpnState = try? vpn.disconnectFull() }  // §8: гасим Full VPN
            let result: Result<CoreState, Error>
            if let p { result = Result { try client.connect(p, privateKey: key) } }
            else { result = Result { try client.disconnect() } }
            DispatchQueue.main.async {
                if let vs = vpnState { self?.vpnStatus = vs } else if vpnWasUp { self?.vpnStatus = VpnStatus() }
                self?.apply(result)
            }
        }
    }

    /// Запрос на Full VPN: если демон не установлен — показать онбординг установки,
    /// иначе подключиться сразу (тем же активным профилем и ключом, что и Proxy).
    func requestFullVPN() {
        if active == nil {
            uiError = "Нет активного сервера — добавьте сервер в Серверы…"
            return
        }
        if !vpnInstalled {
            showVpndOnboarding = true
            return
        }
        enterFullVPN()
    }

    /// Включить Full VPN, предварительно погасив Proxy.
    private func enterFullVPN() {
        vpnBusy = true
        uiError = ""
        let client = self.client
        let vpn = self.vpnClient
        let p = self.active
        let key = self.privateKeyB64
        let proxyWasUp = core.state != .disconnected
        work.async { [weak self] in
            var proxyState: CoreState?
            if proxyWasUp { proxyState = try? client.disconnect() }  // §8: гасим Proxy
            guard let p else { DispatchQueue.main.async { self?.vpnBusy = false }; return }
            let result = Result { try vpn.connectFull(profile: p, privateKey: key) }
            DispatchQueue.main.async {
                if let ps = proxyState { self?.core = ps }
                self?.applyVpn(result)
            }
        }
    }

    /// Выключить Full VPN.
    func exitFullVPN() {
        vpnBusy = true
        uiError = ""
        let vpn = self.vpnClient
        work.async { [weak self] in
            let result = Result { try vpn.disconnectFull() }
            DispatchQueue.main.async { self?.applyVpn(result) }
        }
    }

    private func applyVpn(_ result: Result<VpnStatus, Error>) {
        vpnBusy = false
        switch result {
        case .success(let st):
            vpnStatus = st
            uiError = st.lastError
        case .failure(let err):
            if case VpnClient.VpnError.notInstalled = err {
                vpnInstalled = false
                showVpndOnboarding = true
            }
            uiError = (err as? VpnClient.VpnError)?.errorDescription ?? err.localizedDescription
        }
    }

    // MARK: - Установка демона vpnd

    /// Ставит root-демон (запрос пароля администратора через osascript), затем
    /// поднимает Full VPN. Отмену пароля трактуем как отказ от онбординга.
    func installVpnd() {
        vpnBusy = true
        uiError = ""
        let installer = self.vpnInstaller
        work.async { [weak self] in
            let res = Result { try installer.install() }
            DispatchQueue.main.async {
                guard let self else { return }
                self.vpnBusy = false
                switch res {
                case .success:
                    self.vpnInstalled = true
                    self.showVpndOnboarding = false
                    self.enterFullVPN()
                case .failure(let err):
                    if case VpnInstaller.InstallError.cancelled = err {
                        self.showVpndOnboarding = false
                    }
                    self.uiError = (err as? VpnInstaller.InstallError)?.errorDescription
                        ?? err.localizedDescription
                }
            }
        }
    }

    func cancelVpndOnboarding() { showVpndOnboarding = false }

    // MARK: - Status polling

    private func startStatusTimer() {
        statusTimer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.refreshStatusOnce()
                self?.refreshVpnStatusOnce()
            }
        }
    }

    /// Фоновый опрос статуса Full VPN (только если демон установлен).
    private func refreshVpnStatusOnce() {
        guard vpnInstalled else { return }
        let vpn = self.vpnClient
        work.async { [weak self] in
            guard let st = try? vpn.statusFull() else { return }
            DispatchQueue.main.async {
                guard let self, !self.vpnBusy else { return }
                self.vpnStatus = st
                if !st.lastError.isEmpty { self.uiError = st.lastError }
            }
        }
    }

    private func refreshStatusOnce() {
        let client = self.client
        work.async { [weak self] in
            guard let st = try? client.status() else { return }
            DispatchQueue.main.async {
                guard let self, !self.busy else { return }
                self.core = st
                if !st.lastError.isEmpty { self.uiError = st.lastError }
            }
        }
    }

    // MARK: - Provisioning

    /// Живой прогресс: пока идёт provision, опрашиваем журнал ядра (команда logs
    /// обрабатывается параллельно с длинным provision) и показываем новые шаги
    /// «provision: …» по мере их появления. baseline отсекает строки прошлых запусков.
    private func startProvisionPolling() {
        let client = self.client
        work.async { [weak self] in
            let baseline = ((try? client.logs()) ?? []).filter { $0.contains("provision:") }.count
            DispatchQueue.main.async {
                guard let self, self.provisionBusy else { return }
                self.provisionTimer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
                    Task { @MainActor in self?.pollProvisionLog(baseline: baseline) }
                }
            }
        }
    }

    private func pollProvisionLog(baseline: Int) {
        guard provisionBusy else { stopProvisionPolling(); return }
        let client = self.client
        work.async { [weak self] in
            let prov = ((try? client.logs()) ?? []).filter { $0.contains("provision:") }
            let fresh = prov.count > baseline ? Array(prov.suffix(prov.count - baseline)) : []
            let steps = fresh.map { line -> String in
                if let r = line.range(of: "provision:") {
                    return String(line[r.upperBound...]).trimmingCharacters(in: .whitespaces)
                }
                return line
            }
            DispatchQueue.main.async {
                guard let self, self.provisionBusy else { return }
                self.provisionLog = ["Подключаемся к серверу…"] + steps
            }
        }
    }

    private func stopProvisionPolling() {
        provisionTimer?.invalidate()
        provisionTimer = nil
    }

    /// Разворачивает новый сервер по SSH и, по успеху, добавляет профиль и делает его активным.
    /// Секреты (пароль/ключ) живут только в переданном `ssh` на время вызова — никуда не сохраняются.
    func provision(displayName: String,
                   country: String,
                   provider: String,
                   ssh: SSHConfig,
                   params: ProvisionParams) {
        provisionBusy = true
        provisionError = ""
        provisionedID = ""
        provisionLog = ["Подключаемся к \(ssh.host)…"]
        startProvisionPolling()

        let client = self.client
        let pub = self.publicKey
        work.async { [weak self] in
            do {
                let res = try client.provision(ssh: ssh, params: params, clientPublicKey: pub)
                let id = "srv-" + UUID().uuidString.prefix(8).lowercased()
                let profile = ServerProfile(
                    id: id,
                    displayName: displayName.isEmpty ? res.host : displayName,
                    country: country,
                    provider: provider,
                    host: res.host,
                    port: res.port,
                    serverPublicKey: res.serverPublicKey,
                    clientVpnAddress: res.clientVpnAddress,
                    serverVpnAddress: res.serverVpnAddress,
                    dns: ["1.1.1.1", "8.8.8.8"],
                    mtu: 1420,
                    persistentKeepalive: 25,
                    jc: res.jc, jmin: res.jmin, jmax: res.jmax,
                    s1: res.s1, s2: res.s2, s3: 0, s4: 0,
                    h1: res.h1, h2: res.h2, h3: res.h3, h4: res.h4
                )
                DispatchQueue.main.async {
                    guard let self else { return }
                    self.store.upsert(profile)
                    self.store.setActive(profile.id)
                    self.profiles = self.store.profiles
                    self.activeID = self.store.activeID
                    self.provisionLog = res.log
                    self.provisionedID = profile.id
                    self.stopProvisionPolling(); self.provisionBusy = false
                }
            } catch let e as ControlClient.ProvisionFailure {
                DispatchQueue.main.async {
                    guard let self else { return }
                    self.provisionLog = e.log
                    self.provisionError = e.message
                    self.stopProvisionPolling(); self.provisionBusy = false
                }
            } catch {
                DispatchQueue.main.async {
                    guard let self else { return }
                    let msg = (error as? ControlClient.ControlError)?.errorDescription
                        ?? error.localizedDescription
                    self.provisionError = msg
                    self.stopProvisionPolling(); self.provisionBusy = false
                }
            }
        }
    }

    /// Сбрасывает состояние прогресса (при открытии формы заново).
    func resetProvisionState() {
        provisionBusy = false
        provisionLog = []
        provisionError = ""
        provisionedID = ""
    }

    // MARK: - Profiles management

    func upsertProfile(_ p: ServerProfile) {
        store.upsert(p)
        profiles = store.profiles
    }

    func deleteProfile(_ id: String) {
        store.delete(id)
        profiles = store.profiles
        activeID = store.activeID
    }

    // MARK: - Clipboard

    func copyPublicKey() { setClipboard(publicKey) }
    func copyClaudeCommand() { setClipboard(claudeCommand) }

    /// Обновляет журнал для встроенного просмотрщика: читает `client.logs()`
    /// в фоне и публикует в `logLines`. Паттерн захвата client — как у остальных.
    func refreshLogs() {
        let client = self.client
        work.async { [weak self] in
            let lines = (try? client.logs()) ?? []
            DispatchQueue.main.async { self?.logLines = lines }
        }
    }

    func copyDiagnostics() {
        let client = self.client
        work.async { [weak self] in
            let lines = (try? client.logs()) ?? []
            DispatchQueue.main.async { self?.setClipboard(lines.joined(separator: "\n")) }
        }
    }

    private func setClipboard(_ s: String) {
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setString(s, forType: .string)
    }

    // MARK: - Derived labels

    var uptimeText: String {
        guard core.state == .connected, core.connectedSinceUnix > 0 else { return "—" }
        let secs = max(0, Int(Date().timeIntervalSince1970) - Int(core.connectedSinceUnix))
        let h = secs / 3600, m = (secs % 3600) / 60
        if h > 0 { return "\(h) ч \(m) мин" }
        let s = secs % 60
        return "\(m) мин \(s) с"
    }

    var lastCheckText: String {
        guard core.lastCheckUnix > 0 else { return "—" }
        let secs = max(0, Int(Date().timeIntervalSince1970) - Int(core.lastCheckUnix))
        if secs < 60 { return "\(secs) сек назад" }
        return "\(secs / 60) мин назад"
    }

    var pingText: String { core.pingMs >= 0 ? "\(core.pingMs) ms" : "—" }

    var rxText: String { Self.humanBytes(core.rxBytes) }
    var txText: String { Self.humanBytes(core.txBytes) }

    private static func humanBytes(_ n: Int64) -> String {
        let units = ["B", "KB", "MB", "GB"]
        var v = Double(n), i = 0
        while v >= 1024, i < units.count - 1 { v /= 1024; i += 1 }
        return i == 0 ? "\(n) B" : String(format: "%.1f %@", v, units[i])
    }

    /// Иконка menu bar по состоянию (SF Symbol).
    var iconName: String {
        switch core.state {
        case .connected:              return "shield.lefthalf.filled"
        case .connecting, .switching: return "shield"
        case .error:                  return "exclamationmark.shield"
        case .disconnected:           return "shield.slash"
        }
    }

    var isOn: Bool {
        switch core.state {
        case .connected, .connecting, .switching: return true
        case .error: return true
        case .disconnected: return false
        }
    }

    var statusColor: Color {
        switch core.state {
        case .connected:              return .green
        case .connecting, .switching: return .yellow
        case .error:                  return .red
        case .disconnected:           return .secondary
        }
    }
}
