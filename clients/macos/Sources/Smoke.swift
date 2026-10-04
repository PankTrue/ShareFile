import Foundation

// Disposable fixture driver exercises the same native model / queue / process bridge as the UI.
// Device tokens arrive through stdin; clipboard and persistent user preferences are untouched.
func integrationSmokeTest() -> Never {
    let data = FileHandle.standardInput.readDataToEndOfFile()
    Task { @MainActor in
        do {
            guard let input = try JSONSerialization.jsonObject(with: data) as? [String: Any],
                  let origin = input["origin"] as? String, let url = URL(string: origin),
                  url.scheme == "http", url.host == "127.0.0.1", url.user == nil,
                  let token = input["token"] as? String,
                  let folder = input["folder"] as? String,
                  let files = input["files"] as? [String], !files.isEmpty else {
                throw AppFailure(message: "Invalid disposable integration fixture")
            }
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent("sharefile-native-smoke-" + UUID().uuidString)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            defer { try? FileManager.default.removeItem(at: directory) }
            let state = directory.appendingPathComponent("state.json")
            let model = Model(stateURL: state, clipboardEnabled: false)
            model.settings.origin = origin; model.token = token
            guard let session = try await model.request("session") as? [String: Any], let user = session["user"] as? [String: Any], let owner = user["id"] as? String else { throw AppFailure(message: "Session validation failed") }
            model.settings.owner = owner; model.settings.folder = folder
            model.settings.username = user["username"] as? String ?? ""
            model.add(files.map { URL(fileURLWithPath: $0) })
            let deadline = Date().addingTimeInterval(60)
            while Date() < deadline {
                if model.settings.uploads.count == files.count && model.settings.uploads.allSatisfy({ $0.status == "done" && !$0.link.isEmpty }) {
                    let saved = try String(contentsOf: state, encoding: .utf8)
                    guard !saved.contains(token), model.settings.uploads.allSatisfy({ !saved.contains($0.link) }) else { throw AppFailure(message: "Checkpoint leaked credentials") }
                    model.add([directory.appendingPathComponent("missing-test-source.txt")])
                    let rejectionDeadline = Date().addingTimeInterval(5)
                    while model.fileErrors.isEmpty && Date() < rejectionDeadline { try await Task.sleep(nanoseconds: 100_000_000) }
                    guard !model.fileErrors.isEmpty else { throw AppFailure(message: "Missing file was not reported") }
                    let result: [String: Any] = ["links": model.settings.uploads.map(\.link), "files": model.settings.uploads.map(\.file_id)]
                    let encoded = try JSONSerialization.data(withJSONObject: result)
                    try FileManager.default.removeItem(at: directory)
                    FileHandle.standardOutput.write(encoded)
                    exit(0)
                }
                if model.settings.uploads.contains(where: { ["error", "uncertain"].contains($0.status) }) { throw AppFailure(message: "Native queue failed: " + (model.settings.uploads.first(where: { !$0.message.isEmpty })?.message ?? "unknown")) }
                try await Task.sleep(nanoseconds: 100_000_000)
            }
            throw AppFailure(message: "Native queue timed out: " + model.notice)
        } catch { fputs("FAIL: \(error.localizedDescription)\n", stderr); exit(1) }
    }
    dispatchMain()
}
