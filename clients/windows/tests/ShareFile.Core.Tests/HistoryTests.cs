using System.Text.Json;
using ShareFile.Core;

namespace ShareFile.Core.Tests;

public sealed class HistoryTests : IDisposable
{
    private readonly string _directory = Path.Combine(Path.GetTempPath(), "sharefile-history-" + Guid.NewGuid().ToString("N"));
    private readonly MemoryLinkSecrets _secrets = new();
    private string PathToHistory => Path.Combine(_directory, "history.json");
    private static readonly ClientSettings Settings = new() { Origin = "https://sharefile.example", Owner = new('u', 43) };
    private static UploadJob Job => new() { Id = UploadEngine.NewId(), Source = "/tmp/history.txt", Name = "Отчёт 日本語.txt", Folder = FakeApi.Folder, Fingerprint = new('f', 64) };
    private static ShareLink Link => new(Settings.Origin + "/s/" + UploadEngine.NewId(), new ShareIdentity(UploadEngine.NewId()));
    public HistoryTests() => Directory.CreateDirectory(_directory);
    public void Dispose() => Directory.Delete(_directory, true);

    [Fact]
    public async Task RestartRetainsCapabilitiesInSecretsAndMetadataIsSecretFree()
    {
        var history = new LinkHistory(PathToHistory, _secrets);
        var link = Link;
        await history.RecordAsync(Settings, Job, link);
        var entry = Assert.Single(history.Entries);
        Assert.Equal(link.Url, await history.GetLinkAsync(entry));
        var raw = await File.ReadAllTextAsync(PathToHistory, TestContext.Current.CancellationToken);
        Assert.DoesNotContain("/s/", raw);
        Assert.DoesNotContain(link.Url, raw);
        var restarted = new LinkHistory(PathToHistory, _secrets);
        await restarted.LoadAsync(TestContext.Current.CancellationToken);
        Assert.Equal(link.Url, await restarted.GetLinkAsync(Assert.Single(restarted.Entries)));
        Assert.Empty(restarted.ForAccount("https://other.example", Settings.Owner));
        Assert.Empty(restarted.ForAccount(Settings.Origin, new string('z', 43)));
        await restarted.MarkRevokedAsync(entry.Origin, entry.Owner, entry.ShareId);
        Assert.Empty(_secrets.Values);
        await Assert.ThrowsAsync<ClientException>(() => restarted.GetLinkAsync(entry));
        await history.LoadAsync(TestContext.Current.CancellationToken);
        Assert.True(Assert.Single(history.Entries).Revoked);
    }

    [Fact]
    public async Task RetentionDeletesOnlyTheEvictedCredentialAndPreservesSeparateLinks()
    {
        var history = new LinkHistory(PathToHistory, _secrets);
        var job = Job;
        for (var i = 0; i <= LinkHistory.Limit; i++) await history.RecordAsync(Settings, job, Link);
        Assert.Equal(LinkHistory.Limit, history.Entries.Count);
        Assert.Equal(LinkHistory.Limit, _secrets.Values.Count);
        var other = Settings with { Owner = new string('z', 43) };
        var same = Link;
        await history.RecordAsync(Settings, job, same);
        await history.RecordAsync(other, job, same);
        Assert.Equal(2, history.Entries.Count(e => e.ShareId == same.Share.Id));
    }

    [Fact]
    public async Task CorruptIndexIsPreservedAndCannotBeReplacedByNextUpload()
    {
        await File.WriteAllTextAsync(PathToHistory, "corrupt", TestContext.Current.CancellationToken);
        var history = new LinkHistory(PathToHistory, _secrets);
        await Assert.ThrowsAsync<ClientException>(() => history.LoadAsync(TestContext.Current.CancellationToken));
        await Assert.ThrowsAsync<ClientException>(() => history.RecordAsync(Settings, Job, Link));
        Assert.Equal("corrupt", await File.ReadAllTextAsync(PathToHistory, TestContext.Current.CancellationToken));
        Assert.Empty(_secrets.Values);
    }

    [Fact]
    public async Task ExpiredMissingAndUnsafeCapabilitiesCannotBeRead()
    {
        var history = new LinkHistory(PathToHistory, _secrets);
        await history.RecordAsync(Settings, Job with { Days = -1 }, Link);
        await Assert.ThrowsAsync<ClientException>(() => history.GetLinkAsync(Assert.Single(history.Entries)));
        await history.RecordAsync(Settings, Job with { Never = true }, Link);
        var entry = history.Entries[0];
        _secrets.Delete(entry.Account);
        await Assert.ThrowsAsync<ClientException>(() => history.GetLinkAsync(entry));
        _secrets.Write(entry.Account, "file:///tmp/passwords.txt");
        await Assert.ThrowsAsync<ClientException>(() => history.GetLinkAsync(entry));
    }

