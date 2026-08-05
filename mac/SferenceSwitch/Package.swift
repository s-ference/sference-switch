// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "SferenceSwitch",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(name: "SferenceSwitch", path: "Sources/SferenceSwitch"),
        .testTarget(name: "SferenceSwitchTests", dependencies: ["SferenceSwitch"])
    ]
)
