import SwiftUI

struct ServersView: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var selectedID: String?
    @State private var draft: ServerProfile = .defaultHostkey
    @State private var isNew = false
    @State private var showProvision = false

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            HStack(spacing: 0) {
                list.frame(width: 220)
                Divider()
                detail
            }
        }
        .frame(minWidth: 700, minHeight: 560)
        .onAppear {
            selectedID = model.activeID
            draft = model.active ?? .defaultHostkey
            isNew = false
        }
        .sheet(isPresented: $showProvision) {
            ProvisionView { newID in
                // По успеху — выделяем новый профиль в списке.
                selectedID = newID
                draft = model.profiles.first(where: { $0.id == newID }) ?? model.active ?? .defaultHostkey
                isNew = false
            }
            .environmentObject(model)
        }
    }

    // MARK: - Header

    private var header: some View {
        HStack {
            Text("Серверы").font(.title3).bold()
            Spacer()
            Menu {
                Button("Авто (по SSH)") {
                    model.resetProvisionState()
                    showProvision = true
                }
                Button("Вручную") { startNew() }
            } label: {
                Label("Добавить сервер", systemImage: "plus")
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
            Button("Готово") { dismiss() }
                .keyboardShortcut(.defaultAction)
        }
        .padding(12)
    }

    // MARK: - List

    private var list: some View {
        List(selection: $selectedID) {
            ForEach(model.profiles) { p in
                HStack(spacing: 8) {
                    Image(systemName: p.id == model.activeID ? "checkmark.circle.fill" : "circle")
                        .foregroundStyle(p.id == model.activeID ? Color.accentColor : .secondary)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(p.displayName).lineLimit(1)
                        Text("\(p.host):\(p.port)").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                .tag(p.id)
            }
        }
        .listStyle(.sidebar)
        .onChange(of: selectedID) { _, newVal in
            if let id = newVal, let p = model.profiles.first(where: { $0.id == id }) {
                draft = p; isNew = false
            }
        }
    }

    // MARK: - Detail

    private var detail: some View {
        VStack(spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    section("Профиль") {
                        row("Название") { TextField("", text: $draft.displayName).textFieldStyle(.roundedBorder) }
                        row("Страна") { TextField("", text: $draft.country).textFieldStyle(.roundedBorder) }
                        row("Провайдер") { TextField("", text: $draft.provider).textFieldStyle(.roundedBorder) }
                    }
                    section("Адрес") {
                        row("Хост") { TextField("", text: $draft.host).textFieldStyle(.roundedBorder) }
                        row("Порт") {
                            TextField("", value: $draft.port, format: .number.grouping(.never))
                                .textFieldStyle(.roundedBorder).frame(width: 110)
                            Spacer(minLength: 0)
                        }
                        row("Публичный ключ сервера") {
                            TextField("", text: $draft.serverPublicKey)
                                .textFieldStyle(.roundedBorder).font(.system(.body, design: .monospaced))
                        }
                        row("Адрес клиента в VPN") { TextField("", text: $draft.clientVpnAddress).textFieldStyle(.roundedBorder).frame(width: 160); Spacer(minLength: 0) }
                        row("Адрес сервера в VPN") { TextField("", text: $draft.serverVpnAddress).textFieldStyle(.roundedBorder).frame(width: 160); Spacer(minLength: 0) }
                    }
                    section("AmneziaWG") {
                        Grid(alignment: .leading, horizontalSpacing: 14, verticalSpacing: 8) {
                            GridRow {
                                num("Jc", $draft.jc); num("Jmin", $draft.jmin); num("Jmax", $draft.jmax)
                            }
                            GridRow {
                                num("S1", $draft.s1); num("S2", $draft.s2); num("S3", $draft.s3)
                            }
                            GridRow {
                                num("S4", $draft.s4); Color.clear.gridCellUnsizedAxes([.horizontal, .vertical]); Color.clear
                            }
                            GridRow {
                                unum("H1", $draft.h1); unum("H2", $draft.h2); Color.clear
                            }
                            GridRow {
                                unum("H3", $draft.h3); unum("H4", $draft.h4); Color.clear
                            }
                        }
                        Text("S3/S4 оставляйте 0 — userspace-клиент их не поддерживает.")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
                .padding(16)
            }
            Divider()
            actionBar
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var actionBar: some View {
        HStack(spacing: 8) {
            Button(isNew ? "Создать" : "Сохранить") { save() }
                .buttonStyle(.borderedProminent)
            Button("Сделать активным") { model.switchServer(draft.id) }
                .disabled(isNew)
            Button("Проверить соединение") { model.refresh() }
                .disabled(isNew || model.core.state == .disconnected)
            Spacer()
            Button(role: .destructive) { delete() } label: { Text("Удалить") }
                .disabled(isNew)
        }
        .padding(12)
    }

    // MARK: - Building blocks

    private func section<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title.uppercased())
                .font(.caption).bold()
                .foregroundStyle(.secondary)
            content()
        }
    }

    /// Строка «подпись слева, поле справа» с одинаковой шириной подписи.
    private func row<Content: View>(_ label: String, @ViewBuilder _ field: () -> Content) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text(label)
                .frame(width: 150, alignment: .leading)
                .foregroundStyle(.secondary)
            field()
        }
    }

    private func num(_ label: String, _ binding: Binding<Int>) -> some View {
        HStack(spacing: 6) {
            Text(label).font(.caption).foregroundStyle(.secondary).frame(width: 34, alignment: .trailing)
            TextField("", value: binding, format: .number.grouping(.never))
                .textFieldStyle(.roundedBorder).frame(width: 90)
        }
    }

    private func unum(_ label: String, _ binding: Binding<UInt32>) -> some View {
        HStack(spacing: 6) {
            Text(label).font(.caption).foregroundStyle(.secondary).frame(width: 34, alignment: .trailing)
            TextField("", value: binding, format: .number.grouping(.never))
                .textFieldStyle(.roundedBorder).frame(width: 90)
        }
    }

    // MARK: - Actions

    private func startNew() {
        var p = ServerProfile.defaultHostkey
        p.id = "srv-" + UUID().uuidString.prefix(8).lowercased()
        p.displayName = "Новый сервер"
        p.host = ""
        draft = p
        isNew = true
        selectedID = nil
    }

    private func save() {
        model.upsertProfile(draft)
        isNew = false
        selectedID = draft.id
    }

    private func delete() {
        model.deleteProfile(draft.id)
        selectedID = model.activeID
        draft = model.active ?? .defaultHostkey
    }
}

