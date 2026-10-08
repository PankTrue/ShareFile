using System.Net;
using System.Net.Http.Headers;
using System.Text;
using ShareFile.Core;

namespace ShareFile.Core.Tests;

public sealed class ProtocolTests
{
    [Theory]
    [InlineData("http://localhost:8080", "http://localhost:8080")]
    [InlineData("http://[::1]:8080/", "http://[::1]:8080")]
    [InlineData("https://192.168.1.1:8081/", "https://192.168.1.1:8081")]
    public void OriginNormalization(string input, string expected) => Assert.Equal(expected, Protocol.NormalizeOrigin(input));
    [Theory]
    [InlineData("http://192.168.1.1")]
    [InlineData("https://user:secret@example.com")]
    [InlineData("https://example.com/api")]
    [InlineData("https://example.com?token=secret")]
    [InlineData("file:///tmp/file")]
    [InlineData("https://example.com/#token")]
    public void UnsafeOriginsAreRejected(string input) => Assert.Throws<ClientException>(() => Protocol.NormalizeOrigin(input));

    [Fact]
    public void AdvertisedUploadOriginIsIgnored()
    {
        Assert.Equal(FakeApi.Upload, Protocol.UploadPath("https://public.example" + FakeApi.Upload));
        Assert.Throws<ClientException>(() => Protocol.UploadPath(FakeApi.Upload + "?token=secret"));
    }

    [Fact]
    public async Task RedirectCannotForwardDeviceToken()
    {
        // Test the real production handler against two local listeners.
        using var first = new System.Net.Sockets.TcpListener(IPAddress.Loopback, 0);
        using var second = new System.Net.Sockets.TcpListener(IPAddress.Loopback, 0);
        first.Start(); second.Start();
        var firstPort = ((IPEndPoint)first.LocalEndpoint).Port;
        var secondPort = ((IPEndPoint)second.LocalEndpoint).Port;
        var server = Task.Run(async () =>
        {
            using var client = await first.AcceptTcpClientAsync(TestContext.Current.CancellationToken);
            await using var stream = client.GetStream();
            using var reader = new StreamReader(stream, leaveOpen: true);
            while (await reader.ReadLineAsync(TestContext.Current.CancellationToken) is { Length: > 0 }) { }
            await stream.WriteAsync(Encoding.ASCII.GetBytes($"HTTP/1.1 307 Temporary Redirect\r\nLocation: http://127.0.0.1:{secondPort}/api/v1/me\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"), TestContext.Current.CancellationToken);
        }, TestContext.Current.CancellationToken);
        using var api = new ShareFileApi($"http://127.0.0.1:{firstPort}", "disposable-token");
        var error = await Assert.ThrowsAsync<ApiException>(() => api.MeAsync(TestContext.Current.CancellationToken));
        Assert.Equal(307, error.Status); await server;
        Assert.False(second.Pending());
    }

    [Fact]
    public async Task PatchUsesPostOverrideKnownLengthAndDoesNotDisposeSource()
    {
        byte[]? received = null;
        using var handler = new RecordingHandler(async request =>
        {
            Assert.Equal(HttpMethod.Post, request.Method);
            Assert.Equal("PATCH", request.Headers.GetValues("X-HTTP-Method-Override").Single());
            Assert.Equal("1.0.0", request.Headers.GetValues("Tus-Resumable").Single());
            Assert.Equal("application/offset+octet-stream", request.Content!.Headers.ContentType!.MediaType);
            Assert.Equal(2, request.Content.Headers.ContentLength);
            Assert.Equal("Bearer", request.Headers.Authorization!.Scheme);
            received = await request.Content.ReadAsByteArrayAsync();
            return new HttpResponseMessage(HttpStatusCode.NoContent);
        });
        using var api = new ShareFileApi("https://sharefile.example", "test-token", handler);
        using var source = new MemoryStream([1, 2, 3, 4]); source.Position = 1;
        await api.PatchAsync(FakeApi.Upload, 1, source, 2, TestContext.Current.CancellationToken);
        Assert.Equal(new byte[] { 2, 3 }, received); Assert.True(source.CanRead);
    }

    [Fact]
    public async Task SharePostIsIssuedOnlyOnceAndErrorsDoNotExposeResponseCapabilities()
    {
        using var handler = new RecordingHandler(_ => Task.FromResult(new HttpResponseMessage(HttpStatusCode.ServiceUnavailable)
        { Content = new StringContent("secret-token-secret-url") }));
        using var api = new ShareFileApi("https://sharefile.example", "token", handler);
        var error = await Assert.ThrowsAsync<ApiException>(() => api.ShareAsync(FakeApi.FileId, new ShareOptions(), TestContext.Current.CancellationToken));
        Assert.Equal(1, handler.Calls); Assert.DoesNotContain("secret", error.Message);
    }

    [Fact]
    public async Task PairingRejectsUntrustedBrowserAddress()
    {
        var json = "{\"request\":{\"id\":\"" + FakeApi.Folder + "\",\"secret\":\"test\",\"expires_at\":99},\"verification_url\":\"https://evil.example/device/connect?request=" + FakeApi.Folder + "\",\"interval\":1}";
        using var handler = new RecordingHandler(_ => Task.FromResult(new HttpResponseMessage(HttpStatusCode.Created) { Content = new StringContent(json) }));
        using var api = new ShareFileApi("https://sharefile.example", "", handler);
        await Assert.ThrowsAsync<ClientException>(() => api.StartPairingAsync("Windows test", TestContext.Current.CancellationToken));
    }
}

internal sealed class RecordingHandler(Func<HttpRequestMessage, Task<HttpResponseMessage>> respond) : HttpMessageHandler
{
    public int Calls { get; private set; }
    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    { Calls++; return respond(request); }
}
