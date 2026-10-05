import XCTest
@testable import ClaudeProxyApp

/// Разбор ps и политика «завершать/предупреждать» для посторонних ядер.
final class ForeignCoresTests: XCTestCase {
    let support = "/Users/u/Library/Application Support/ClaudeProxy"
    let bundle = "/Applications/Claude Proxy.app"
    var ourCore: String { bundle + "/Contents/Helpers/claude-proxy-core" }
    var ourSock: String { support + "/control.sock" }

    // Реалистичный вывод `ps -axww -o pid=,ppid=,uid=,command=`.
    var psOutput: String {
        """
          1     0     0 /sbin/launchd
        100     1   501 \(bundle)/Contents/MacOS/ClaudeProxyApp
        101   100   501 \(ourCore) -sock \(support)/control.sock -proxy 127.0.0.1:8118 -log \(support)/diagnostics.log
        200     1     0 /Library/PrivilegedHelperTools/claude-proxy-core -mode vpnd -sock /var/run/claude-proxy-vpnd.sock -state /var/lib/claude-proxy/vpnd-state.json -sock-uid 501
        300   250   501 /Users/u/Desktop/claude-proxy/core/bin/claude-proxy-core -sock /tmp/cpc-t.sock -proxy 127.0.0.1:8119
        301   250   501 /usr/bin/pkill -f claude-proxy-core -sock /tmp/x.sock
        302   250   501 grep claude-proxy-core
        303   250   501 /bin/sh -c /Users/u/Desktop/claude-proxy/core/bin/claude-proxy-core -sock /tmp/y.sock
        304   250   502 /Users/v/claude-proxy-core -sock /tmp/other-user.sock
        305   250   501 /opt/x/claude-proxy-core-old -sock /tmp/z.sock
        306   250   501 ./claude-proxy-core -verbose
        garbage line
        """
    }

    func testParsePSKeepsOnlyCoreExecutables() {
        let procs = ForeignCores.parsePS(psOutput)
        XCTAssertEqual(procs.map(\.pid), [101, 200, 300, 304, 306])

        let app = procs[0]
        XCTAssertEqual(app.ppid, 100)
        XCTAssertEqual(app.uid, 501)
        XCTAssertEqual(app.executable, ourCore, "путь с пробелами в имени бандла")
        XCTAssertEqual(app.sock, ourSock, "путь сокета с пробелом (Application Support)")
        XCTAssertEqual(app.proxy, "127.0.0.1:8118")
        XCTAssertEqual(app.flags["log"], support + "/diagnostics.log")
        XCTAssertEqual(app.mode, "proxy")

        XCTAssertEqual(procs[1].mode, "vpnd")
        XCTAssertEqual(procs[2].proxy, "127.0.0.1:8119")
        XCTAssertEqual(procs[2].sock, "/tmp/cpc-t.sock")
        XCTAssertEqual(procs[4].executable, "./claude-proxy-core")
        XCTAssertEqual(procs[4].proxy, "127.0.0.1:8118", "без -proxy — порт по умолчанию")
        XCTAssertEqual(procs[4].flags["verbose"], "")
    }

    func testParsePSEmptyAndGarbage() {
        XCTAssertEqual(ForeignCores.parsePS(""), [])
        XCTAssertEqual(ForeignCores.parsePS("\n\n  \nabc def ghi jkl\n1 2\n"), [])
    }

    func testParseFlagsVariants() {
        let f = ForeignCores.parseFlags("--sock=/a b/c.sock -proxy :9000 -verbose -mode vpnd")
        // Форма `-name=value` берёт один токен: хвост после пробела не склеивается.
        XCTAssertEqual(f["sock"], "/a")
        XCTAssertEqual(f["proxy"], ":9000")
        XCTAssertEqual(f["verbose"], "")
        XCTAssertEqual(f["mode"], "vpnd")
        XCTAssertEqual(ForeignCores.parseFlags(""), [:])
        // Значение, похожее на отрицательное число, не флаг.
        XCTAssertEqual(ForeignCores.parseFlags("-sock-uid -1")["sock-uid"], "-1")
    }

