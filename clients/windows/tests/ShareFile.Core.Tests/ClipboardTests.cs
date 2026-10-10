using ShareFile.Core;

namespace ShareFile.Core.Tests;

public sealed class ClipboardTests
{
    private static readonly byte[] Png = Convert.FromBase64String("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=");

    [Fact]
    public async Task DurableSourceSurvivesRestartAndCleanupLeavesUserFilesAlone()
    {
        var root = Path.Combine(Path.GetTempPath(), "sharefile-clipboard-" + Guid.NewGuid().ToString("N"));
        try
        {
            var spool = new ClipboardImages(Path.Combine(root, "clipboard"));
            var first = await spool.WriteAsync(Png, TestContext.Current.CancellationToken);
            var second = await spool.WriteAsync(Png, TestContext.Current.CancellationToken);
            spool.Reconcile([]); Assert.True(File.Exists(first)); Assert.True(File.Exists(second));
            spool.Release(first); spool.Release(second);
            Assert.NotEqual(first, second);
            Assert.Equal(Png, await File.ReadAllBytesAsync(first, TestContext.Current.CancellationToken));
            var job = (await UploadEngine.PrepareAsync(first, UploadEngine.NewId(), TestContext.Current.CancellationToken)) with { Status = UploadStatus.Paused };
            var store = new JsonStateStore(Path.Combine(root, "state.json"));
            await store.SaveAsync(new ClientState { Jobs = [job] }, TestContext.Current.CancellationToken);
            var restored = await store.LoadAsync(TestContext.Current.CancellationToken);
            new ClipboardImages(Path.Combine(root, "clipboard")).Reconcile(restored.Jobs);
            Assert.True(File.Exists(first)); Assert.False(File.Exists(second));
            var user = Path.Combine(root, "Screenshot-personal.png");
            await File.WriteAllBytesAsync(user, Png, TestContext.Current.CancellationToken);
            spool.Remove(user);
            spool.Reconcile([job with { Status = UploadStatus.Done }]);
            Assert.False(File.Exists(first)); Assert.True(File.Exists(user));
        }
        finally { if (Directory.Exists(root)) Directory.Delete(root, true); }
    }

    [Fact]
    public async Task InvalidClipboardDoesNotCreateFiles()
    {
        var root = Path.Combine(Path.GetTempPath(), "sharefile-clipboard-" + Guid.NewGuid().ToString("N"));
        var spool = new ClipboardImages(root);
        await Assert.ThrowsAsync<ClientException>(() => spool.WriteAsync("clipboard text"u8.ToArray(), TestContext.Current.CancellationToken));
        var huge = Png.ToArray();
        System.Buffers.Binary.BinaryPrimitives.WriteUInt32BigEndian(huge.AsSpan(16, 4), 100000);
        System.Buffers.Binary.BinaryPrimitives.WriteUInt32BigEndian(huge.AsSpan(20, 4), 100000);
        await Assert.ThrowsAsync<ClientException>(() => spool.WriteAsync(huge, TestContext.Current.CancellationToken));
        Assert.False(Directory.Exists(root));
    }
}
