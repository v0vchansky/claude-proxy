import SwiftUI

struct PopoverView: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.openWindow) private var openWindow
    @State private var copiedCmd = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            Divider()
            statusBlock
            modeSwitcher
            Divider()
            serverBlock
            localProxyBlock
            Divider()
            actions
            if !model.coreAvailable {
                Text("Ядро не запущено — проверь сборку бандла")
                    .font(.caption).foregroundStyle(.red)
            }
            lastErrorBlock
        }
        .padding(14)
        .frame(width: 340)
        .sheet(isPresented: $model.showVpndOnboarding) { onboardingSheet }
    }

    private var header: some View {
        HStack {
            Image(systemName: model.iconName)
            Text("Claude Proxy").font(.headline)
            Spacer()
        }
    }

    // Сегмент Off / Proxy / Full VPN. Взаимное исключение держит AppModel.setMode (§8).
    private var modeSwitcher: some View {
        VStack(alignment: .leading, spacing: 6) {
            Picker("", selection: Binding(
                get: { model.currentMode },
                set: { model.setMode($0) }
            )) {
                Text("Выкл").tag(AppModel.AppMode.off)
                Text("Прокси").tag(AppModel.AppMode.proxy)
                Text("Полный VPN").tag(AppModel.AppMode.full)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .disabled(model.busy || model.vpnBusy || !model.coreAvailable || model.active == nil)

            if model.active == nil {
                Text("Нет серверов — добавьте в Серверы…")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    // Онбординг установки системного компонента (root-демона vpnd).
    private var onboardingSheet: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 8) {
                Image(systemName: "lock.shield").font(.title2)
                Text("Установить системный компонент").font(.headline)
            }
            Text("Полный VPN направляет весь трафик системы через туннель. Для этого нужен системный компонент (root-демон). При установке macOS запросит пароль администратора — он нужен один раз.")
                .font(.callout).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if !model.uiError.isEmpty {
                Text(model.uiError).font(.caption).foregroundStyle(.red).textSelection(.enabled)
            }
            HStack {
                Spacer()
                Button("Отмена") { model.cancelVpndOnboarding() }
                    .disabled(model.vpnBusy)
                Button {
                    model.installVpnd()
                } label: {
                    if model.vpnBusy {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Установить")
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(model.vpnBusy)
            }
        }
        .padding(18)
        .frame(width: 360)
    }

    // Единый блок статуса под переключателем режимов. Ветвится по актуальному
    // режиму (currentMode): Выкл → только серая лампа «Отключено»; Прокси →
    // состояние и метрики из core; Полный VPN → состояние и метрики из vpnStatus.
    @ViewBuilder private var statusBlock: some View {
        VStack(alignment: .leading, spacing: 4) {
            switch model.currentMode {
            case .off:
                HStack(spacing: 6) {
                    Circle().fill(model.statusColor).frame(width: 9, height: 9)
                    Text("Отключено").font(.subheadline).bold()
                }

            case .proxy:
                HStack(spacing: 6) {
                    Circle().fill(model.statusColor).frame(width: 9, height: 9)
                    Text(model.core.state.title).font(.subheadline).bold()
                    if model.busy { ProgressView().controlSize(.small).padding(.leading, 4) }
                }
                if model.core.state == .connected || model.core.state == .error {
                    statusLine("Пинг: \(model.pingText)")
                    statusLine("Проверено: \(model.lastCheckText)")
                    statusLine("На связи: \(model.uptimeText)")
                    statusLine("Трафик: ↓ \(model.rxText)  ↑ \(model.txText)")
                }

            case .full:
                HStack(spacing: 6) {
                    Circle().fill(model.statusColor).frame(width: 9, height: 9)
                    Image(systemName: "network.badge.shield.half.filled")
                    Text("Полный VPN: \(model.vpnStatus.state.title)").font(.subheadline).bold()
                    if model.vpnBusy { ProgressView().controlSize(.small).padding(.leading, 4) }
                }
                if model.vpnStatus.state == .connected {
                    if !model.vpnStatus.utun.isEmpty {
                        statusLine("Интерфейс: \(model.vpnStatus.utun)")
                    }
                    Text("Kill-switch: \(model.vpnStatus.killSwitch ? "вкл" : "выкл")")
                        .font(.caption)
                        .foregroundStyle(model.vpnStatus.killSwitch ? Color.green : Color.orange)
                    statusLine("Пинг: \(model.pingText(model.vpnStatus.pingMs))")
                    statusLine("Проверено: \(model.agoText(model.vpnStatus.lastCheckUnix))")
                    statusLine("На связи: \(model.uptimeText(since: model.vpnStatus.connectedSinceUnix))")
                    statusLine("Трафик: ↓ \(model.bytesText(model.vpnStatus.rxBytes))  ↑ \(model.bytesText(model.vpnStatus.txBytes))")
                    if model.vpnStatus.doubleVpnWarning {
                        Text("Внимание: уже активен другой VPN")
                            .font(.caption).foregroundStyle(.orange)
                    }
                }
            }
        }
    }

    // Строка детали статуса — одинаковое оформление для всех метрик.
    private func statusLine(_ text: String) -> some View {
        Text(text).font(.caption).foregroundStyle(.secondary)
    }

    private var serverBlock: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Сервер").font(.caption).foregroundStyle(.secondary)
            if model.profiles.isEmpty {
                Text("Нет серверов — добавьте в Серверы…")
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                Picker("", selection: Binding(
                    get: { model.activeID },
                    set: { model.switchServer($0) }
                )) {
                    ForEach(model.profiles) { p in
                        Text(p.displayName).tag(p.id)
                    }
                }
                .labelsHidden()
                .disabled(model.busy)

                if let a = model.active {
                    Text("\(a.host):\(a.port)")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }

    private var localProxyBlock: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("Локальный прокси").font(.caption).foregroundStyle(.secondary)
            Text(model.core.localProxy).font(.system(.caption, design: .monospaced))
        }
    }

    private var actions: some View {
        VStack(spacing: 8) {
            // Акцентное действие — проверить соединение (как в макете §4).
            Button {
                model.refresh()
            } label: {
                Label("Обновить", systemImage: "arrow.clockwise").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .disabled(model.busy || model.vpnBusy || !model.coreAvailable)
            .help("Проверить соединение")

            // Скопировать команду запуска Claude Code.
            Button {
                model.copyClaudeCommand(); flash($copiedCmd)
            } label: {
                Label(copiedCmd ? "Скопировано" : "Скопировать команду Claude",
                      systemImage: copiedCmd ? "checkmark" : "doc.on.doc")
                    .frame(maxWidth: .infinity)
            }
            .help("Скопировать команду запуска Claude Code через прокси")

            // Журнал и Серверы — в один ряд.
            HStack(spacing: 8) {
                Button {
                    NSApp.activate(ignoringOtherApps: true)
                    openWindow(id: "logs")
                } label: {
                    Label("Журнал", systemImage: "list.bullet.rectangle").frame(maxWidth: .infinity)
                }
                .help("Открыть окно журнала логов")
                Button {
                    NSApp.activate(ignoringOtherApps: true)
                    openWindow(id: "servers")
                } label: {
                    Text("Серверы…").frame(maxWidth: .infinity)
                }
                .help("Управление серверами")
            }

            Button {
                NSApplication.shared.terminate(nil)
            } label: {
                Text("Выход").frame(maxWidth: .infinity)
            }
            .help("Закрыть приложение")

            Divider().padding(.vertical, 2)

            VStack(alignment: .leading, spacing: 6) {
                Toggle("Запуск при входе", isOn: $model.launchAtLogin)
                Toggle("Включать прокси при запуске", isOn: $model.enableOnLaunch)
            }
            .toggleStyle(.checkbox)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .buttonStyle(.bordered)
        .controlSize(.regular)
    }

    private var lastErrorBlock: some View {
        HStack(alignment: .top, spacing: 4) {
            Text("Последняя ошибка:").font(.caption).foregroundStyle(.secondary)
            Text(model.uiError.isEmpty ? "—" : model.uiError)
                .font(.caption)
                .foregroundStyle(model.uiError.isEmpty ? Color.secondary : Color.red)
                .textSelection(.enabled)
        }
    }

    private func flash(_ binding: Binding<Bool>) {
        binding.wrappedValue = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.2) { binding.wrappedValue = false }
    }
}
