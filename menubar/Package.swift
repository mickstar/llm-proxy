// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "LLMProxyMenuBar",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "llm-proxy-menubar", targets: ["LLMProxyMenuBar"])
    ],
    targets: [
        .target(
            name: "MenuBarCore",
            path: "Sources/MenuBarCore"
        ),
        .executableTarget(
            name: "LLMProxyMenuBar",
            dependencies: ["MenuBarCore"],
            path: "Sources/LLMProxyMenuBar"
        ),
        .testTarget(
            name: "MenuBarCoreTests",
            dependencies: ["MenuBarCore"],
            path: "Tests/MenuBarCoreTests"
        ),
    ]
)
