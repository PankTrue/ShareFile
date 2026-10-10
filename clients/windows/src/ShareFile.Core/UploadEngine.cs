using System.Globalization;
using System.Security.Cryptography;
using System.Text;

namespace ShareFile.Core;

public sealed partial class UploadEngine
{
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;
    public UploadEngine(Func<TimeSpan, CancellationToken, Task>? delay = null) => _delay = delay ?? Task.Delay;

    private static FileStream Open(string source) => new(source, FileMode.Open, FileAccess.Read, FileShare.Read,
        64 * 1024, FileOptions.Asynchronous | FileOptions.RandomAccess);
    public static string NewId() => Convert.ToBase64String(RandomNumberGenerator.GetBytes(32)).TrimEnd('=').Replace('+', '-').Replace('/', '_');

    public static async Task<UploadJob> PrepareAsync(string source, string folder, CancellationToken ct = default)
    {
        Protocol.RequireIdentifier(folder);
        source = Path.TrimEndingDirectorySeparator(Path.GetFullPath(source));
        if (System.IO.Directory.Exists(source)) return await PrepareDirectoryAsync(source, folder, ct).ConfigureAwait(false);
        if (!File.Exists(source) || (File.GetAttributes(source) & FileAttributes.Directory) != 0)
            throw new ClientException(Localization.Text("Можно загружать только обычные файлы"));
        await using var file = Open(source);
        var ticks = File.GetLastWriteTimeUtc(source).Ticks;
        var size = file.Length;
        return new UploadJob
        {
            Id = NewId(),
            Source = source,
            Name = Path.GetFileName(source),
            Folder = folder,
            Size = size,
            ModifiedTicks = ticks,
            Fingerprint = await FingerprintAsync(file, size, ticks, ct).ConfigureAwait(false)
        };
    }

    private static async Task<string> FingerprintAsync(FileStream file, long size, long ticks, CancellationToken ct)
    {
        using var hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        hash.AppendData(Encoding.UTF8.GetBytes(string.Create(CultureInfo.InvariantCulture, $"{size}:{ticks}:")));
        var buffer = new byte[65536];
        foreach (var offset in new[] { 0, Math.Max(0, size / 2 - 32768), Math.Max(0, size - 65536) })
        {
            file.Position = offset;
            var length = (int)Math.Min(buffer.Length, size - offset);
            await file.ReadExactlyAsync(buffer.AsMemory(0, length), ct).ConfigureAwait(false);
            hash.AppendData(buffer.AsSpan(0, length));
        }
        return Convert.ToHexStringLower(hash.GetHashAndReset());
    }

    public static void Validate(UploadJob job)
    {
        if (!Protocol.IsIdentifier(job.Id) || !Protocol.IsIdentifier(job.Folder) || job.Size < 0 || job.Offset < 0 ||
            job.Offset > job.Size || !Path.IsPathFullyQualified(job.Source) || job.Name != Path.GetFileName(job.Source) ||
            job.Fingerprint.Length != 64 || !job.Fingerprint.All(Uri.IsHexDigit) ||
            (job.Upload.Length != 0 && (!Protocol.IsIdentifier(job.FileId) || Protocol.UploadPath(job.Upload) != job.Upload)))
            throw new ClientException(Localization.Text("Некорректное состояние загрузки"));
        if (job.Directory is not null) ValidateDirectory(job);
    }

    private static void CheckSource(FileStream file, UploadJob job)
    {
        if (file.Length != job.Size || File.GetLastWriteTimeUtc(job.Source).Ticks != job.ModifiedTicks)
            throw new ClientException(Localization.Text("Исходный файл изменился. Отмените загрузку и добавьте файл заново"));
    }

    private static bool Retryable(Exception error) => error switch
    {
        ApiException api => api.Status == 429 || api.Status >= 500 && api.Status != 507,
        ClientException client => client.Retryable,
        HttpRequestException => true,
        OperationCanceledException => true, // A request timeout is retryable; caller cancellation is checked separately.
        _ => false
    };

    private Task DelayAsync(Exception? error, int attempt, CancellationToken ct)
    {
        var seconds = Math.Min(1 << attempt, 8);
        var retryAfter = error is ApiException api ? api.RetryAfter.TotalSeconds : 0;
        return _delay(TimeSpan.FromSeconds(Math.Max(seconds, retryAfter)), ct);
    }
    private async Task<T> RetryAsync<T>(Func<Task<T>> action, CancellationToken ct)
    {
        for (var attempt = 0; ; attempt++)
        {
            ct.ThrowIfCancellationRequested();
            try { return await action().ConfigureAwait(false); }
            catch (Exception error) when (!ct.IsCancellationRequested && Retryable(error) && attempt < 4)
            { await DelayAsync(error, attempt, ct).ConfigureAwait(false); }
        }
    }

