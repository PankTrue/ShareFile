using System.Diagnostics;
using System.Net;
using System.Net.Http.Json;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text.Json;
using ShareFile.Core;

namespace ShareFile.Core.Tests;

public sealed class BackendTests
{
    [Fact]
    [Trait("Category", "Backend")]
    public async Task PairingRestartResumeSharingAndRevocationAgainstRealBackend()
    {
        var executable = Environment.GetEnvironmentVariable("SHAREFILE_BACKEND");
        Assert.SkipWhen(string.IsNullOrEmpty(executable), "Set SHAREFILE_BACKEND to a locally built Go backend to run integration tests");
        var root = Path.Combine(Path.GetTempPath(), "sharefile-net-integration-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        Process? server = null;
        using var timeout = new CancellationTokenSource(TimeSpan.FromMinutes(2)); var ct = timeout.Token;
        static int FreePort() { using var listener = new TcpListener(IPAddress.Loopback, 0); listener.Start(); return ((IPEndPoint)listener.LocalEndpoint).Port; }
        var port = FreePort(); var admin = FreePort();
        var origin = $"http://127.0.0.1:{port}";
        var config = Path.Combine(root, "config.json");
        await File.WriteAllTextAsync(config, JsonSerializer.Serialize(new
        {
            data_dir = Path.Combine(root, "storage"),
            volume_id = "dotnet-test",
            public_addr = $"127.0.0.1:{port}",
            admin_addr = $"127.0.0.1:{admin}",
            public_url = origin,
            admin_url = $"http://127.0.0.1:{admin}",
            insecure_local = true,
            reserve_bytes = 0,
            chunk_bytes = 1048576
        }), ct);
        ProcessStartInfo StartInfo(string command)
        {
            var info = new ProcessStartInfo(executable!)
            {
                UseShellExecute = false,
                RedirectStandardInput = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true
            };
            info.ArgumentList.Add(command); info.ArgumentList.Add("-config"); info.ArgumentList.Add(config);
            return info;
        }
        async Task CliAsync(string command, bool user = false)
        {
            var info = StartInfo(command);
            if (user) { info.ArgumentList.Add("-username"); info.ArgumentList.Add("dotnet-test"); info.ArgumentList.Add("-password-stdin"); }
            using var process = Process.Start(info)!;
            var output = process.StandardOutput.ReadToEndAsync(ct); var error = process.StandardError.ReadToEndAsync(ct);
            if (user) await process.StandardInput.WriteLineAsync("disposable-dotnet-test-password".AsMemory(), ct);
            process.StandardInput.Close();
            await process.WaitForExitAsync(ct); await output; await error;
            Assert.Equal(0, process.ExitCode);
        }
        using var http = new HttpClient(new SocketsHttpHandler { UseCookies = false, AllowAutoRedirect = false }) { BaseAddress = new Uri(origin) };
        async Task StartAsync()
        {
            server = Process.Start(StartInfo("serve"))!;
            _ = server.StandardOutput.ReadToEndAsync(ct); _ = server.StandardError.ReadToEndAsync(ct);
            for (var attempt = 0; attempt < 100; attempt++)
            {
                Assert.False(server.HasExited);
                try { using var ready = await http.GetAsync("/readyz", ct); if (ready.StatusCode == HttpStatusCode.OK) return; }
                catch (HttpRequestException) { }
                await Task.Delay(50, ct);
            }
            throw new TimeoutException("Disposable backend startup timed out");
        }
        async Task StopAsync()
        {
            // Simulate a process crash, including release of the volume lock.
            server!.Kill(entireProcessTree: true); await server.WaitForExitAsync(ct); server.Dispose(); server = null;
        }
        try
        {
            await CliAsync("init-storage"); await CliAsync("admin-create", true); await StartAsync();
            using var login = await http.PostAsJsonAsync("/api/v1/auth/login", new { username = "dotnet-test", password = "disposable-dotnet-test-password" }, ct);
            login.EnsureSuccessStatusCode();
            var browser = await login.Content.ReadFromJsonAsync<JsonElement>(ct);
            var cookie = login.Headers.GetValues("Set-Cookie").First().Split(';')[0];
            using var anonymous = new ShareFileApi(origin);
            var pair = await anonymous.StartPairingAsync("Windows integration test", ct);
            Assert.True(pair.Interval >= 12); Assert.Null(await anonymous.PollPairingAsync(pair.Request, ct));
            using var approvalRequest = new HttpRequestMessage(HttpMethod.Post, "/api/v1/device-requests/" + pair.Request.Id + "/approve");
            approvalRequest.Headers.Add("Cookie", cookie); approvalRequest.Headers.Add("X-CSRF-Token", browser.GetProperty("csrf_token").GetString());
            using var approval = await http.SendAsync(approvalRequest, ct); Assert.Equal(HttpStatusCode.NoContent, approval.StatusCode);
            var token = await anonymous.PollPairingAsync(pair.Request, ct); Assert.NotNull(token);
            using var api = new ShareFileApi(origin, token);
            var session = await api.MeAsync(ct);
            var folder = (await api.FoldersAsync(session.User.HomeId, ct)).Single(n => n.Name == "Uploads");
            var source = Path.Combine(root, "Отчёт 日本語.bin"); var payload = RandomNumberGenerator.GetBytes(2 * 1048576 + 137);
            await File.WriteAllBytesAsync(source, payload, ct);
            var job = await UploadEngine.PrepareAsync(source, folder.Id, ct);
            var created = await api.CreateUploadAsync(job, ct);
            // Lose the creation response checkpoint and commit only part of the first chunk.
            using (var partial = new MemoryStream(payload[..70000])) await api.PatchAsync(created.Upload, 0, partial, partial.Length, ct);
            await StopAsync(); await StartAsync();
            var engine = new UploadEngine();
            var resumed = await engine.UploadAsync(api, job, (_, _) => Task.CompletedTask, ct);
            Assert.Equal(created.FileId, resumed.FileId); Assert.Equal(payload.Length, resumed.Offset);
            using var download = new HttpRequestMessage(HttpMethod.Get, "/api/v1/nodes/" + resumed.FileId + "/content");
            download.Headers.Authorization = new("Bearer", token);
            using var downloaded = await http.SendAsync(download, ct); downloaded.EnsureSuccessStatusCode();
            Assert.Equal(SHA256.HashData(payload), SHA256.HashData(await downloaded.Content.ReadAsByteArrayAsync(ct)));
            var link = await api.ShareAsync(resumed.FileId, new ShareOptions(), ct);
            Assert.Equal(payload, await http.GetByteArrayAsync(link.Url, ct));
            var protectedLink = await api.ShareAsync(resumed.FileId, new ShareOptions(Never: true, Password: "link-test-password"), ct);
            using var protectedResponse = await http.GetAsync(protectedLink.Url, ct); Assert.Equal(HttpStatusCode.Unauthorized, protectedResponse.StatusCode);
            await api.RevokeShareAsync(link.Share.Id, ct);
            using var revoked = await http.GetAsync(link.Url, ct); Assert.Equal(HttpStatusCode.NotFound, revoked.StatusCode);
            var empty = Path.Combine(root, "пустой.txt"); await File.WriteAllBytesAsync(empty, [], ct);
            var zero = await engine.UploadAsync(api, await UploadEngine.PrepareAsync(empty, folder.Id, ct), (_, _) => Task.CompletedTask, ct);
            Assert.Equal(0, zero.Offset);
            var zeroLink = await api.ShareAsync(zero.FileId, new ShareOptions(), ct);
            Assert.Empty(await http.GetByteArrayAsync(zeroLink.Url, ct));
            // A directory is one queue item, one capability, and an independently resumable tree.
            var tree = Path.Combine(root, "Проект 日本語");
            Directory.CreateDirectory(Path.Combine(tree, "a"));
            Directory.CreateDirectory(Path.Combine(tree, "b"));
            Directory.CreateDirectory(Path.Combine(tree, "empty", "nested"));
            await File.WriteAllBytesAsync(Path.Combine(tree, "a", "report.txt"), payload, ct);
            await File.WriteAllTextAsync(Path.Combine(tree, "b", "report.txt"), "different report", ct);
            await File.WriteAllBytesAsync(Path.Combine(tree, "zero.txt"), [], ct);
            var directoryJob = await UploadEngine.PrepareAsync(tree, folder.Id, ct);
            Assert.NotNull(directoryJob.Directory);
            var directoryRoot = await api.CreateFolderAsync(folder.Id, directoryJob.Name, directoryJob.Id, ct);
            var folderIds = new Dictionary<string, string> { [""] = directoryRoot.Id };
            foreach (var entry in directoryJob.Directory.Folders)
            {
                var parentPath = entry.Path.Contains('/') ? entry.Path[..entry.Path.LastIndexOf('/')] : "";
                var node = await api.CreateFolderAsync(folderIds[parentPath], entry.Path.Split('/').Last(), entry.Key, ct);
                folderIds[entry.Path] = node.Id;
            }
            var first = directoryJob.Directory.Files[0].Job with { Folder = folderIds["a"] };
            var firstCreated = await api.CreateUploadAsync(first, ct);
            using (var partial = new MemoryStream(payload[..70000])) await api.PatchAsync(firstCreated.Upload, 0, partial, partial.Length, ct);
            // Restart with all create replies absent from the persisted directory manifest.
            await StopAsync(); await StartAsync();
            var stateFile = Path.Combine(root, "client", "state.json");
            var stateStore = new JsonStateStore(stateFile);
            await stateStore.SaveAsync(new ClientState { Settings = new ClientSettings { Origin = origin, Owner = session.User.Id, Folder = folder.Id },
                Jobs = [directoryJob with { Status = UploadStatus.Uploading, Batch = directoryJob.Id }] }, ct);
            await using (var queue = new UploadQueue(stateStore, engine))
            {
                var completions = new List<BatchCompletion>(); queue.Completed += completions.Add;
                await queue.LoadAsync(ct); await queue.AttachAsync(api, "", ct);
                Assert.Equal(UploadStatus.Paused, Assert.Single(queue.State.Jobs).Status);
                await queue.ResumeAsync(directoryJob.Id, ct); await queue.WaitForIdleAsync(ct);
                var done = Assert.Single(queue.State.Jobs);
                Assert.Equal(UploadStatus.Done, done.Status); Assert.Equal(directoryRoot.Id, done.FileId);
                Assert.Equal(done.Size, done.Offset); Assert.Single(Assert.Single(completions).Links);
                Assert.Equal(firstCreated.FileId, done.Directory!.Files[0].Job.FileId);
                foreach (var entry in done.Directory.Files)
                {
                    Assert.True(entry.Complete);
                    var expected = await File.ReadAllBytesAsync(entry.Job.Source, ct);
                    Assert.Equal(expected, await http.GetByteArrayAsync(done.Link + "/nodes/" + entry.Job.FileId, ct));
                }
                Assert.Equal("empty/nested", done.Directory.Folders.Last().Path);
                Assert.Empty(await api.FoldersAsync(done.Directory.Folders.Last().Id, ct));
                Assert.Equal(JsonSerializer.Serialize(done.Directory), JsonSerializer.Serialize((await stateStore.LoadAsync(ct)).Jobs[0].Directory));
                var persisted = await File.ReadAllTextAsync(stateFile, ct);
                Assert.DoesNotContain(token, persisted); Assert.DoesNotContain("/s/", persisted);
            }
            var changed = await UploadEngine.PrepareAsync(tree, folder.Id, ct);
            await File.WriteAllTextAsync(Path.Combine(tree, "new.txt"), "changed", ct);
            await Assert.ThrowsAsync<ClientException>(() => engine.UploadAsync(api, changed, (_, _) => Task.CompletedTask, ct));
            var cancellation = await UploadEngine.PrepareAsync(tree, folder.Id, ct);
            var cancelledRoot = await api.CreateFolderAsync(folder.Id, cancellation.Name, cancellation.Id, ct);
            await engine.CancelAsync(api, cancellation, ct);
            await Assert.ThrowsAsync<ApiException>(() => api.NodeAsync(cancelledRoot.Id, ct));
            var cancel = await api.CreateUploadAsync(await UploadEngine.PrepareAsync(source, folder.Id, ct), ct);
            await engine.CancelAsync(api, cancel, ct);
            await Assert.ThrowsAsync<ApiException>(() => api.HeadAsync(cancel.Upload, ct));
        }
        finally
        {
            if (server is { HasExited: false }) { server.Kill(entireProcessTree: true); await server.WaitForExitAsync(TestContext.Current.CancellationToken); }
            server?.Dispose(); Directory.Delete(root, true);
        }
    }
}
