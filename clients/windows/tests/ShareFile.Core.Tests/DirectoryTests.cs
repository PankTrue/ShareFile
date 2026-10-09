using ShareFile.Core;

namespace ShareFile.Core.Tests;

public sealed class DirectoryTests : IDisposable
{
    private readonly string _root = Path.Combine(Path.GetTempPath(), "sharefile-directory-" + Guid.NewGuid().ToString("N"));
    public DirectoryTests() => Directory.CreateDirectory(_root);
    public void Dispose() => Directory.Delete(_root, true);

    [Fact]
    public async Task TreeCheckpointContainsEmptyFoldersAndIndependentDuplicateNames()
    {
        Directory.CreateDirectory(Path.Combine(_root, "a")); Directory.CreateDirectory(Path.Combine(_root, "b"));
        Directory.CreateDirectory(Path.Combine(_root, "empty", "nested"));
        await File.WriteAllTextAsync(Path.Combine(_root, "a", "report.txt"), "first", TestContext.Current.CancellationToken);
        await File.WriteAllTextAsync(Path.Combine(_root, "b", "report.txt"), "second", TestContext.Current.CancellationToken);
        var job = await UploadEngine.PrepareAsync(_root, FakeApi.Folder, TestContext.Current.CancellationToken);
        Assert.Equal(11, job.Size); Assert.Equal(2, job.Directory!.Files.Count);
        Assert.Contains(job.Directory.Folders, f => f.Path == "empty/nested");
        Assert.Equal(2, job.Directory.Files.Select(f => f.Job.Id).Distinct().Count());
        UploadEngine.Validate(job);
        var bad = job with { Directory = job.Directory with { Files = [job.Directory.Files[0] with { Path = "../outside.txt" }] } };
        Assert.Throws<ClientException>(() => UploadEngine.Validate(bad));
    }

    [Fact]
    public async Task EmptyRootHasAValidZeroByteDirectoryCheckpoint()
    {
        var job = await UploadEngine.PrepareAsync(_root, FakeApi.Folder, TestContext.Current.CancellationToken);
        Assert.NotNull(job.Directory); Assert.Empty(job.Directory.Files); Assert.Empty(job.Directory.Folders);
        Assert.Equal(0, job.Size); UploadEngine.Validate(job);
    }

    [Fact]
    public async Task ChangedTreeFailsBeforeAnyNetworkMutation()
    {
        var job = await UploadEngine.PrepareAsync(_root, FakeApi.Folder, TestContext.Current.CancellationToken);
        await File.WriteAllTextAsync(Path.Combine(_root, "added.txt"), "changed", TestContext.Current.CancellationToken);
        await Assert.ThrowsAsync<ClientException>(() => new UploadEngine().UploadAsync(new FakeApi(), job,
            (_, _) => Task.CompletedTask, TestContext.Current.CancellationToken));
    }

    [Fact]
    public async Task DirectorySymlinkIsRejectedBeforeReadingExternalFiles()
    {
        var external = Path.Combine(Path.GetTempPath(), "sharefile-external-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(external);
        try
        {
            try { Directory.CreateSymbolicLink(Path.Combine(_root, "link"), external); }
            catch (UnauthorizedAccessException) { Assert.Skip("Symbolic links require Windows Developer Mode or appropriate privileges"); }
            await Assert.ThrowsAsync<ClientException>(() => UploadEngine.PrepareAsync(_root, FakeApi.Folder, TestContext.Current.CancellationToken));
        }
        finally { Directory.Delete(external, true); }
    }
}
