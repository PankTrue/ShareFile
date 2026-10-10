using System.Diagnostics;

namespace ShareFile.Core;

// A transient rolling rate based on a monotonic clock and confirmed bytes from this run.
public sealed class TransferMeter
{
    private readonly Func<double> _clock;
    private readonly List<(double Time, long Bytes)> _samples = [];
    public TransferMeter(Func<double>? clock = null)
    {
        _clock = clock ?? (() => Stopwatch.GetTimestamp() / (double)Stopwatch.Frequency);
        _samples.Add((_clock(), 0));
    }
    public void Record(long transferred)
    {
        if (transferred < _samples[^1].Bytes) { _samples.Clear(); _samples.Add((_clock(), 0)); }
        if (transferred != _samples[^1].Bytes) _samples.Add((_clock(), transferred));
        Trim();
    }
    private void Trim()
    {
        while (_samples.Count > 2 && _samples[1].Time <= _clock() - 10) _samples.RemoveAt(0);
    }
    public double Rate
    {
        get
        {
            Trim();
            var first = _samples[0]; var last = _samples[^1]; var now = _clock();
            return now - last.Time >= 10 || now <= first.Time ? 0 : Math.Max(0, (last.Bytes - first.Bytes) / (now - first.Time));
        }
    }
}

public sealed record TransferSummary(double Size, double Offset, int Completed, int Count, double Percent, string Detail)
{
    public static IReadOnlyList<UploadJob> Selection(IReadOnlyList<UploadJob> jobs)
    {
        static string Batch(UploadJob job) => job.Batch.Length == 0 ? job.Id : job.Batch;
        var batches = jobs.Where(j => j.Status != UploadStatus.Done).Select(Batch).ToHashSet();
        if (batches.Count == 0 && jobs.Count != 0) batches.Add(Batch(jobs[^1]));
        return jobs.Where(j => batches.Contains(Batch(j))).ToArray();
    }
    public static TransferSummary Create(IReadOnlyList<UploadJob> jobs, double rate)
    {
        var size = jobs.Sum(j => (double)j.Size);
        var offset = jobs.Sum(j => (double)Math.Clamp(j.Offset, 0, j.Size));
        var completed = jobs.Count(j => j.Status == UploadStatus.Done);
        var percent = size > 0 ? Math.Floor(offset / size * 100) : completed == jobs.Count ? 100 : 0;
        var blocked = jobs.Any(j => j.Status is not (UploadStatus.Queued or UploadStatus.Uploading or UploadStatus.Uploaded or UploadStatus.Sharing or UploadStatus.Done));
        var detail = completed == jobs.Count ? Localization.Text("Загрузка завершена")
            : jobs.Any(j => j.Status is UploadStatus.Error or UploadStatus.Uncertain or UploadStatus.Cancelling) ? Localization.Text("Требуется действие · Оставшееся время —")
            : blocked ? Localization.Text("Приостановлено · Оставшееся время —")
            : size == offset ? Localization.Text("Данные переданы · Завершаем загрузку…")
            : jobs.Any(j => j.Status == UploadStatus.Uploading) ? Details(rate, rate > 0 ? (size - offset) / rate : null)
            : Localization.Text("В очереди · Оставшееся время —");
        return new(size, offset, completed, jobs.Count, percent, detail);
    }
    public static string Bytes(double bytes)
    {
        string[] units = [Localization.Text("Б"), Localization.Text("КиБ"), Localization.Text("МиБ"), Localization.Text("ГиБ"), Localization.Text("ТиБ")];
        var index = 0;
        while (bytes >= 1024 && index < units.Length - 1) { bytes /= 1024; index++; }
        return bytes.ToString(index == 0 ? "F0" : "F1", Localization.Culture) + " " + units[index];
    }
    public static string RemainingTime(double seconds)
    {
        seconds = Math.Max(1, Math.Ceiling(seconds));
        if (seconds < 60) return Localization.Text("{0:F0} с", seconds);
        if (seconds < 3600) return Localization.Text("{0:F0} мин", Math.Ceiling(seconds / 60));
        var minutes = Math.Ceiling(seconds / 60);
        return Localization.Text("{0:F0} ч", Math.Floor(minutes / 60)) + (minutes % 60 > 0 ? Localization.Text(" {0:F0} мин", minutes % 60) : "");
    }
    public static string Details(double rate, double? seconds) => rate <= 0 ? Localization.Text("Скорость — · Оцениваем оставшееся время…")
        : Bytes(rate) + Localization.Text("/с · ") + (seconds is null ? Localization.Text("Оставшееся время —") : Localization.Text("Осталось ≈ ") + RemainingTime(seconds.Value));
}
