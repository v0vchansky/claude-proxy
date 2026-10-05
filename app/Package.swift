// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "ClaudeProxyApp",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "ClaudeProxyApp",
            path: "Sources/ClaudeProxyApp"
        ),
        // Юнит-тесты чистой логики приложения (swift test; нужен Xcode ради XCTest).
        .testTarget(
            name: "ClaudeProxyAppTests",
            dependencies: ["ClaudeProxyApp"],
            path: "Tests/ClaudeProxyAppTests"
        )
    ]
)
