using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Microsoft.Win32.SafeHandles;

namespace ShareFile.Core;

public sealed partial class UploadEngine
{
    private static bool ValidName(string name) => name.Length != 0 && name.Trim() == name &&
        name is not ("." or "..") && Encoding.UTF8.GetByteCount(name) <= 240 &&
        !name.Any(c => char.IsControl(c) || c is '/' or '\\');
    private static string ParentPath(string path) => path.Contains('/') ? path[..path.LastIndexOf('/')] : "";
    private static string Leaf(string path) => path[(path.LastIndexOf('/') + 1)..];

    private static void CheckDirectoryChain(string root, string source)
    {
        var relative = Path.GetRelativePath(root, source);
        if (relative == ".." || relative.StartsWith(".." + Path.DirectorySeparatorChar, StringComparison.Ordinal) || Path.IsPathRooted(relative))
            throw new ClientException("Файл находится вне загружаемой папки");
        // Reject links and junctions, including a changed ancestor, before opening files.
        for (string? current = source; current is not null; current = Path.GetDirectoryName(current))
        {
            if ((File.GetAttributes(current) & (FileAttributes.ReparsePoint | FileAttributes.Device)) != 0)
                throw new ClientException("Папка содержит символическую ссылку или специальный файл");
            if (!OperatingSystem.IsWindows() && current == root) break;
            if (current == Path.GetPathRoot(current)) break;
        }
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern uint GetFinalPathNameByHandle(SafeFileHandle handle, StringBuilder path, uint length, uint flags);

    private static FileStream OpenDirectoryFile(string root, string source)
    {
        CheckDirectoryChain(root, source);
        var file = Open(source);
        try
        {
            CheckDirectoryChain(root, source);
            if (OperatingSystem.IsWindows())
            {
                // Check the opened handle too: a parent can become a junction during open.
                var buffer = new StringBuilder(32768);
                var length = GetFinalPathNameByHandle(file.SafeFileHandle, buffer, (uint)buffer.Capacity, 0);
                if (length == 0 || length >= buffer.Capacity) throw new ClientException("Не удалось проверить путь файла");
                var actual = buffer.ToString();
                actual = actual.StartsWith(@"\\?\UNC\", StringComparison.Ordinal) ? @"\\" + actual[8..] :
                    actual.StartsWith(@"\\?\", StringComparison.Ordinal) ? actual[4..] : actual;
                if (!actual.Equals(Path.GetFullPath(source), StringComparison.OrdinalIgnoreCase))
                    throw new ClientException("Путь файла изменился во время загрузки");
            }
            return file;
        }
        catch { file.Dispose(); throw; }
    }

    private static async Task<UploadJob> PrepareDirectoryAsync(string source, string parent, CancellationToken ct)
    {
        if (!ValidName(Path.GetFileName(source))) throw new ClientException("Недопустимое имя папки");
        CheckDirectoryChain(source, source);
        var folders = new List<DirectoryFolder>(); var files = new List<DirectoryFile>();
        long size = 0;
        using var hash = IncrementalHash.CreateHash(HashAlgorithmName.SHA256);
        async Task WalkAsync(string directory)
        {
            foreach (var entry in System.IO.Directory.EnumerateFileSystemEntries(directory).Order(StringComparer.Ordinal))
            {
                ct.ThrowIfCancellationRequested();
                CheckDirectoryChain(source, entry);
                var path = Path.GetRelativePath(source, entry).Replace(Path.DirectorySeparatorChar, '/');
                if (!ValidName(Path.GetFileName(entry)) || path.Split('/').Length > 128)
                    throw new ClientException("Недопустимое имя или слишком глубокая вложенность папки");
                if (folders.Count + files.Count >= 10000) throw new ClientException("В папке больше 10000 объектов");
                if ((File.GetAttributes(entry) & FileAttributes.Directory) != 0)
                {
                    folders.Add(new DirectoryFolder(path, NewId()));
                    hash.AppendData(Encoding.UTF8.GetBytes("d\0" + path + "\0"));
                    await WalkAsync(entry).ConfigureAwait(false);
                }
                else
                {
                    await using var file = OpenDirectoryFile(source, entry);
                    var ticks = File.GetLastWriteTimeUtc(entry).Ticks;
                    var child = new UploadJob { Id = NewId(), Source = entry, Name = Path.GetFileName(entry), Folder = parent,
                        Size = file.Length, ModifiedTicks = ticks, Fingerprint = await FingerprintAsync(file, file.Length, ticks, ct).ConfigureAwait(false) };
                    if (child.Size > long.MaxValue - size) throw new ClientException("Суммарный размер папки слишком велик");
                    size += child.Size;
                    files.Add(new DirectoryFile(path, child));
                    hash.AppendData(Encoding.UTF8.GetBytes("f\0" + path + "\0" + child.Fingerprint + "\0"));
                }
            }
        }
        await WalkAsync(source).ConfigureAwait(false);
        var manifest = new DirectoryManifest(folders.ToArray(), files.ToArray());
        if (JsonSerializer.SerializeToUtf8Bytes(manifest).Length > 8 * 1024 * 1024)
            throw new ClientException("Структура папки слишком большая. Разделите её на несколько папок");
        return new UploadJob { Id = NewId(), Source = source, Name = Path.GetFileName(source), Folder = parent,
            Size = size, Fingerprint = Convert.ToHexStringLower(hash.GetHashAndReset()), Directory = manifest };
    }

    private static void ValidateDirectory(UploadJob job)
    {
        var manifest = job.Directory!;
        if (manifest.Folders is null || manifest.Files is null || job.Upload.Length != 0 ||
            !ValidName(job.Name) || (job.FileId.Length != 0 && !Protocol.IsIdentifier(job.FileId)) ||
            manifest.Folders.Count + manifest.Files.Count > 10000)
            throw new ClientException("Некорректная структура папки");
        var parents = new HashSet<string>(StringComparer.Ordinal) { "" };
        var paths = new HashSet<string>(StringComparer.Ordinal);
        var keys = new HashSet<string>(StringComparer.Ordinal) { job.Id };
        void CheckPath(string path)
        {
            if (path.Split('/').Length > 128 || !path.Split('/').All(ValidName) ||
                !parents.Contains(ParentPath(path)) || !paths.Add(path)) throw new ClientException("Некорректный путь внутри папки");
        }
        foreach (var folder in manifest.Folders)
        {
            CheckPath(folder.Path);
            if (!Protocol.IsIdentifier(folder.Key) || !keys.Add(folder.Key) || (folder.Id.Length != 0 && !Protocol.IsIdentifier(folder.Id)))
                throw new ClientException("Некорректный ключ загрузки папки");
            parents.Add(folder.Path);
        }
        long size = 0;
        foreach (var entry in manifest.Files)
        {
            CheckPath(entry.Path);
            var child = entry.Job;
            if (child.Directory is not null || !keys.Add(child.Id) || child.Source != Path.Combine(job.Source, entry.Path.Replace('/', Path.DirectorySeparatorChar)))
                throw new ClientException("Некорректное состояние файла в папке");
            Validate(child);
            if (child.Size > long.MaxValue - size || (entry.Complete && (child.FileId.Length == 0 || child.Offset != child.Size)))
                throw new ClientException("Некорректный размер файла в папке");
            size += child.Size;
        }
        // Leave room for resource IDs and upload paths added to the initial 8 MiB manifest.
        if (size != job.Size || JsonSerializer.SerializeToUtf8Bytes(manifest).Length > 16 * 1024 * 1024)
            throw new ClientException("Некорректный размер структуры папки");
    }

    private async Task<Node> EnsureFolderAsync(IShareFileApi api, string id, string parent, string name, string key, CancellationToken ct)
    {
        var node = id.Length == 0 ? await RetryAsync(() => api.CreateFolderAsync(parent, name, key, ct), ct).ConfigureAwait(false)
            : await api.NodeAsync(id, ct).ConfigureAwait(false);
        if (!Protocol.IsIdentifier(node.Id) || node.Kind != "folder" || node.ParentId != parent || node.Role < 3)
            throw new ClientException("Загружаемая папка перемещена или недоступна. Проверьте её в Web UI");
        return node;
    }

    private async Task<UploadJob> UploadDirectoryAsync(IShareFileApi api, UploadJob job,
        Func<UploadJob, CancellationToken, Task> checkpoint, CancellationToken ct)
    {
        var fresh = await PrepareDirectoryAsync(job.Source, job.Folder, ct).ConfigureAwait(false);
        if (fresh.Fingerprint != job.Fingerprint || !fresh.Directory!.Folders.Select(f => f.Path).SequenceEqual(job.Directory!.Folders.Select(f => f.Path)) ||
            !fresh.Directory.Files.Select(f => (f.Path, f.Job.Fingerprint)).SequenceEqual(job.Directory.Files.Select(f => (f.Path, f.Job.Fingerprint))))
            throw new ClientException("Исходная папка изменилась. Отмените загрузку и добавьте папку заново");
        var parent = await api.NodeAsync(job.Folder, ct).ConfigureAwait(false);
        if (parent.Kind != "folder" || parent.Role < 3) throw new ClientException("Для загрузки и ссылки нужны права управляющего");
        var root = await EnsureFolderAsync(api, job.FileId, job.Folder, job.Name, job.Id, ct).ConfigureAwait(false);
        job = job with { FileId = root.Id };
        await checkpoint(job, ct).ConfigureAwait(false);
        var folders = job.Directory!.Folders.ToArray(); var files = job.Directory.Files.Select(f => f with { Job = f.Job with { Transferred = 0 } }).ToArray();
        var ids = new Dictionary<string, string>(StringComparer.Ordinal) { [""] = root.Id };
        for (var i = 0; i < folders.Length; i++)
        {
            var entry = folders[i];
            var node = await EnsureFolderAsync(api, entry.Id, ids[ParentPath(entry.Path)], Leaf(entry.Path), entry.Key, ct).ConfigureAwait(false);
            folders[i] = entry with { Id = node.Id }; ids[entry.Path] = node.Id;
            job = job with { Directory = new DirectoryManifest(folders.ToArray(), files.ToArray()) };
            await checkpoint(job, ct).ConfigureAwait(false);
        }
        async Task SaveFileAsync(int index, UploadJob child, bool complete, CancellationToken token)
        {
            files[index] = files[index] with { Job = child, Complete = complete };
            job = job with { Offset = files.Sum(f => f.Job.Offset), Transferred = files.Sum(f => f.Job.Transferred), Directory = new DirectoryManifest(folders.ToArray(), files.ToArray()) };
            await checkpoint(job, token).ConfigureAwait(false);
        }
        for (var i = 0; i < files.Length; i++)
        {
            var index = i; var entry = files[i]; var folder = ids[ParentPath(entry.Path)];
            if (entry.Complete || entry.Job.Upload.Length != 0)
            {
                if (entry.Job.Folder != folder) throw new ClientException("Папка файла изменилась на сервере");
            }
            if (entry.Complete)
            {
                var node = await api.NodeAsync(entry.Job.FileId, ct).ConfigureAwait(false);
                if (node.Kind != "file" || node.ParentId != folder) throw new ClientException("Загружаемый файл перемещён или удалён");
                continue;
            }
            var child = entry.Job with { Folder = folder };
            var result = await UploadFileAsync(api, child, (progress, token) => SaveFileAsync(index, progress, false, token), ct, job.Source).ConfigureAwait(false);
            await SaveFileAsync(index, result, true, ct).ConfigureAwait(false);
        }
        return job;
    }

    private async Task CancelDirectoryAsync(IShareFileApi api, UploadJob job, CancellationToken ct)
    {
        var root = await EnsureFolderAsync(api, job.FileId, job.Folder, job.Name, job.Id, ct).ConfigureAwait(false);
        var ids = new Dictionary<string, string>(StringComparer.Ordinal) { [""] = root.Id };
        foreach (var folder in job.Directory!.Folders)
            if (folder.Id.Length != 0) ids[folder.Path] = folder.Id;
        foreach (var entry in job.Directory.Files)
            if (!entry.Complete && ids.TryGetValue(ParentPath(entry.Path), out var folder))
                await CancelAsync(api, entry.Job with { Folder = folder }, ct).ConfigureAwait(false);
        await RetryAsync(async () => { await api.TrashNodeAsync(root.Id, ct).ConfigureAwait(false); return true; }, ct).ConfigureAwait(false);
    }
}