// MARK: - Provisioning (авторазвёртывание по SSH)

/// Форма автоматического развёртывания сервера по SSH.
/// Секреты (пароль/ключ) держатся только в @State на время вызова и не сохраняются.
struct ProvisionView: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) private var dismiss

    /// Вызывается по успешному развёртывании с id нового профиля.
    let onDone: (String) -> Void

    private enum AuthMethod: String, CaseIterable, Identifiable {
        case password, key
        var id: String { rawValue }
        var title: String { self == .password ? "Пароль" : "Приватный ключ" }
    }

    @State private var displayName = ""
    @State private var country = ""
    @State private var provider = ""
    @State private var host = ""
    @State private var sshPort = 22
    @State private var sshUser = "root"
    @State private var auth: AuthMethod = .password
    @State private var password = ""
    @State private var keyPath = ""
    @State private var passphrase = ""
    @State private var showAdvanced = false
    @State private var awgPort = 51820
    @State private var clientVpn = "10.77.0.2"

    private var canSubmit: Bool {
        guard !host.trimmingCharacters(in: .whitespaces).isEmpty else { return false }
        switch auth {
        case .password: return !password.isEmpty
        case .key:      return !keyPath.trimmingCharacters(in: .whitespaces).isEmpty
        }
    }

    private var succeeded: Bool { !model.provisionedID.isEmpty }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Развернуть сервер").font(.title3).bold()
                Spacer()
            }
            .padding(12)
            Divider()

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    identitySection
                    sshSection
                    advancedSection
                    if model.provisionBusy || !model.provisionLog.isEmpty || !model.provisionError.isEmpty {
                        progressSection
                    }
                }
                .padding(16)
            }
            Divider()
            actionBar
        }
        .frame(width: 560, height: 620)
    }

    // MARK: Секции

    private var identitySection: some View {
        section("Профиль") {
            row("Название") {
                TextField("", text: $displayName).textFieldStyle(.roundedBorder)
            }
            row("Страна") {
                TextField("опционально", text: $country).textFieldStyle(.roundedBorder)
            }
            row("Провайдер") {
                TextField("опционально", text: $provider).textFieldStyle(.roundedBorder)
            }
        }
    }

    private var sshSection: some View {
        section("SSH-доступ") {
            row("Хост / IP") {
                TextField("1.2.3.4", text: $host).textFieldStyle(.roundedBorder)
            }
            row("Порт SSH") {
                TextField("", value: $sshPort, format: .number.grouping(.never))
                    .textFieldStyle(.roundedBorder).frame(width: 110)
                Spacer(minLength: 0)
            }
            row("Пользователь SSH") {
                TextField("root", text: $sshUser).textFieldStyle(.roundedBorder).frame(width: 200)
                Spacer(minLength: 0)
            }
            row("Аутентификация") {
                Picker("", selection: $auth) {
                    ForEach(AuthMethod.allCases) { m in Text(m.title).tag(m) }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .frame(width: 240)
                Spacer(minLength: 0)
            }
            if auth == .password {
                row("Пароль") {
                    SecureField("", text: $password).textFieldStyle(.roundedBorder)
                }
            } else {
                row("Путь к ключу") {
                    TextField("/path/id_ed25519", text: $keyPath).textFieldStyle(.roundedBorder)
                }
                row("Пароль ключа") {
                    SecureField("опционально", text: $passphrase).textFieldStyle(.roundedBorder)
                }
            }
        }
    }

    private var advancedSection: some View {
        DisclosureGroup("Дополнительно", isExpanded: $showAdvanced) {
            VStack(alignment: .leading, spacing: 8) {
                row("Порт AWG") {
                    TextField("", value: $awgPort, format: .number.grouping(.never))
                        .textFieldStyle(.roundedBorder).frame(width: 110)
                    Spacer(minLength: 0)
                }
                row("Адрес клиента в VPN") {
                    TextField("", text: $clientVpn).textFieldStyle(.roundedBorder).frame(width: 160)
                    Spacer(minLength: 0)
                }
            }
            .padding(.top, 8)
        }
        .font(.caption.bold())
        .foregroundStyle(.secondary)
    }

    private var progressSection: some View {
        section(succeeded ? "Готово" : (model.provisionBusy ? "Разворачиваем…" : "Журнал")) {
            if !model.provisionError.isEmpty {
                Text(model.provisionError)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(model.provisionLog.enumerated()), id: \.offset) { _, line in
                        Text(line)
                            .font(.system(.caption, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(8)
            }
            .frame(height: 160)
            .background(Color(nsColor: .textBackgroundColor))
            .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Color.secondary.opacity(0.25)))
            .textSelection(.enabled)
        }
    }

    private var actionBar: some View {
        HStack(spacing: 8) {
            if model.provisionBusy {
                ProgressView().controlSize(.small)
                Text("Это может занять до пары минут…").font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if succeeded {
                Button("Готово") {
                    let id = model.provisionedID
                    model.resetProvisionState()
                    onDone(id)
                    dismiss()
                }
                .buttonStyle(.borderedProminent)
            } else {
                Button("Отмена") { dismiss() }.disabled(model.provisionBusy)
                Button("Развернуть") { submit() }
                    .buttonStyle(.borderedProminent)
                    .disabled(!canSubmit || model.provisionBusy)
            }
        }
        .padding(12)
    }

    // MARK: Действия

    private func submit() {
        let ssh = SSHConfig(
            host: host.trimmingCharacters(in: .whitespaces),
            port: sshPort,
            user: sshUser.isEmpty ? "root" : sshUser,
            password: auth == .password && !password.isEmpty ? password : nil,
            privateKeyPath: auth == .key && !keyPath.isEmpty ? keyPath.trimmingCharacters(in: .whitespaces) : nil,
            passphrase: auth == .key && !passphrase.isEmpty ? passphrase : nil
        )
        var params = ProvisionParams()
        params.awgPort = awgPort
        params.clientVpnAddress = clientVpn.trimmingCharacters(in: .whitespaces)
        model.provision(displayName: displayName.trimmingCharacters(in: .whitespaces),
                        country: country.trimmingCharacters(in: .whitespaces),
                        provider: provider.trimmingCharacters(in: .whitespaces),
                        ssh: ssh,
                        params: params)
    }

    // MARK: Building blocks

    private func section<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title.uppercased())
                .font(.caption).bold()
                .foregroundStyle(.secondary)
            content()
        }
    }

    private func row<Content: View>(_ label: String, @ViewBuilder _ field: () -> Content) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text(label)
                .frame(width: 150, alignment: .leading)
                .foregroundStyle(.secondary)
            field()
        }
    }
}
