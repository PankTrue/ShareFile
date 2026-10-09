using System.Text.Json.Serialization;

namespace ShareFile.Core;

public sealed record User(string Id, string Username, [property: JsonPropertyName("home_id")] string HomeId);
public sealed record Session(User User);
public sealed record Node(string Id, string Name, string Kind,
    [property: JsonPropertyName("parent_id")] string? ParentId,
    [property: JsonPropertyName("effective_role")] int Role);
public sealed record PairRequest(string Id, string Secret,
    [property: JsonPropertyName("expires_at")] long ExpiresAt);
public sealed record Pairing(PairRequest Request,
    [property: JsonPropertyName("verification_url")] string VerificationUrl, int Interval);
public sealed record ShareIdentity(string Id, [property: JsonPropertyName("created_at")] long CreatedAt = 0,
    [property: JsonPropertyName("expires_at")] long? ExpiresAt = null);
public sealed record ShareLink(string Url, ShareIdentity Share);
public sealed record ShareOptions(int Days = 7, bool Never = false, string Password = "");
public sealed record UploadHead(long Offset, long Length, long ChunkSize, string FileId, string State);
public sealed record DirectoryFolder(string Path, string Key, string Id = "");
public sealed record DirectoryFile(string Path, UploadJob Job, bool Complete = false);
public sealed record DirectoryManifest(IReadOnlyList<DirectoryFolder> Folders, IReadOnlyList<DirectoryFile> Files);

public enum UploadStatus { Queued, Uploading, Paused, Uploaded, Sharing, Uncertain, Done, Error, Cancelling }

// Credentials and public capabilities are deliberately excluded from checkpoints.
public sealed record UploadJob
{
    public required string Id { get; init; }
    public required string Source { get; init; }
    public required string Name { get; init; }
    public long Size { get; init; }
    public long ModifiedTicks { get; init; }
    public required string Fingerprint { get; init; }
    public required string Folder { get; init; }
    public string Upload { get; init; } = "";
    public string FileId { get; init; } = "";
    public long Offset { get; init; }
    public DirectoryManifest? Directory { get; init; }
    public UploadStatus Status { get; init; } = UploadStatus.Queued;
    public string Message { get; init; } = "";
    public string Batch { get; init; } = "";
    public int Days { get; init; } = 7;
    public bool Never { get; init; }
    public string ShareId { get; init; } = "";
    [JsonIgnore] public string Link { get; init; } = "";
}

public sealed record ClientSettings
{
    public string Origin { get; init; } = "";
    public string Owner { get; init; } = "";
    public string Username { get; init; } = "";
    public string Folder { get; init; } = "";
    public string FolderName { get; init; } = "Uploads";
    public int Days { get; init; } = 7;
    public bool Never { get; init; }
    public bool SilentExplorerUploads { get; init; } = true;
    public bool NotifyUploads { get; init; } = true;
}

public sealed record ClientState
{
    public int Version { get; init; } = 1;
    public ClientSettings Settings { get; init; } = new();
    public IReadOnlyList<UploadJob> Jobs { get; init; } = [];
}

public sealed class ClientException(string message) : Exception(message);

public sealed class ApiException(int status, TimeSpan retryAfter = default) : Exception(status switch
{
    401 => "Подключение отозвано или истекло. Подключите компьютер заново",
    403 => "Недостаточно прав для этой операции",
    404 => "Объект не найден или срок загрузки истёк",
    409 => "Состояние файла изменилось. Попробуйте продолжить загрузку",
    429 => "Сервер занят. Повторите операцию позже",
    503 => "Хранилище сейчас недоступно",
    507 => "В хранилище недостаточно свободного места",
    _ => $"Сервер вернул HTTP {status}"
})
{
    public int Status { get; } = status;
    public TimeSpan RetryAfter { get; } = retryAfter;
}

public interface IShareFileApi
{
    string Origin { get; }
    Task<Session> MeAsync(CancellationToken cancellationToken);
    Task<IReadOnlyList<Node>> FoldersAsync(string parent, CancellationToken cancellationToken);
    Task<Node> NodeAsync(string id, CancellationToken cancellationToken);
    Task<Node> CreateFolderAsync(string parent, string name, string key, CancellationToken cancellationToken);
    Task TrashNodeAsync(string id, CancellationToken cancellationToken);
    Task<UploadJob> CreateUploadAsync(UploadJob job, CancellationToken cancellationToken);
    Task<UploadHead> HeadAsync(string path, CancellationToken cancellationToken);
    Task PatchAsync(string path, long offset, Stream source, long count, CancellationToken cancellationToken);
    Task CancelUploadAsync(string path, CancellationToken cancellationToken);
    Task<ShareLink> ShareAsync(string fileId, ShareOptions options, CancellationToken cancellationToken);
    Task RevokeShareAsync(string id, CancellationToken cancellationToken);
}
