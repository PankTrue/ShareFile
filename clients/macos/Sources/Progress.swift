import Foundation

// Measurements are transient; recovered durable offsets never enter the rate.
final class TransferMeter {
    private let clock: () -> Double
    private var samples: [(time: Double, bytes: Int64)]
    init(clock: @escaping () -> Double = { ProcessInfo.processInfo.systemUptime }) {
        self.clock = clock; samples = [(clock(), 0)]
    }
    func record(_ bytes: Int64) {
        if bytes < samples.last!.bytes { samples = [(clock(), 0)] }
        if bytes != samples.last!.bytes { samples.append((clock(), bytes)) }
        trim()
    }
    private func trim() {
        while samples.count > 2 && samples[1].time <= clock() - 10 { samples.removeFirst() }
    }
    func rate() -> Double {
        trim()
        let first = samples.first!, last = samples.last!, now = clock()
        guard now - last.time < 10, now > first.time else { return 0 }
        return max(0, Double(last.bytes - first.bytes) / (now - first.time))
    }
}

struct TransferSummary {
    let size: Double
    let offset: Double
    let completed: Int
    let count: Int
    let percent: Double
    let detail: String
    init(jobs: [Upload], rate: Double) {
        size = jobs.reduce(0) { $0 + Double($1.size) }
        offset = jobs.reduce(0) { $0 + Double(max(0, min($1.offset, $1.size))) }
        completed = jobs.filter { $0.status == "done" }.count
        count = jobs.count
        percent = size > 0 ? floor(offset / size * 100) : completed == count ? 100 : 0
        let blocked = jobs.contains { !["queued", "uploading", "uploaded", "sharing", "done"].contains($0.status) }
        if completed == count { detail = "Загрузка завершена" }
        else if jobs.contains(where: { ["error", "uncertain", "cancelling"].contains($0.status) }) { detail = "Требуется действие · Оставшееся время —" }
        else if blocked { detail = "Приостановлено · Оставшееся время —" }
        else if size == offset { detail = "Данные переданы · Завершаем загрузку…" }
        else if jobs.contains(where: { $0.status == "uploading" }) { detail = transferDetails(rate, seconds: rate > 0 ? (size - offset) / rate : nil) }
        else { detail = "В очереди · Оставшееся время —" }
    }
}

func progressSelection(_ jobs: [Upload]) -> [Upload] {
    func batch(_ job: Upload) -> String { job.batch.isEmpty ? job.id : job.batch }
    var active = Set(jobs.filter { $0.status != "done" }.map(batch))
    if active.isEmpty, let last = jobs.last { active.insert(batch(last)) }
    return jobs.filter { active.contains(batch($0)) }
}

func progressBytes(_ bytes: Double) -> String {
    // Double(Int64.max) rounds up; stay below the conversion boundary.
    ByteCountFormatter.string(fromByteCount: Int64(max(0, min(bytes, Double(Int64.max).nextDown))), countStyle: .file)
}
func remainingTime(_ seconds: Double) -> String {
    let rounded = max(1, ceil(seconds))
    if rounded < 60 { return "\(Int(rounded)) с" }
    if rounded < 3600 { return "\(Int(ceil(rounded / 60))) мин" }
    let minutes = Int(ceil(rounded / 60))
    return "\(minutes / 60) ч" + (minutes % 60 > 0 ? " \(minutes % 60) мин" : "")
}
func transferDetails(_ rate: Double, seconds: Double?) -> String {
    guard rate > 0 else { return "Скорость — · Оцениваем оставшееся время…" }
    return progressBytes(rate) + "/с · " + (seconds.map { "Осталось ≈ " + remainingTime($0) } ?? "Оставшееся время —")
}

func verifyTransferProgress() throws {
    guard remainingTime(7199) == "2 ч" else { throw AppFailure(message: "ETA rounding produced 60 minutes") }
    var now = 0.0
    var meter = TransferMeter(clock: { now })
    guard meter.rate() == 0 else { throw AppFailure(message: "Initial transfer rate is nonzero") }
    now = 2; meter.record(200)
    guard meter.rate() == 100 else { throw AppFailure(message: "Wrong confirmed-byte rate") }
    now = 12
    guard meter.rate() == 0 else { throw AppFailure(message: "Stalled rate did not expire") }
    meter = TransferMeter(clock: { now }); now = 14; meter.record(100)
    guard meter.rate() == 50 else { throw AppFailure(message: "Resume reused an old rate") }
    var job = Upload(id: "large", source: "", name: "large", size: 900, modified: 0, fingerprint: "", folder: "", upload: "", file_id: "", offset: 400, status: "uploading", batch: "new")
    var small = job; small.id = "small"; small.size = 100; small.offset = 100; small.status = "done"
    var old = small; old.id = "old"; old.batch = "old"; old.size = 9000; old.offset = 9000
    let selection = progressSelection([old, small, job])
    let summary = TransferSummary(jobs: selection, rate: 100)
    guard summary.percent == 50, summary.size == 1000, summary.completed == 1, summary.detail.contains("Осталось ≈ 5 с") else { throw AppFailure(message: "Wrong batch weighting or ETA") }
    job.status = "paused"
    guard !TransferSummary(jobs: [small, job], rate: 100).detail.contains("Осталось ≈") else { throw AppFailure(message: "Paused selection has ETA") }
    job.size = 0; job.offset = 0; job.status = "sharing"
    guard TransferSummary(jobs: [job], rate: 0).percent == 0 else { throw AppFailure(message: "Empty file completed too early") }
    job.status = "done"
    guard TransferSummary(jobs: [job], rate: 0).percent == 100 else { throw AppFailure(message: "Empty completed file has no progress") }
}
