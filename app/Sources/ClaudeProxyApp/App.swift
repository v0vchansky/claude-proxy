import SwiftUI
import AppKit

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        // Menu bar приложение без иконки в Dock.
        NSApp.setActivationPolicy(.accessory)
        // Поднять ядро и (опционально) подключиться сразу на старте.
        AppModel.shared.bootstrap()
    }
}

@main
struct ClaudeProxyApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @ObservedObject private var model = AppModel.shared

    var body: some Scene {
        MenuBarExtra {
            PopoverView().environmentObject(model)
        } label: {
            Image(systemName: model.iconName)
        }
        .menuBarExtraStyle(.window)

        // Экран серверов — отдельное окно (а не sheet поверх popover),
        // чтобы его закрытие/Done возвращали в обычное состояние, а клик по
        // иконке всегда показывал popover.
        Window("Servers", id: "servers") {
            ServersView().environmentObject(model)
        }
        .windowResizability(.contentSize)
        .defaultSize(width: 700, height: 600)
    }
}
