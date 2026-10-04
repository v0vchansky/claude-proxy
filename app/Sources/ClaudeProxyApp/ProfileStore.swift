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

    /// Активный профиль или nil, если серверов нет (можно удалить все).
    var active: ServerProfile? {
        profiles.first(where: { $0.id == activeID })
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
        // Разрешаем удалить все серверы — список может стать пустым (Proxy тогда
        // недоступен, пока не добавишь сервер). Дефолт больше не навязываем.
        if !profiles.contains(where: { $0.id == activeID }) {
            activeID = profiles.first?.id ?? ""
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