    func testForeignExcludesVpndSelfChildrenOtherUsers() {
        let procs = ForeignCores.parsePS(psOutput)
        // Мы — pid 100 (приложение): 101 — наш дочерний.
        let f = ForeignCores.foreign(procs, uid: 501, selfPid: 100, excludePids: [])
        XCTAssertEqual(f.map(\.pid), [300, 306])
        // Явное исключение (известный pid своего ядра).
        let f2 = ForeignCores.foreign(procs, uid: 501, selfPid: 999, excludePids: [101, 306])
        XCTAssertEqual(f2.map(\.pid), [300])
        // Осиротевшее боевое ядро (родитель умер, ppid=1) — чужое.
        let f3 = ForeignCores.foreign(procs, uid: 501, selfPid: 999, excludePids: [])
        XCTAssertTrue(f3.contains { $0.pid == 101 })
    }

    func testIsOurBinary() {
        XCTAssertTrue(ForeignCores.isOurBinary(ourCore, bundlePath: bundle, ourCore: nil))
        XCTAssertTrue(ForeignCores.isOurBinary("/x/y", bundlePath: "", ourCore: "/x/y"))
        XCTAssertTrue(ForeignCores.isOurBinary("/Users/u/Desktop/claude-proxy/core/bin/claude-proxy-core",
                                               bundlePath: bundle, ourCore: ourCore))
        XCTAssertFalse(ForeignCores.isOurBinary("/opt/other/claude-proxy-core", bundlePath: bundle, ourCore: ourCore))
        // Соседний бандл с общим префиксом имени — не наш.
        XCTAssertFalse(ForeignCores.isOurBinary("/Applications/Claude Proxy.app.old/Contents/Helpers/claude-proxy-core",
                                                bundlePath: bundle, ourCore: ourCore))
    }

    func testPort() {
        XCTAssertEqual(ForeignCores.port("127.0.0.1:8118"), "8118")
        XCTAssertEqual(ForeignCores.port(":8118"), "8118")
        XCTAssertEqual(ForeignCores.port("[::1]:8119"), "8119")
        XCTAssertNil(ForeignCores.port("8118"))
        XCTAssertNil(ForeignCores.port("127.0.0.1:"))
    }

    private func proc(_ exe: String, _ args: String) -> CoreProcInfo {
        CoreProcInfo(pid: 1, ppid: 1, uid: 501, command: exe + " " + args, executable: exe, args: args)
    }

    func testShouldKillPolicy() {
        let repo = "/Users/u/Desktop/claude-proxy/core/bin/claude-proxy-core"
        let kill = { (p: CoreProcInfo, connected: Bool) in
            ForeignCores.shouldKill(p, connected: connected, ourProxy: "127.0.0.1:8118", ourSock: self.ourSock,
                                    bundlePath: self.bundle, ourCore: self.ourCore)
        }
        // Инцидент: тестовое ядро из core/bin на 8119, подключено → завершить.
        XCTAssertTrue(kill(proc(repo, "-sock /tmp/cpc-t.sock -proxy 127.0.0.1:8119"), true))
        // То же, но не подключено и порт другой → только предупреждение.
        XCTAssertFalse(kill(proc(repo, "-sock /tmp/cpc-t.sock -proxy 127.0.0.1:8119"), false))
        // Наш бинарь занимает наш порт (явно или по умолчанию) → завершить.
        XCTAssertTrue(kill(proc(ourCore, "-sock /tmp/a.sock -proxy 127.0.0.1:8118"), false))
        XCTAssertTrue(kill(proc(repo, "-sock /tmp/a.sock"), false))
        // Наш сокет → завершить.
        XCTAssertTrue(kill(proc(repo, "-sock \(ourSock) -proxy 127.0.0.1:9999"), false))
        // Чужой бинарь не трогаем, даже подключённый и на нашем порту.
        XCTAssertFalse(kill(proc("/opt/other/claude-proxy-core", "-proxy 127.0.0.1:8118"), true))
    }

    func testWarningTextHasPidAndCommand() {
        let p = proc("/x/claude-proxy-core", "-sock /tmp/t.sock")
        let w = ForeignCores.warningText(p, killed: true)
        XCTAssertTrue(w.contains("pid 1"))
        XCTAssertTrue(w.contains("/x/claude-proxy-core -sock /tmp/t.sock"))
        XCTAssertTrue(w.contains("завершено"))
        XCTAssertTrue(ForeignCores.warningText(p, killed: false).contains("не тронуто"))
    }
}
