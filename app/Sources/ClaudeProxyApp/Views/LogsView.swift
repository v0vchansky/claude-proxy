import SwiftUI
import AppKit

/// Встроенный просмотрщик журнала логов ядра: отдельное окно.
/// Данные берём через AppModel (`model.logLines` / `model.refreshLogs()`),
/// второй ControlClient не создаём. Фильтр по подстроке, автоскролл вниз,
/// автообновление раз в 2с (таймер на главном акторе, гасим на onDisappear).
struct LogsView: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) private var dismiss

    /// Текст фильтра: пусто → показываем все строки.
    @State private var filter = ""
    /// Таймер автообновления; живёт пока открыто окно.
    @State private var timer: Timer?

    /// Отфильтрованные строки (регистронезависимо).
    private var lines: [String] {
        let q = filter.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return model.logLines }
        return model.logLines.filter { $0.range(of: q, options: .caseInsensitive) != nil }
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            searchBar
            Divider()
            logList
        }
        .frame(minWidth: 700, minHeight: 560)
        .onAppear {
            model.refreshLogs()
            startTimer()
        }
        .onDisappear { stopTimer() }
    }

    // MARK: - Header

    private var header: some View {
        HStack(spacing: 8) {
            Text("Журнал").font(.title3).bold()
            Spacer()
            Button {
                model.refreshLogs()
            } label: {
                Label("Обновить", systemImage: "arrow.clockwise")
            }
            Button {
                copyAll()
            } label: {
                Label("Скопировать", systemImage: "doc.on.doc")
            }
            Button("Готово") { dismiss() }
                .keyboardShortcut(.defaultAction)
        }
        .padding(12)
    }

    // MARK: - Search

    private var searchBar: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
            TextField("Фильтр по подстроке", text: $filter)
                .textFieldStyle(.roundedBorder)
            if !filter.isEmpty {
                Button {
                    filter = ""
                } label: {
                    Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }

    // MARK: - List

    private var logList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 1) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { idx, line in
                        Text(line)
                            .font(.system(.caption, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .id(idx)
                    }
                }
                .padding(8)
                // Якорь внизу для автоскролла.
                Color.clear.frame(height: 1).id(bottomID)
            }
            .background(Color(nsColor: .textBackgroundColor))
            .textSelection(.enabled)
            // Автоскролл вниз при открытии и после любого обновления/фильтрации.
            .onChange(of: model.logLines) { scrollToBottom(proxy) }
            .onChange(of: filter) { scrollToBottom(proxy) }
            .onAppear { scrollToBottom(proxy) }
        }
    }

    private let bottomID = "logs.bottom.anchor"

    private func scrollToBottom(_ proxy: ScrollViewProxy) {
        // Небольшая задержка, чтобы список успел перестроиться перед скроллом.
        DispatchQueue.main.async { proxy.scrollTo(bottomID, anchor: .bottom) }
    }

    // MARK: - Автообновление

    private func startTimer() {
        stopTimer()
        timer = Timer.scheduledTimer(withTimeInterval: 2.0, repeats: true) { _ in
            Task { @MainActor in model.refreshLogs() }
        }
    }

    private func stopTimer() {
        timer?.invalidate()
        timer = nil
    }

    // MARK: - Буфер обмена

    private func copyAll() {
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setString(model.logLines.joined(separator: "\n"), forType: .string)
    }
}
