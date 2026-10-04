import SwiftUI

struct ServersView: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var selectedID: String?
    @State private var draft: ServerProfile = .defaultHostkey
    @State private var isNew = false

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
            draft = model.active
            isNew = false
        }
    }

    // MARK: - Header

    private var header: some View {
        HStack {
            Text("Servers").font(.title3).bold()
            Spacer()
            Button {
                startNew()
            } label: {
                Label("Add server", systemImage: "plus")
            }
            Button("Done") { dismiss() }
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
                    section("Identity") {
                        row("Display name") { TextField("", text: $draft.displayName).textFieldStyle(.roundedBorder) }
                        row("Country") { TextField("", text: $draft.country).textFieldStyle(.roundedBorder) }
                        row("Provider") { TextField("", text: $draft.provider).textFieldStyle(.roundedBorder) }
                    }
                    section("Endpoint") {
                        row("Host") { TextField("", text: $draft.host).textFieldStyle(.roundedBorder) }
                        row("Port") {
                            TextField("", value: $draft.port, format: .number.grouping(.never))
                                .textFieldStyle(.roundedBorder).frame(width: 110)
                            Spacer(minLength: 0)
                        }
                        row("Server public key") {
                            TextField("", text: $draft.serverPublicKey)
                                .textFieldStyle(.roundedBorder).font(.system(.body, design: .monospaced))
                        }
                        row("Client VPN address") { TextField("", text: $draft.clientVpnAddress).textFieldStyle(.roundedBorder).frame(width: 160); Spacer(minLength: 0) }
                        row("Server VPN address") { TextField("", text: $draft.serverVpnAddress).textFieldStyle(.roundedBorder).frame(width: 160); Spacer(minLength: 0) }
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
            Button(isNew ? "Create" : "Save") { save() }
                .buttonStyle(.borderedProminent)
            Button("Set as active") { model.switchServer(draft.id) }
                .disabled(isNew)
            Button("Test connection") { model.refresh() }
                .disabled(isNew || model.core.state == .disconnected)
            Spacer()
            Button(role: .destructive) { delete() } label: { Text("Delete") }
                .disabled(isNew || model.profiles.count <= 1)
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
        p.displayName = "New server"
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
        draft = model.active
    }
}
