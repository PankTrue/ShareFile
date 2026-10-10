using System.Text;
using ShareFile.Core;

namespace ShareFile.Core.Tests;

public sealed class TransferTests : IDisposable
{
    private readonly string _directory = Path.Combine(Path.GetTempPath(), "sharefile-csharp-" + Guid.NewGuid().ToString("N"));
    public TransferTests() => Directory.CreateDirectory(_directory);
    public void Dispose() => Directory.Delete(_directory, true);
    private async Task<UploadJob> JobAsync(byte[]? bytes = null)
    {
        var path = Path.Combine(_directory, "Отчёт 日本語.bin");
        await File.WriteAllBytesAsync(path, bytes ?? Enumerable.Range(0, 2500).Select(i => (byte)i).ToArray());
        return await UploadEngine.PrepareAsync(path, FakeApi.Folder, TestContext.Current.CancellationToken);
    }
    private static UploadEngine Engine() => new((_, ct) => { ct.ThrowIfCancellationRequested(); return Task.CompletedTask; });

    [Fact]
    public async Task LostCreateAndPartialPatchResponsesResumeFromDurableOffsetWithoutDuplicate()
    {
        var job = await JobAsync();
        var api = new FakeApi { LoseCreate = true, LosePatch = true };
        var checkpoints = new List<UploadJob>();
        var result = await Engine().UploadAsync(api, job, (j, _) => { checkpoints.Add(j); return Task.CompletedTask; }, TestContext.Current.CancellationToken);
        Assert.Equal(job.Size, result.Offset);
        Assert.Equal(job.Size, result.Transferred);
        Assert.Equal(1, api.CreatedObjects);
        Assert.All(api.IdempotencyKeys, key => Assert.Equal(job.Id, key));
        Assert.Equal(await File.ReadAllBytesAsync(job.Source, TestContext.Current.CancellationToken), api.Bytes.ToArray());
        Assert.Contains(checkpoints, j => j.Offset == 127);
        Assert.Equal(new long[] { 0, 127, 1151, 2175 }, api.PatchOffsets);
    }