    [Fact]
    public async Task QueueRemovalAndNewLinkKeepHistoryAndRevocationWorksWithoutJob()
    {
        var source = Path.Combine(_directory, "history.txt");
        await File.WriteAllTextAsync(source, "hello", TestContext.Current.CancellationToken);
        var history = new LinkHistory(PathToHistory, _secrets);
        await using var queue = new UploadQueue(new MemoryStore(), new UploadEngine(), history);
        await queue.LoadAsync(TestContext.Current.CancellationToken);
        await queue.AttachAsync(new FakeApi(), "", TestContext.Current.CancellationToken);
        await queue.AddAsync([source], TestContext.Current.CancellationToken);
        await queue.WaitForIdleAsync(TestContext.Current.CancellationToken);
        var job = Assert.Single(queue.State.Jobs);
        var original = Assert.Single(history.Entries);
        await queue.NewLinkAsync(job.Id, TestContext.Current.CancellationToken);
        Assert.Equal(2, history.Entries.Count);
        await queue.RemoveAsync(job.Id, TestContext.Current.CancellationToken);
        Assert.Empty(queue.State.Jobs);
        await queue.RevokeHistoryAsync(original, TestContext.Current.CancellationToken);
        Assert.True(history.Entries.Single(e => e.ShareId == original.ShareId).Revoked);
        Assert.True(history.Entries[0].Active);
        await Assert.ThrowsAsync<ClientException>(() => queue.RevokeHistoryAsync(original with { Owner = new string('z', 43) }, TestContext.Current.CancellationToken));
    }

    [Fact]
    public async Task HistoryStorageFailurePreservesCompletedShareAndDoesNotRetry()
    {
        var source = Path.Combine(_directory, "history.txt");
        await File.WriteAllTextAsync(source, "hello", TestContext.Current.CancellationToken);
        _secrets.FailWrite = true;
        var history = new LinkHistory(PathToHistory, _secrets);
        var api = new FakeApi();
        await using var queue = new UploadQueue(new MemoryStore(), new UploadEngine(), history);
        await queue.LoadAsync(TestContext.Current.CancellationToken);
        await queue.AttachAsync(api, "", TestContext.Current.CancellationToken);
        await queue.AddAsync([source], TestContext.Current.CancellationToken);
        await queue.WaitForIdleAsync(TestContext.Current.CancellationToken);
        var job = Assert.Single(queue.State.Jobs);
        Assert.Equal(UploadStatus.Done, job.Status);
        Assert.NotEmpty(job.Link);
        Assert.Contains("не сохранена", job.Message);
        Assert.Equal(1, api.ShareCalls);
        Assert.Empty(history.Entries);
    }

    [Fact]
    public async Task IndexWriteFailureRemovesTheNewCredential()
    {
        Directory.CreateDirectory(PathToHistory);
        var history = new LinkHistory(PathToHistory, _secrets);
        await Assert.ThrowsAnyAsync<IOException>(() => history.RecordAsync(Settings, Job, Link));
        Assert.Empty(history.Entries);
        Assert.Empty(_secrets.Values);
    }

    [Fact]
    public async Task FailedServerRevokeKeepsHistoryCapabilityAndQueueLink()
    {
        var source = Path.Combine(_directory, "history.txt");
        await File.WriteAllTextAsync(source, "hello", TestContext.Current.CancellationToken);
        var history = new LinkHistory(PathToHistory, _secrets);
        await using var queue = new UploadQueue(new MemoryStore(), new UploadEngine(), history);
        await queue.LoadAsync(TestContext.Current.CancellationToken);
        await queue.AttachAsync(new FakeApi { RevokeError = true }, "", TestContext.Current.CancellationToken);
        await queue.AddAsync([source], TestContext.Current.CancellationToken);
        await queue.WaitForIdleAsync(TestContext.Current.CancellationToken);
        var entry = Assert.Single(history.Entries);
        var link = Assert.Single(queue.State.Jobs).Link;
        await Assert.ThrowsAsync<ApiException>(() => queue.RevokeHistoryAsync(entry, TestContext.Current.CancellationToken));
        Assert.False(Assert.Single(history.Entries).Revoked);
        Assert.Equal(link, Assert.Single(queue.State.Jobs).Link);
        Assert.Equal(link, await history.GetLinkAsync(entry));
    }
}

internal sealed class MemoryLinkSecrets : ILinkSecrets
{
    public Dictionary<string, string> Values { get; } = [];
    public bool FailWrite { get; set; }
    public string? Read(string account) => Values.GetValueOrDefault(account);
    public void Write(string account, string value) { if (FailWrite) throw new ClientException("Хранилище заблокировано"); Values[account] = value; }
    public void Delete(string account) => Values.Remove(account);
}
