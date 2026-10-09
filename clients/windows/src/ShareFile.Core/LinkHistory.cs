using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace ShareFile.Core;

public interface ILinkSecrets
{
    string? Read(string account);
    void Write(string account, string value);
    void Delete(string account);
}

public sealed record SentLink(string Origin, string Owner, string ShareId, string JobId, string Name,
    DateTimeOffset Created, DateTimeOffset? Expires, bool Revoked = false)
{
    public string Account => "history:" + Convert.ToHexStringLower(SHA256.HashData(Encoding.UTF8.GetBytes(Origin + "\n" + Owner + "\n" + ShareId)));
    public bool Active => !Revoked && (Expires is null || Expires > DateTimeOffset.UtcNow);
    public string Status => Revoked ? "Отозвана" : !Active ? "Срок истёк" : Expires is { } until ? "До " + until.LocalDateTime.ToString("g") : "Бессрочно";
    public string CreatedLabel => Created.LocalDateTime.ToString("g");
}

// Public URLs are written exclusively to the OS secret store, one capability per credential.
// The independent index survives clearing the upload queue and contains only metadata.
public sealed class LinkHistory(string path, ILinkSecrets secrets)
{
    public const int Limit = 200;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web);
    private bool _available = true;
    private SentLink[] _entries = [];
    public IReadOnlyList<SentLink> Entries => _entries;
    public event Action? Changed;
    public IReadOnlyList<SentLink> ForAccount(string origin, string owner) => _entries.Where(e => e.Origin == origin && e.Owner == owner).ToArray();

    public async Task LoadAsync(CancellationToken ct = default)
    {
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (!File.Exists(path)) return;
            if (new FileInfo(path).Length > 2 * 1024 * 1024) throw new ClientException("История слишком большая");
            await using var file = File.OpenRead(path);
            var entries = await JsonSerializer.DeserializeAsync<SentLink[]>(file, Json, ct).ConfigureAwait(false);
            if (entries is null || entries.Length > Limit) throw new ClientException("Некорректная история");
            foreach (var entry in entries)
            {
                if (entry is null || Protocol.NormalizeOrigin(entry.Origin) != entry.Origin ||
                    !Protocol.IsIdentifier(entry.Owner) || !Protocol.IsIdentifier(entry.ShareId) ||
                    !Protocol.IsIdentifier(entry.JobId) || entry.Name is null || entry.Name.Length > 1024 ||
                    entry.Created <= DateTimeOffset.UnixEpoch || entry.Expires < DateTimeOffset.UnixEpoch)
                    throw new ClientException("Некорректная история");
            }
            if (entries.Select(e => e.Account).Distinct().Count() != entries.Length) throw new ClientException("Повторяющиеся записи истории");
            _entries = entries;
        }
        catch (Exception error) when (error is JsonException or ClientException or IOException or UnauthorizedAccessException or ArgumentException)
        {
            _available = false;
            throw new ClientException("Не удалось прочитать history.json. История сохранена для восстановления");
        }
        finally { _gate.Release(); Changed?.Invoke(); }
    }

    private async Task SaveAsync(SentLink[] entries)
    {
        // Computed Account/Active/Status fields are never serialized into the metadata index.
        var metadata = entries.Select(e => new { e.Origin, e.Owner, e.ShareId, e.JobId, e.Name, e.Created, e.Expires, e.Revoked });
        var directory = Path.GetDirectoryName(Path.GetFullPath(path))!;
        Directory.CreateDirectory(directory);
        var temporary = Path.Combine(directory, ".history-" + Guid.NewGuid().ToString("N"));
        try
        {
            await using (var file = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None,
                16384, FileOptions.Asynchronous | FileOptions.WriteThrough))
            {
                await JsonSerializer.SerializeAsync(file, metadata, Json).ConfigureAwait(false);
                await file.FlushAsync().ConfigureAwait(false);
                file.Flush(flushToDisk: true);
            }
            File.Move(temporary, path, overwrite: true);
        }
        finally { if (File.Exists(temporary)) File.Delete(temporary); }
    }

    public async Task RecordAsync(ClientSettings settings, UploadJob job, ShareLink link)
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            if (!_available) throw new ClientException("История недоступна");
            Protocol.ValidateLink(link);
            Protocol.RequireIdentifier(settings.Owner);
            Protocol.RequireIdentifier(job.Id);
            var created = link.Share.CreatedAt > 0 ? DateTimeOffset.FromUnixTimeSeconds(link.Share.CreatedAt) : DateTimeOffset.UtcNow;
            var entry = new SentLink(settings.Origin, settings.Owner, link.Share.Id, job.Id, job.Name, created,
                link.Share.ExpiresAt is { } expiry ? DateTimeOffset.FromUnixTimeSeconds(expiry) : job.Never ? null : created.AddDays(job.Days));
            await Task.Run(() => secrets.Write(entry.Account, link.Url)).ConfigureAwait(false);
            var previous = _entries.Where(e => e.Account != entry.Account).ToArray();
            var next = new[] { entry }.Concat(previous).Take(Limit).ToArray();
            try { await SaveAsync(next).ConfigureAwait(false); }
            catch
            {
                if (!_entries.Any(e => e.Account == entry.Account))
                {
                    try { await Task.Run(() => secrets.Delete(entry.Account)).ConfigureAwait(false); }
                    catch { /* Preserve the original index failure; no plaintext fallback. */ }
                }
                throw;
            }
            _entries = next;
            foreach (var removed in previous.Skip(Limit - 1))
                await Task.Run(() => secrets.Delete(removed.Account)).ConfigureAwait(false);
        }
        finally { _gate.Release(); Changed?.Invoke(); }
    }

    public async Task<string> GetLinkAsync(SentLink entry)
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            if (!_entries.Any(e => e.Account == entry.Account && e.Active)) throw new ClientException("Ссылка отозвана или срок действия истёк");
            var value = await Task.Run(() => secrets.Read(entry.Account)).ConfigureAwait(false);
            if (string.IsNullOrEmpty(value)) throw new ClientException("Ссылка недоступна в Windows Credential Manager");
            Protocol.ValidateLink(new ShareLink(value, new ShareIdentity(entry.ShareId)));
            return value;
        }
        finally { _gate.Release(); }
    }

    public async Task MarkRevokedAsync(string origin, string owner, string shareId)
    {
        await _gate.WaitAsync().ConfigureAwait(false);
        try
        {
            var entry = _entries.FirstOrDefault(e => e.Origin == origin && e.Owner == owner && e.ShareId == shareId);
            if (entry is null) return;
            _entries = _entries.Select(e => e.Account == entry.Account ? e with { Revoked = true } : e).ToArray();
            try { await SaveAsync(_entries).ConfigureAwait(false); }
            finally { await Task.Run(() => secrets.Delete(entry.Account)).ConfigureAwait(false); }
        }
        finally { _gate.Release(); Changed?.Invoke(); }
    }
}