    [Fact]
    public async Task CancellationCheckpointCanBeResumedByAnotherEngine()
    {
        var job = await JobAsync(); var api = new FakeApi();
        using var stop = CancellationTokenSource.CreateLinkedTokenSource(TestContext.Current.CancellationToken);
        var saved = job;
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => Engine().UploadAsync(api, job,
            (j, _) => { saved = j; if (j.Offset > 0) stop.Cancel(); return Task.CompletedTask; }, stop.Token));
        Assert.True(saved.Offset > 0);
        var resumed = await Engine().UploadAsync(api, saved, (_, _) => Task.CompletedTask, TestContext.Current.CancellationToken);
        Assert.Equal(job.Size, resumed.Offset);
        Assert.Equal(job.Size - saved.Offset, resumed.Transferred);
        Assert.Equal(1, api.CreatedObjects);
    }

    [Fact]
    public async Task SourceReplacementIsRejectedEvenWhenSizeAndTimestampArePreserved()
    {
        var job = await JobAsync(); var stamp = File.GetLastWriteTimeUtc(job.Source);
        await File.WriteAllBytesAsync(job.Source, new byte[job.Size], TestContext.Current.CancellationToken); File.SetLastWriteTimeUtc(job.Source, stamp);
        var api = new FakeApi();
        await Assert.ThrowsAsync<ClientException>(() => Engine().UploadAsync(api, job, (_, _) => Task.CompletedTask, TestContext.Current.CancellationToken));
        Assert.Equal(0, api.CreatedObjects);
    }

    [Fact]
    public async Task EmptyFileCompletes()
    {
        var job = await JobAsync([]); var api = new FakeApi();
        var result = await Engine().UploadAsync(api, job, (_, _) => Task.CompletedTask, TestContext.Current.CancellationToken);
        Assert.Equal(0, result.Offset); Assert.Empty(api.PatchOffsets);
        Assert.Equal(0, result.Transferred);
    }

    [Fact]
    public async Task OffsetsAboveFourGiBAreNotTruncated()
    {
        var path = Path.Combine(_directory, "sparse.bin");
        await using (var file = File.Create(path)) file.SetLength(5L * 1024 * 1024 * 1024 + 17);
        var job = await UploadEngine.PrepareAsync(path, FakeApi.Folder, TestContext.Current.CancellationToken);
        var api = new FakeApi { Offset = job.Size - 17, RecordBytes = false };
        api.Size = job.Size;
        job = job with { Upload = FakeApi.Upload, FileId = FakeApi.FileId };
        var result = await Engine().UploadAsync(api, job, (_, _) => Task.CompletedTask, TestContext.Current.CancellationToken);
        Assert.Equal(job.Size, result.Offset); Assert.Equal(job.Size - 17, Assert.Single(api.PatchOffsets));
    }

    [Theory]
    [InlineData(401)]
    [InlineData(403)]
    [InlineData(507)]
    public async Task PermanentFailuresDoNotRetry(int status)
    {
        var job = await JobAsync(); var api = new FakeApi { CreateError = status };
        await Assert.ThrowsAsync<ApiException>(() => Engine().UploadAsync(api, job, (_, _) => Task.CompletedTask, TestContext.Current.CancellationToken));
        Assert.Single(api.IdempotencyKeys);
    }

    [Fact]
    public async Task CheckpointFailureStopsBeforeFileDataIsSent()
    {
        var job = await JobAsync(); var api = new FakeApi();
        await Assert.ThrowsAsync<IOException>(() => Engine().UploadAsync(api, job,
            (_, _) => throw new IOException("disk full"), TestContext.Current.CancellationToken));
        Assert.Empty(api.PatchOffsets);
    }

    [Fact]
    public async Task QueueCopiesWholeBatchOnceAndNeverPersistsCapabilities()
    {
        var job = await JobAsync(); var second = Path.Combine(_directory, "two.txt"); await File.WriteAllTextAsync(second, "two", TestContext.Current.CancellationToken);
        var store = new JsonStateStore(Path.Combine(_directory, "state.json"));
        await using var queue = new UploadQueue(store, Engine());
        var completions = new List<BatchCompletion>(); queue.Completed += completions.Add;
        await queue.LoadAsync(TestContext.Current.CancellationToken); await queue.AttachAsync(new FakeApi(), "password-test-only", TestContext.Current.CancellationToken);
        await queue.AddAsync([job.Source, second], TestContext.Current.CancellationToken); await queue.WaitForIdleAsync(TestContext.Current.CancellationToken);
        var completion = Assert.Single(completions);
        Assert.Equal(2, completion.Links.Count); Assert.All(queue.State.Jobs, j => Assert.Equal(UploadStatus.Done, j.Status));
        var json = await File.ReadAllTextAsync(Path.Combine(_directory, "state.json"), TestContext.Current.CancellationToken);
        Assert.DoesNotContain("/s/", json); Assert.DoesNotContain("password-test-only", json);
        var reloaded = await store.LoadAsync(TestContext.Current.CancellationToken); Assert.All(reloaded.Jobs, j => Assert.Empty(j.Link));
    }

    [Fact]
    public async Task LostShareResponseIsUncertainAndIsNotAutomaticallyRepeated()
    {
        var job = await JobAsync(); var store = new MemoryStore(); var api = new FakeApi { LoseShare = true };
        await using var queue = new UploadQueue(store, Engine());
        await queue.LoadAsync(TestContext.Current.CancellationToken); await queue.AttachAsync(api, "", TestContext.Current.CancellationToken); await queue.AddAsync([job.Source], TestContext.Current.CancellationToken); await queue.WaitForIdleAsync(TestContext.Current.CancellationToken);
        Assert.Equal(1, api.ShareCalls); Assert.Equal(UploadStatus.Uncertain, Assert.Single(queue.State.Jobs).Status);
        Assert.Contains(store.Writes, state => state.Jobs.Any(j => j.Status == UploadStatus.Sharing));
        await Assert.ThrowsAsync<ClientException>(() => queue.AttachAsync(new FakeApi { Origin = "https://other.example" }, "", TestContext.Current.CancellationToken));
        await Assert.ThrowsAsync<ClientException>(() => queue.AttachAsync(new FakeApi { Owner = new string('z', 43) }, "", TestContext.Current.CancellationToken));
    }

    [Fact]
    public async Task FailedSelectionProducesOneFailureNotificationForTheWholeBatch()
    {
        var job = await JobAsync();
        var second = Path.Combine(_directory, "second.txt");
        await File.WriteAllTextAsync(second, "two", TestContext.Current.CancellationToken);
        await using var queue = new UploadQueue(new MemoryStore(), Engine());
        var failures = new List<BatchFailure>();
        queue.BatchFailed += failures.Add;
        await queue.LoadAsync(TestContext.Current.CancellationToken);
        await queue.AttachAsync(new FakeApi { LoseShare = true }, "", TestContext.Current.CancellationToken);
        await queue.AddAsync([job.Source, second], TestContext.Current.CancellationToken);
        await queue.WaitForIdleAsync(TestContext.Current.CancellationToken);
        Assert.Equal(new BatchFailure(2, 2, true), Assert.Single(failures));
    }

    [Fact]
    public async Task RestartPausesTransfersAndMarksInFlightSharesUncertain()
    {
        var job = await JobAsync();
        var store = new MemoryStore
        {
            Saved = new ClientState
            {
                Jobs = [job with { Status = UploadStatus.Uploading },
            job with { Id = UploadEngine.NewId(), Status = UploadStatus.Sharing }]
            }
        };
        await using var queue = new UploadQueue(store, Engine()); await queue.LoadAsync(TestContext.Current.CancellationToken);
        Assert.Equal(UploadStatus.Paused, queue.State.Jobs[0].Status);
        Assert.Equal(UploadStatus.Uncertain, queue.State.Jobs[1].Status);
    }

    [Fact]
    public async Task CancellationFailureKeepsTheCheckpoint()
    {
        var job = await JobAsync(); var api = new FakeApi { CancelError = true };
        var store = new MemoryStore { Saved = new ClientState { Settings = new ClientSettings { Origin = api.Origin, Owner = api.Owner }, Jobs = [job] } };
        await using var queue = new UploadQueue(store, Engine()); await queue.LoadAsync(TestContext.Current.CancellationToken); await queue.AttachAsync(api, "", TestContext.Current.CancellationToken);
        await Assert.ThrowsAsync<ApiException>(() => queue.RemoveAsync(job.Id, TestContext.Current.CancellationToken)); Assert.Single(queue.State.Jobs);
    }

    [Fact]
    public async Task QueueDoesNotStartNetworkOperationsWhenStateCannotBeSaved()
    {
        var job = await JobAsync(); var store = new MemoryStore(); var api = new FakeApi();
        await using var queue = new UploadQueue(store, Engine()); await queue.LoadAsync(TestContext.Current.CancellationToken); await queue.AttachAsync(api, "", TestContext.Current.CancellationToken);
        store.Fail = true;
        await Assert.ThrowsAsync<IOException>(() => queue.AddAsync([job.Source], TestContext.Current.CancellationToken)); Assert.Equal(0, api.CreatedObjects);
    }
}