    public async Task<UploadJob> UploadAsync(IShareFileApi api, UploadJob job,
        Func<UploadJob, CancellationToken, Task> checkpoint, CancellationToken ct)
    {
        job = job with { Transferred = 0 };
        Validate(job);
        if (job.Directory is not null) return await UploadDirectoryAsync(api, job, checkpoint, ct).ConfigureAwait(false);
        return await UploadFileAsync(api, job, checkpoint, ct).ConfigureAwait(false);
    }

    private async Task<UploadJob> UploadFileAsync(IShareFileApi api, UploadJob job,
        Func<UploadJob, CancellationToken, Task> checkpoint, CancellationToken ct, string? directoryRoot = null)
    {
        job = job with { Transferred = 0 };
        await using var file = directoryRoot is null ? Open(job.Source) : OpenDirectoryFile(directoryRoot, job.Source);
        CheckSource(file, job);
        if (await FingerprintAsync(file, job.Size, job.ModifiedTicks, ct).ConfigureAwait(false) != job.Fingerprint)
            throw new ClientException(Localization.Text("Исходный файл изменился. Отмените загрузку и добавьте файл заново"));
        var folder = await api.NodeAsync(job.Folder, ct).ConfigureAwait(false);
        if (folder.Kind != "folder" || folder.Role < 3) throw new ClientException(Localization.Text("Для загрузки и ссылки нужны права управляющего"));
        if (job.Upload.Length == 0)
        {
            job = await RetryAsync(() => api.CreateUploadAsync(job, ct), ct).ConfigureAwait(false);
            await checkpoint(job, ct).ConfigureAwait(false);
        }
        var failures = 0;
        var stalled = 0;
        var reconciled = false;
        while (true)
        {
            ct.ThrowIfCancellationRequested();
            var head = await RetryAsync(() => api.HeadAsync(job.Upload, ct), ct).ConfigureAwait(false);
            if (head.Length != job.Size || head.FileId != job.FileId || head.Offset < 0 || head.Offset > job.Size || head.ChunkSize < 1)
                throw new ClientException(Localization.Text("Состояние загрузки не соответствует исходному файлу"));
            var previous = job.Offset;
            job = job with { Offset = head.Offset, Transferred = job.Transferred + (reconciled ? Math.Max(0, head.Offset - previous) : 0) };
            reconciled = true;
            await checkpoint(job, ct).ConfigureAwait(false);
            if (head.State == "complete")
            {
                if (head.Offset != job.Size) throw new ClientException(Localization.Text("Сервер не подтвердил завершение файла"));
                CheckSource(file, job);
                return job;
            }
            if (head.State == "committing")
            {
                if (++stalled > 60) throw new ClientException(Localization.Text("Сервер пока не завершил файл. Продолжите позже"));
                await _delay(TimeSpan.FromSeconds(2), ct).ConfigureAwait(false);
                continue;
            }
            if (head.State != "open") throw new ClientException(Localization.Text("Некорректное состояние загрузки"));
            // Bound even a broken server that returns success without advancing its durable offset.
            if (head.Offset == previous && ++stalled > 8) throw new ClientException(Localization.Text("Сервер не подтверждает передачу. Продолжите позже"));
            if (head.Offset != previous) stalled = 0;
            CheckSource(file, job);
            file.Position = head.Offset;
            try
            {
                await api.PatchAsync(job.Upload, head.Offset, file, Math.Min(head.ChunkSize, job.Size - head.Offset), ct).ConfigureAwait(false);
                failures = 0;
            }
            catch (Exception error) when (!ct.IsCancellationRequested &&
                (Retryable(error) || error is ApiException { Status: 409 }) && failures < 4)
            { await DelayAsync(error, failures++, ct).ConfigureAwait(false); }
            // Never replay a chunk blindly. HEAD recovers partial writes and lost final responses.
        }
    }

    public async Task CancelAsync(IShareFileApi api, UploadJob job, CancellationToken ct)
    {
        Validate(job);
        if (job.Directory is not null) { await CancelDirectoryAsync(api, job, ct).ConfigureAwait(false); return; }
        if (job.Upload.Length == 0) job = await RetryAsync(() => api.CreateUploadAsync(job, ct), ct).ConfigureAwait(false);
        await RetryAsync(async () => { await api.CancelUploadAsync(job.Upload, ct).ConfigureAwait(false); return true; }, ct).ConfigureAwait(false);
    }
}
