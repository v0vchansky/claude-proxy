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

    /// Кнопка «Обновить». В режиме Полного VPN проверяет связь демона
    /// (healthcheck-full), иначе — прокси-ядра (healthcheck), как было.
    func refresh() {
        if currentMode == .full {
            refreshVpnHealth()
            return
        }
        busy = true
        let client = self.client
        work.async { [weak self] in
            let result = Result { try client.healthcheck() }
            DispatchQueue.main.async { self?.apply(result) }
        }
    }

    /// Активная проверка связи Полного VPN по запросу пользователя.
    func refreshVpnHealth() {
        vpnBusy = true
        let vpn = self.vpnClient
        work.async { [weak self] in
            let result = Result { try vpn.healthcheckFull() }
            DispatchQueue.main.async { self?.applyVpn(result) }
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

    /// Выключить оба режима. Порядок — fail-closed: СНАЧАЛА вернуть прокси из direct
    /// (чтобы 8118 не ходил напрямую после падения туннеля), потом опустить Полный VPN
    /// и WG-туннель прокси. Итог: 8118 жив, forwardMode=off, оба туннеля опущены.
    func turnAllOff() {
        busy = true
        vpnBusy = true
        uiError = ""
        let client = self.client
        let vpn = self.vpnClient
        let vpnWasUp = vpnStatus.state == .connected || vpnStatus.state == .connecting
        let proxyWasUp = core.state != .disconnected
        work.async { [weak self] in
            // 1. Вернуть форвард в off (безопасно и идемпотентно в любом состоянии).
            var proxyState = try? client.forward(mode: "off")
            // 2. Опустить Полный VPN, если был.
            var vpnState: VpnStatus?
            if vpnWasUp { vpnState = try? vpn.disconnectFull() }
            // 3. Опустить WG-туннель прокси, если был (listener 8118 остаётся жив).
            if proxyWasUp, let st = try? client.disconnect() { proxyState = st }
            DispatchQueue.main.async {
                guard let self else { return }
                self.busy = false
                self.vpnBusy = false
                if let vs = vpnState { self.vpnStatus = vs } else if vpnWasUp { self.vpnStatus = VpnStatus() }
                if let ps = proxyState { self.core = ps }
            }
        }
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
            if vpnWasUp {
                // §8 + fail-closed: сперва вернуть прокси из direct, затем опустить
                // Полный VPN. direct не должен пережить падение системного utun.
                _ = try? client.forward(mode: "off")
                vpnState = try? vpn.disconnectFull()
            }
            // connect сам поставит forwardMode=tunnel; listener 8118 жив весь переход.
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

    /// Включить Full VPN (вариант А). Порядок строго fail-closed:
    /// 1) поднять Полный VPN;
    /// 2) ТОЛЬКО при успехе — опустить WG-туннель прокси (listener 8118 остаётся жив,
    ///    §8: два туннеля на одном WG-ключе не держим) и перевести форвард в direct,
    ///    чтобы 8118 заворачивал трафик в системный utun Полного VPN;
    /// 3) если connectFull упал — форвард НЕ трогаем (остаётся как был), показываем ошибку.
    /// direct НИКОГДА не включается без поднятого Полного VPN.
    private func enterFullVPN() {
        vpnBusy = true
        uiError = ""
        let client = self.client
        let vpn = self.vpnClient
        let p = self.active
        let key = self.privateKeyB64
        let proxyWasUp = core.state != .disconnected
        work.async { [weak self] in
            guard let p else { DispatchQueue.main.async { self?.vpnBusy = false }; return }
            // 1. СНАЧАЛА опускаем WG-туннель прокси, чтобы освободить ключ. Иначе два
            //    туннеля с одним ключом одновременно долбятся в сервер — он «мечется»
            //    между источниками (WireGuard roaming flap) и рвёт связь на все секунды
            //    handshake Полного VPN. Listener 8118 при этом остаётся жив.
            if proxyWasUp { _ = try? client.disconnect() }
            // 2. Поднять Полный VPN — ключ свободен, handshake чистый и быстрый.
            let result = Result { try vpn.connectFull(profile: p, privateKey: key) }
            var proxyState: CoreState?
            if case .success(let vs) = result, vs.state == .connected {
                // 3. Прокси резюмирует через системный utun (direct): 8118 снова рабочий.
                proxyState = try? client.forward(mode: "direct")
            } else if proxyWasUp {
                // Не удалось поднять Полный VPN — вернуть прокси, чтобы не остаться без связи.
                proxyState = try? client.connect(p, privateKey: key)
            }
            DispatchQueue.main.async {
                if let ps = proxyState { self?.core = ps }
                self?.applyVpn(result)
            }
        }
    }

    /// Выключить Full VPN. Порядок строго fail-closed: СНАЧАЛА вернуть прокси из direct
    /// (forward off), и только ПОТОМ опустить Полный VPN — чтобы 8118 не остался
    /// ходить напрямую после падения системного utun.
    func exitFullVPN() {
        vpnBusy = true
        uiError = ""
        let client = self.client
        let vpn = self.vpnClient
        work.async { [weak self] in
            let proxyState = try? client.forward(mode: "off")   // 1. прокси из direct → off
            let result = Result { try vpn.disconnectFull() }     // 2. Полный VPN вниз
            DispatchQueue.main.async {
                if let ps = proxyState { self?.core = ps }
                self?.applyVpn(result)
            }
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
        let vpn = self.vpnClient
        work.async { [weak self] in
            let res = Result { try installer.install() }
            // launchctl bootstrap асинхронный — сокет демона появляется не мгновенно.
            // Ждём готовности (ping) до ~8с, иначе первый connect упрётся в «не установлен».
            var ready = false
            if case .success = res {
                for _ in 0..<40 {
                    if vpn.ping() != nil { ready = true; break }
                    usleep(200_000)
                }
            }
            DispatchQueue.main.async {
                guard let self else { return }
                self.vpnBusy = false
                switch res {
                case .success:
                    self.vpnInstalled = true
                    self.showVpndOnboarding = false
                    if ready { self.enterFullVPN() }
                    else { self.uiError = "Демон установлен, но ещё запускается — выберите «Полный VPN» ещё раз" }
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
        let vpn = self.vpnClient
        // Если демон ещё не помечен установленным — проверяем сокет: мог появиться
        // после установки. Как только ответил ping, помечаем установленным.
        if !vpnInstalled {
            work.async { [weak self] in
                guard vpn.ping() != nil else { return }
                DispatchQueue.main.async { self?.vpnInstalled = true }
            }
            return
        }
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

    // Общие хелперы по unix-времени/байтам — применяются и к Proxy, и к Full VPN.

    /// «N сек/мин назад» от переданного момента (0 → «—»).
    func agoText(_ unix: Int64) -> String {
        guard unix > 0 else { return "—" }
        let secs = max(0, Int(Date().timeIntervalSince1970) - Int(unix))
        if secs < 60 { return "\(secs) сек назад" }
        return "\(secs / 60) мин назад"
    }

    /// Аптайм от момента соединения (0 → «—»).
    func uptimeText(since unix: Int64) -> String {
        guard unix > 0 else { return "—" }
        let secs = max(0, Int(Date().timeIntervalSince1970) - Int(unix))
        let h = secs / 3600, m = (secs % 3600) / 60
        if h > 0 { return "\(h) ч \(m) мин" }
        let s = secs % 60
        return "\(m) мин \(s) с"
    }

    /// Пинг в ms (−1 → «—»).
    func pingText(_ ms: Int) -> String { ms >= 0 ? "\(ms) ms" : "—" }

    /// Человекочитаемый объём трафика.
    func bytesText(_ n: Int64) -> String {
        let units = ["B", "KB", "MB", "GB"]
        var v = Double(n), i = 0
        while v >= 1024, i < units.count - 1 { v /= 1024; i += 1 }
        return i == 0 ? "\(n) B" : String(format: "%.1f %@", v, units[i])
    }

    // Производные строки прокси (через общие хелперы).

    var uptimeText: String {
        core.state == .connected ? uptimeText(since: core.connectedSinceUnix) : "—"
    }
    var lastCheckText: String { agoText(core.lastCheckUnix) }
    var pingText: String { pingText(core.pingMs) }
    var rxText: String { bytesText(core.rxBytes) }
    var txText: String { bytesText(core.txBytes) }

    /// Иконка menu bar по ТЕКУЩЕМУ режиму (SF Symbol). Четыре визуально разных
    /// состояния: выключено / прокси-подключён / полный VPN-подключён / ошибка.
    /// Промежуточные фазы (connecting/switching) дают нейтральный `shield`.
    var iconName: String {
        switch currentMode {
        case .off:
            // Ошибка в любом источнике — единый тревожный символ, иначе «выключено».
            if core.state == .error || vpnStatus.state == .error { return "exclamationmark.shield" }
            return "shield.slash"
        case .proxy:
            return core.state == .connected ? "shield.lefthalf.filled" : "shield"
        case .full:
            return vpnStatus.state == .connected ? "lock.shield.fill" : "shield"
        }
    }

    var isOn: Bool {
        switch core.state {
        case .connected, .connecting, .switching: return true
        case .error: return true
        case .disconnected: return false
        }
    }

    /// Цвет лампочки статуса по ТЕКУЩЕМУ режиму: зелёная (подключено),
    /// жёлтая (подключение/переключение), красная (ошибка), серая (выключено).
    var statusColor: Color {
        switch currentMode {
        case .off:
            if core.state == .error || vpnStatus.state == .error { return .red }
            return .secondary
        case .proxy:
            switch core.state {
            case .connected:              return .green
            case .connecting, .switching: return .yellow
            case .error:                  return .red
            case .disconnected:           return .secondary
            }
        case .full:
            switch vpnStatus.state {
            case .connected:  return .green
            case .connecting: return .yellow
            case .error:      return .red
            case .off:        return .secondary
            }
        }
    }
}
