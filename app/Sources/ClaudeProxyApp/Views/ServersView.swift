import SwiftUI

struct ServersView: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var selectedID: String?
    @State private var draft: ServerProfile = .defaultHostkey
    @State private var isNew = false

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Servers").font(.headline)
                Spacer()
                Button("Add server") { startNew() }
                Button("Done") { dismiss() }
            }
            .padding()
            Divider()
            HStack(spacing: 0) {
                list.frame(width: 200)
                Divider()
                editor.frame(minWidth: 320)
            }
        }
        .frame(width: 560, height: 520)
        .onAppear {
            selectedID = model.activeID
            draft = model.active
            isNew = false
        }
    }

    private var list: some View {
        List(selection: $selectedID) {
            ForEach(model.profiles) { p in
                HStack {
                    Image(systemName: p.id == model.activeID ? "largecircle.fill.circle" : "circle")
                        .foregroundStyle(p.id == model.activeID ? Color.accentColor : .secondary)
                    VStack(alignment: .leading) {
                        Text(p.displayName).font(.body)
                        Text("\(p.host):\(p.port)").font(.caption).foregroundStyle(.secondary)
                    }
                }
                .tag(p.id)
            }
        }
        .onChange(of: selectedID) { _, newVal in
            if let id = newVal, let p = model.profiles.first(where: { $0.id == id }) {
                draft = p; isNew = false
            }
        }
    }

    private var editor: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                group("Identity") {
                    field("Display name", $draft.displayName)
                    field("Country", $draft.country)
                    field("Provider", $draft.provider)
                }
                group("Endpoint") {
                    field("Host", $draft.host)
                    intField("Port", $draft.port)
                    field("Server public key", $draft.serverPublicKey)
                    field("Client VPN address", $draft.clientVpnAddress)
                    field("Server VPN address", $draft.serverVpnAddress)
                }
                group("AmneziaWG") {
                    HStack { intField("Jc", $draft.jc); intField("Jmin", $draft.jmin); intField("Jmax", $draft.jmax) }
                    HStack { intField("S1", $draft.s1); intField("S2", $draft.s2); intField("S3", $draft.s3); intField("S4", $draft.s4) }
                    HStack { uintField("H1", $draft.h1); uintField("H2", $draft.h2) }
                    HStack { uintField("H3", $draft.h3); uintField("H4", $draft.h4) }
                    Text("S3/S4 оставляйте 0 — userspace-клиент их не поддерживает.")
                        .font(.caption2).foregroundStyle(.secondary)
                }

                HStack {
                    Button(isNew ? "Create" : "Save") { save() }
                    Button("Set as active") { model.switchServer(draft.id) }
                        .disabled(isNew)
                    Button("Test connection") { model.refresh() }
                        .disabled(isNew || model.core.state == .disconnected)
                    Spacer()
                    Button(role: .destructive) { delete() } label: { Text("Delete") }
                        .disabled(isNew || model.profiles.count <= 1)
                }
                .padding(.top, 6)
            }
            .padding()
        }
    }

    private func group<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.caption).bold().foregroundStyle(.secondary)
            content()
        }
        .padding(.bottom, 4)
    }

    private func field(_ label: String, _ binding: Binding<String>) -> some View {
        HStack {
            Text(label).frame(width: 130, alignment: .leading).font(.caption)
            TextField(label, text: binding).textFieldStyle(.roundedBorder)
        }
    }

    private func intField(_ label: String, _ binding: Binding<Int>) -> some View {
        HStack {
            Text(label).font(.caption)
            TextField(label, value: binding, format: .number).textFieldStyle(.roundedBorder).frame(width: 90)
        }
    }

    private func uintField(_ label: String, _ binding: Binding<UInt32>) -> some View {
        HStack {
            Text(label).font(.caption)
            TextField(label, value: binding, format: .number).textFieldStyle(.roundedBorder).frame(width: 110)
        }
    }

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
