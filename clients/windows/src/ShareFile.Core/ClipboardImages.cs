using System.Buffers.Binary;

namespace ShareFile.Core;

// Only sources created in this private spool are eligible for automatic deletion.
public sealed class ClipboardImages(string directory)
{
    public const int MaxBytes = 32 * 1024 * 1024;
    public const int MaxPixels = 25_000_000;
    private readonly string _directory = Path.GetFullPath(directory);
    private readonly HashSet<string> _preparing = new(StringComparer.OrdinalIgnoreCase);

    public async Task<string> WriteAsync(byte[] png, CancellationToken ct = default)
    {
        if (png.Length is < 33 or > MaxBytes || !png.AsSpan(0, 8).SequenceEqual(new byte[] { 137, 80, 78, 71, 13, 10, 26, 10 }) ||
            !png.AsSpan(12, 4).SequenceEqual("IHDR"u8))
            throw new ClientException(Localization.Text("Изображение повреждено или слишком большое (максимум 32 МиБ)"));
        var width = BinaryPrimitives.ReadUInt32BigEndian(png.AsSpan(16, 4));
        var height = BinaryPrimitives.ReadUInt32BigEndian(png.AsSpan(20, 4));
        if (width == 0 || height == 0 || (ulong)width * height > MaxPixels)
            throw new ClientException(Localization.Text("Изображение слишком большое (максимум 25 Мп)"));
        Directory.CreateDirectory(_directory);
        if ((File.GetAttributes(_directory) & FileAttributes.ReparsePoint) != 0)
            throw new ClientException(Localization.Text("Не удалось открыть каталог изображений"));
        var path = Path.Combine(_directory, $"Screenshot-{DateTime.Now:yyyyMMdd-HHmmss}-{Guid.NewGuid():N}.png");
        lock (_preparing) _preparing.Add(path);
        try
        {
            await using var file = new FileStream(path, FileMode.CreateNew, FileAccess.Write, FileShare.None, 65536, FileOptions.Asynchronous | FileOptions.WriteThrough);
            await file.WriteAsync(png, ct).ConfigureAwait(false);
            await file.FlushAsync(ct).ConfigureAwait(false);
            return path;
        }
        catch { Release(path); Remove(path); throw; }
    }

    public void Release(string path) { lock (_preparing) _preparing.Remove(path); }

    public void Remove(string path)
    {
        if (!string.Equals(Path.GetDirectoryName(Path.GetFullPath(path)), _directory, StringComparison.OrdinalIgnoreCase) ||
            !Path.GetFileName(path).StartsWith("Screenshot-", StringComparison.Ordinal) || Path.GetExtension(path) != ".png") return;
        try { File.Delete(path); }
        catch (IOException) { }
        catch (UnauthorizedAccessException) { }
    }

    public void Reconcile(IEnumerable<UploadJob> jobs, IEnumerable<string>? pending = null)
    {
        try { ReconcileFiles(jobs, pending); }
        catch (IOException) { }
        catch (UnauthorizedAccessException) { }
    }
    private void ReconcileFiles(IEnumerable<UploadJob> jobs, IEnumerable<string>? pending)
    {
        if (!Directory.Exists(_directory) || (File.GetAttributes(_directory) & FileAttributes.ReparsePoint) != 0) return;
        var keep = jobs.Where(j => j.Status is not (UploadStatus.Done or UploadStatus.Uploaded or UploadStatus.Uncertain))
            .Select(j => j.Source).Concat(pending ?? []).ToHashSet(StringComparer.OrdinalIgnoreCase);
        lock (_preparing)
        {
            keep.UnionWith(_preparing);
            foreach (var path in Directory.EnumerateFiles(_directory, "Screenshot-*.png"))
                if (!keep.Contains(path)) Remove(path);
        }
    }
}
