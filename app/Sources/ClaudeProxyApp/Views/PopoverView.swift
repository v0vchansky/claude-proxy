import SwiftUI

struct PopoverView: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.openWindow) private var openWindow
    @State private var copiedKey = false
    @State private var copiedCmd = false
    @State private var copiedDiag = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            Divider()
            proxyToggle
            statusBlock
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
        .frame(width: 300)
    }

    private var header: some View {
        HStack {
            Image(systemName: model.iconName)
            Text("Claude Proxy").font(.headline)
            Spacer()
        }
    }

    private var proxyToggle: some View {
        HStack {
            Text("Proxy").font(.body)
            Spacer()
            Toggle("", isOn: Binding(
                get: { model.isOn },
                set: { model.toggle(on: $0) }
            ))
            .labelsHidden()
            .toggleStyle(.switch)
            .disabled(model.busy || !model.coreAvailable)
        }
    }

    private var statusBlock: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Circle().fill(model.statusColor).frame(width: 9, height: 9)
                Text(model.core.state.title).font(.subheadline).bold()
                if model.busy { ProgressView().controlSize(.small).padding(.leading, 4) }
            }
            if model.core.state == .connected || model.core.state == .error {
                Text("Ping: \(model.pingText)").font(.caption).foregroundStyle(.secondary)
                Text("Checked: \(model.lastCheckText)").font(.caption).foregroundStyle(.secondary)
                Text("Connected for: \(model.uptimeText)").font(.caption).foregroundStyle(.secondary)
                Text("Traffic: ↓ \(model.rxText)  ↑ \(model.txText)").font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private var serverBlock: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Server").font(.caption).foregroundStyle(.secondary)
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

            Text("\(model.active.host):\(model.active.port)")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private var localProxyBlock: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("Local proxy").font(.caption).foregroundStyle(.secondary)
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
            .disabled(model.busy || !model.coreAvailable)

            // Скопировать команду запуска Claude Code.
            Button {
                model.copyClaudeCommand(); flash($copiedCmd)
            } label: {
                Label(copiedCmd ? "Скопировано" : "Copy Claude command",
                      systemImage: copiedCmd ? "checkmark" : "doc.on.doc")
                    .frame(maxWidth: .infinity)
            }

            HStack(spacing: 8) {
                Button {
                    model.copyPublicKey(); flash($copiedKey)
                } label: {
                    Text(copiedKey ? "Скопировано" : "Copy Public Key").frame(maxWidth: .infinity)
                }
                Button {
                    model.copyDiagnostics(); flash($copiedDiag)
                } label: {
                    Text(copiedDiag ? "Скопировано" : "Copy diagnostics").frame(maxWidth: .infinity)
                }
            }

            HStack(spacing: 8) {
                Button {
                    NSApp.activate(ignoringOtherApps: true)
                    openWindow(id: "servers")
                } label: {
                    Text("Servers…").frame(maxWidth: .infinity)
                }
                Button {
                    NSApplication.shared.terminate(nil)
                } label: {
                    Text("Quit").frame(maxWidth: .infinity)
                }
            }

            Divider().padding(.vertical, 2)

            VStack(alignment: .leading, spacing: 6) {
                Toggle("Launch at login", isOn: $model.launchAtLogin)
                Toggle("Enable proxy on launch", isOn: $model.enableOnLaunch)
            }
            .toggleStyle(.checkbox)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .buttonStyle(.bordered)
        .controlSize(.regular)
    }

    private var lastErrorBlock: some View {
        HStack(alignment: .top, spacing: 4) {
            Text("Last error:").font(.caption).foregroundStyle(.secondary)
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