internal sealed class MemoryStore : IStateStore
{
    public ClientState Saved { get; set; } = new();
    public List<ClientState> Writes { get; } = [];
    public bool Fail { get; set; }
    public Task<ClientState> LoadAsync(CancellationToken ct = default) => Task.FromResult(Saved);
    public Task SaveAsync(ClientState state, CancellationToken ct = default)
    { if (Fail) throw new IOException("disk full"); Writes.Add(state); Saved = state; return Task.CompletedTask; }
}

internal sealed class FakeApi : IShareFileApi
{
    public const string Folder = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    public const string FileId = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
    public const string Upload = "/api/v1/uploads/ccccccccccccccccccccccccccccccccccccccccccc";
    public string Origin { get; init; } = "https://sharefile.example";
    public string Owner { get; init; } = new('u', 43);
    public long Offset { get; set; }
    public long Size { get; set; }
    public bool LoseCreate { get; set; }
    public bool LosePatch { get; set; }
    public bool LoseShare { get; init; }
    public bool CancelError { get; init; }
    public bool RevokeError { get; init; }
    public int CreateError { get; init; }
    public int CreatedObjects { get; private set; }
    public int ShareCalls { get; private set; }
    public bool RecordBytes { get; init; } = true;
    public List<string> IdempotencyKeys { get; } = [];
    public List<long> PatchOffsets { get; } = [];
    public MemoryStream Bytes { get; } = new();
    private readonly HashSet<string> _created = [];
    public Task<Session> MeAsync(CancellationToken ct) => Task.FromResult(new Session(new User(Owner, "test", Folder)));
    public Task<IReadOnlyList<Node>> FoldersAsync(string parent, CancellationToken ct) => Task.FromResult<IReadOnlyList<Node>>([new Node(Folder, "Uploads", "folder", "", 3)]);
    public Task<Node> NodeAsync(string id, CancellationToken ct) => Task.FromResult(new Node(Folder, "Uploads", "folder", "", 3));
    public Task<Node> CreateFolderAsync(string parent, string name, string key, CancellationToken ct) => throw new NotSupportedException();
    public Task TrashNodeAsync(string id, CancellationToken ct) => throw new NotSupportedException();
    public Task<UploadJob> CreateUploadAsync(UploadJob job, CancellationToken ct)
    {
        IdempotencyKeys.Add(job.Id);
        if (CreateError != 0) throw new ApiException(CreateError);
        if (_created.Add(job.Id)) { CreatedObjects++; Size = job.Size; Offset = 0; Bytes.SetLength(0); }
        if (LoseCreate) { LoseCreate = false; throw new ClientException("Не удалось получить ответ", retryable: true); }
        return Task.FromResult(job with { Upload = Upload, FileId = FileId });
    }
    public Task<UploadHead> HeadAsync(string path, CancellationToken ct) => Task.FromResult(new UploadHead(Offset, Size, 1024, FileId, Offset == Size ? "complete" : "open"));
    public async Task PatchAsync(string path, long offset, Stream source, long count, CancellationToken ct)
    {
        ct.ThrowIfCancellationRequested(); PatchOffsets.Add(offset);
        if (offset != Offset) throw new ApiException(409);
        var bytes = new byte[LosePatch ? 127 : count]; await source.ReadExactlyAsync(bytes, ct);
        if (RecordBytes) await Bytes.WriteAsync(bytes, ct); Offset += bytes.Length;
        if (LosePatch) { LosePatch = false; throw new ClientException("Не удалось получить ответ", retryable: true); }
    }
    public Task CancelUploadAsync(string path, CancellationToken ct) => CancelError ? throw new ApiException(403) : Task.CompletedTask;
    public Task<ShareLink> ShareAsync(string fileId, ShareOptions options, CancellationToken ct)
    {
        ShareCalls++;
        if (LoseShare) throw new ClientException("Не удалось получить ответ", retryable: true);
        return Task.FromResult(new ShareLink(Origin + "/s/" + UploadEngine.NewId(), new ShareIdentity(UploadEngine.NewId())));
    }
    public Task RevokeShareAsync(string id, CancellationToken ct) => RevokeError ? throw new ApiException(403) : Task.CompletedTask;
}
