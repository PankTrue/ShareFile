import Foundation

// Source keys retain the original Russian copy. The same English catalog ships on all platforms.
enum Localization {
    private static let lock = NSLock()
    private static var selectedLanguage = "en"
    static let english: [String: String] = {
        guard let url = Bundle.main.url(forResource: "en", withExtension: "json"),
              let data = try? Data(contentsOf: url),
              let catalog = try? JSONDecoder().decode([String: String].self, from: data) else {
            fatalError("ShareFile localization resource is missing")
        }
        return catalog
    }()
    private static let sources = Dictionary(english.map { ($0.value, $0.key) }, uniquingKeysWith: { first, _ in first })
    private static let slot = try! NSRegularExpression(pattern: #"\{(\d+)(?::[^}]+)?\}"#)
    private static let templates: [(String, String, NSRegularExpression, [Int])] = english.flatMap { source, translated -> [(String, String, NSRegularExpression, [Int])] in
        guard source.contains("{0") else { return [] }
        return [source, translated].map { text in
            let range = NSRange(text.startIndex..., in: text)
            var pattern = "^", start = text.startIndex
            var indices: [Int] = []
            for match in slot.matches(in: text, range: range) {
                let part = Range(match.range, in: text)!
                pattern += NSRegularExpression.escapedPattern(for: String(text[start..<part.lowerBound])) + "(.*?)"
                indices.append(Int(text[Range(match.range(at: 1), in: text)!])!)
                start = part.upperBound
            }
            pattern += NSRegularExpression.escapedPattern(for: String(text[start...])) + "$"
            return (source, translated, try! NSRegularExpression(pattern: pattern, options: .dotMatchesLineSeparators), indices)
        }
    }
    private static func render(_ text: String, _ arguments: [Int: String]) -> String {
        var result = text
        for match in slot.matches(in: text, range: NSRange(text.startIndex..., in: text)).reversed() {
            let index = Int(text[Range(match.range(at: 1), in: text)!])!
            if let value = arguments[index], let range = Range(match.range, in: result) {
                result.replaceSubrange(range, with: value)
            }
        }
        return result
    }
    static func normalize(_ value: String) -> String { value == "ru" ? "ru" : "en" }
    static var language: String {
        get { lock.lock(); defer { lock.unlock() }; return selectedLanguage }
        set { lock.lock(); selectedLanguage = normalize(newValue); lock.unlock() }
    }
    static var locale: Locale { Locale(identifier: language == "ru" ? "ru_RU" : "en_US") }
    static func text(_ key: String, _ arguments: [String] = []) -> String {
        if arguments.isEmpty && english[key] == nil && sources[key] == nil {
            for (source, translated, pattern, indices) in templates {
                if let match = pattern.firstMatch(in: key, range: NSRange(key.startIndex..., in: key)) {
                    var values: [Int: String] = [:]
                    for (group, index) in indices.enumerated() {
                        values[index] = String(key[Range(match.range(at: group + 1), in: key)!])
                    }
                    return render(language == "ru" ? source : translated, values)
                }
            }
        }
        let source = english[key] != nil ? key : sources[key] ?? key
        let result = language == "ru" ? source : english[source] ?? source
        return render(result, Dictionary(uniqueKeysWithValues: arguments.enumerated().map { ($0.offset, $0.element) }))
    }
}

func L(_ key: String, _ arguments: String...) -> String { Localization.text(key, arguments) }

@MainActor func verifyLocalization() throws {
    defer { Localization.language = "en" }
    Localization.language = "en"
    guard L("Настройки") == "Settings", L("Загружено объектов: {0}", "2") == "Items uploaded: 2",
          L("{0} из {1} · {2}", "{1}", "файл", "{0}") == "{1} of файл · {0}",
          L("Каталог содержит специальный файл: /tmp/Файл.txt") == "The folder contains a special file: /tmp/Файл.txt" else {
        throw AppFailure(message: "English catalog or interpolation failed")
    }
    Localization.language = "ru"
    guard L("Settings") == "Настройки", L("Items uploaded: 2") == "Загружено объектов: 2",
          remainingTime(7199) == "2 ч" else { throw AppFailure(message: "Russian language switch failed") }
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("sharefile-language-" + UUID().uuidString)
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    let path = directory.appendingPathComponent("state.json")
    try Data(#"{"days":30,"notifyUploads":false}"#.utf8).write(to: path)
    let model = Model(stateURL: path)
    guard model.settings.language == "en", Localization.language == "en" else { throw AppFailure(message: "Legacy language is not English") }
    model.serverInput = "https://unsaved.example"
    model.password = "unsaved-password"
    model.setLanguage("ru")
    let restored = Model(stateURL: path)
    guard restored.settings.language == "ru", restored.settings.days == 30, !restored.settings.notifyUploads,
          model.serverInput == "https://unsaved.example", model.password == "unsaved-password" else {
        throw AppFailure(message: "Language persistence changed upload settings or drafts")
    }
    restored.setLanguage("unsupported")
    guard restored.settings.language == "en" else { throw AppFailure(message: "Unsupported language did not fall back to English") }
}
