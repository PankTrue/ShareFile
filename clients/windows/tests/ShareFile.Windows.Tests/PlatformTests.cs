using ShareFile.Core;
using ShareFile.Windows.Platform;

namespace ShareFile.Windows.Tests;

[Collection("Windows integration")]
public sealed class PlatformTests
{
    [Fact]
    public async Task LinkHistorySurvivesRestartWithRealCredentialManager()
    {
        var directory = Path.Combine(Path.GetTempPath(), "sharefile-native-history-" + Guid.NewGuid().ToString("N"));
        var credentials = new CredentialStore();
        var settings = new ClientSettings { Origin = "https://" + Guid.NewGuid().ToString("N") + ".invalid", Owner = UploadEngine.NewId() };
        var job = new UploadJob { Id = UploadEngine.NewId(), Name = "Отчёт 日本語.txt", Source = "C:\\history.txt", Fingerprint = "", Folder = "" };
        var link = new ShareLink(settings.Origin + "/s/" + UploadEngine.NewId(), new ShareIdentity(UploadEngine.NewId()));
        var history = new LinkHistory(Path.Combine(directory, "history.json"), credentials);
        try
        {
            await history.RecordAsync(settings, job, link);
            var restarted = new LinkHistory(Path.Combine(directory, "history.json"), credentials);
            await restarted.LoadAsync(TestContext.Current.CancellationToken);
            var entry = Assert.Single(restarted.Entries);
            Assert.Equal(link.Url, await restarted.GetLinkAsync(entry));
            Assert.DoesNotContain(link.Url, await File.ReadAllTextAsync(Path.Combine(directory, "history.json"), TestContext.Current.CancellationToken));
            await restarted.MarkRevokedAsync(settings.Origin, settings.Owner, link.Share.Id);
            Assert.Null(credentials.Read(entry.Account));
            await Assert.ThrowsAsync<ClientException>(() => restarted.GetLinkAsync(entry));
        }
        finally
        {
            foreach (var entry in history.Entries) credentials.Delete(entry.Account);
            if (Directory.Exists(directory)) Directory.Delete(directory, true);
        }
    }

    [Fact]
    public void CredentialManagerRoundTripUsesDisposableAccount()
    {
        var store = new CredentialStore(); var account = "test:" + Guid.NewGuid().ToString("N");
        try
        {
            Assert.Null(store.Read(account));
            store.Write(account, "one-time-test-value-日本語"); Assert.Equal("one-time-test-value-日本語", store.Read(account));
            store.Write(account, "replacement"); Assert.Equal("replacement", store.Read(account));
            store.Delete(account); Assert.Null(store.Read(account)); store.Delete(account);
        }
        finally { store.Delete(account); }
    }

    [Fact]
    public async Task SecondaryInstanceHandsOffWholeUnicodeSelection()
    {
        await using var primary = new SingleInstance(); Assert.True(primary.Acquire());
        var received = new TaskCompletionSource<LaunchRequest>(TaskCreationOptions.RunContinuationsAsynchronously);
        primary.Listen(request => { received.TrySetResult(request); return Task.CompletedTask; }, error => received.TrySetException(new Exception(error)));
        await using var secondary = new SingleInstance(); Assert.False(secondary.Acquire());
        var request = LaunchRequest.Parse(["--upload", "C:\\Отчёт 日本語.txt", "C:\\File with spaces.txt"]);
        await secondary.ForwardAsync(request);
        var result = await received.Task.WaitAsync(TimeSpan.FromSeconds(10), TestContext.Current.CancellationToken);
        Assert.False(result.Show); Assert.Equal(request.Paths, result.Paths);
    }

    [Fact]
    public void ExplorerAndStartupLaunchDoNotRequestWindow()
    {
        Assert.False(LaunchRequest.Parse(["--upload", "C:\\report.txt"]).Show);
        Assert.False(LaunchRequest.Parse(["--background"]).Show);
        Assert.True(LaunchRequest.Parse([]).Show);
        Assert.Throws<ClientException>(() => LaunchRequest.Parse(["--upload", "relative.txt"]));
    }
}

[CollectionDefinition("Windows integration", DisableParallelization = true)]
public sealed class WindowsIntegrationCollection;
