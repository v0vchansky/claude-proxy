import Foundation

/// Хранит server profiles в servers.json и активный профиль.
final class ProfileStore {
    private(set) var profiles: [ServerProfile]
    private(set) var activeID: String

    private struct Persisted: Codable {
        var profiles: [ServerProfile]
        var activeID: String
    }

    init() {
        if let data = try? Data(contentsOf: AppPaths.serversJSON),
           let p = try? JSONDecoder().decode(Persisted.self, from: data),
           !p.profiles.isEmpty {
            profiles = p.profiles
            activeID = p.profiles.contains(where: { $0.id == p.activeID }) ? p.activeID : p.profiles[0].id
        } else {
            // Первый запуск — засеять профиль HOSTKEY из ТЗ.
            profiles = [.defaultHostkey]
            activeID = ServerProfile.defaultHostkey.id
            save()
        }
    }

    var active: ServerProfile {
        profiles.first(where: { $0.id == activeID }) ?? profiles[0]
    }

    func setActive(_ id: String) {
        guard profiles.contains(where: { $0.id == id }) else { return }
        activeID = id
        save()
    }

    func upsert(_ profile: ServerProfile) {
        if let idx = profiles.firstIndex(where: { $0.id == profile.id }) {
            profiles[idx] = profile
        } else {
            profiles.append(profile)
        }
        save()
    }

    func delete(_ id: String) {
        profiles.removeAll { $0.id == id }
        if profiles.isEmpty {
            profiles = [.defaultHostkey]
        }
        if !profiles.contains(where: { $0.id == activeID }) {
            activeID = profiles[0].id
        }
        save()
    }

    private func save() {
        let p = Persisted(profiles: profiles, activeID: activeID)
        if let data = try? JSONEncoder().encode(p) {
            try? data.write(to: AppPaths.serversJSON, options: .atomic)
        }
    }
}
