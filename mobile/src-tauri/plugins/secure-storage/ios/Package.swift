// swift-tools-version:5.3
import PackageDescription

let package = Package(
  name: "tauri-plugin-jcode-secure-storage",
  platforms: [.iOS(.v13)],
  products: [.library(name: "tauri-plugin-jcode-secure-storage", type: .static, targets: ["tauri-plugin-jcode-secure-storage"])],
  dependencies: [.package(name: "Tauri", path: "../.tauri/tauri-api")],
  targets: [.target(name: "tauri-plugin-jcode-secure-storage", dependencies: [.byName(name: "Tauri")], path: "Sources")]
)

