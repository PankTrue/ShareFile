using System.Globalization;
using System.Net;
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text;
using System.Text.Json;

namespace ShareFile.Core;

public sealed class ShareFileApi : IShareFileApi, IDisposable
{
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web);
    private readonly HttpClient _http;
    private readonly string _token;
    public string Origin { get; }

    public ShareFileApi(string origin, string token = "") : this(origin, token, new SocketsHttpHandler
    {
        AllowAutoRedirect = false,
        UseCookies = false,
        ConnectTimeout = TimeSpan.FromSeconds(15),
        PooledConnectionLifetime = TimeSpan.FromMinutes(5),
        PooledConnectionIdleTimeout = TimeSpan.FromSeconds(30)
    })
    { }

    // Injection seam for protocol tests; production always uses the hardened handler above.
    public ShareFileApi(string origin, string token, HttpMessageHandler handler)
    {
        Origin = Protocol.NormalizeOrigin(origin);
        _token = token;
        _http = new HttpClient(handler) { Timeout = Timeout.InfiniteTimeSpan };
    }

    private HttpRequestMessage Request(HttpMethod method, string path, HttpContent? content = null, bool tus = false)
    {
        if (!path.StartsWith("/api/v1/", StringComparison.Ordinal) || path.Contains('\r') || path.Contains('\n'))
            throw new ClientException("Некорректный API-путь");
        var request = new HttpRequestMessage(method, Origin + path) { Content = content };
        if (_token.Length != 0) request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", _token);
        if (tus) request.Headers.Add("Tus-Resumable", "1.0.0");
        return request;
    }

    private async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, HttpStatusCode expected, CancellationToken ct)
    {
        HttpResponseMessage response;
        try { response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, ct).ConfigureAwait(false); }
        catch (HttpRequestException) { throw new ClientException("Не удалось связаться с сервером. Проверьте сеть и сертификат HTTPS"); }
        if (response.StatusCode == expected) return response;
        var status = (int)response.StatusCode;
        var delay = response.Headers.RetryAfter?.Delta ??
            (response.Headers.RetryAfter?.Date - DateTimeOffset.UtcNow) ?? TimeSpan.Zero;
        response.Dispose();
        throw new ApiException(status, TimeSpan.FromSeconds(Math.Clamp(delay.TotalSeconds, 0, 60)));
    }

    private static CancellationTokenSource Deadline(CancellationToken ct, bool chunk = false)
    {
        var deadline = CancellationTokenSource.CreateLinkedTokenSource(ct);
        deadline.CancelAfter(chunk ? TimeSpan.FromMinutes(15) : TimeSpan.FromSeconds(30));
        return deadline;
    }

    private static async Task<T> ReadAsync<T>(HttpResponseMessage response, CancellationToken ct)
    {
        try
        {
            await response.Content.LoadIntoBufferAsync(1024 * 1024, ct).ConfigureAwait(false);
            return await response.Content.ReadFromJsonAsync<T>(Json, ct).ConfigureAwait(false)
                ?? throw new ClientException("Пустой ответ сервера");
        }
        catch (JsonException) { throw new ClientException("Некорректный ответ сервера"); }
        catch (HttpRequestException) { throw new ClientException("Не удалось прочитать ответ сервера"); }
    }

    private async Task<T> JsonAsync<T>(HttpMethod method, string path, object? body, HttpStatusCode expected, CancellationToken ct)
    {
        using var deadline = Deadline(ct);
        using var request = Request(method, path, body is null ? null : JsonContent.Create(body, options: Json));
        using var response = await SendAsync(request, expected, deadline.Token).ConfigureAwait(false);
        return await ReadAsync<T>(response, deadline.Token).ConfigureAwait(false);
    }

    public Task<Session> MeAsync(CancellationToken cancellationToken) =>
        JsonAsync<Session>(HttpMethod.Get, "/api/v1/me", null, HttpStatusCode.OK, cancellationToken);
    public Task<Node> NodeAsync(string id, CancellationToken cancellationToken) =>
        JsonAsync<Node>(HttpMethod.Get, "/api/v1/nodes/" + Protocol.RequireIdentifier(id), null, HttpStatusCode.OK, cancellationToken);

    public async Task<Node> CreateFolderAsync(string parent, string name, string key, CancellationToken cancellationToken)
    {
        using var deadline = Deadline(cancellationToken);
        using var request = Request(HttpMethod.Post, "/api/v1/folders",
            JsonContent.Create(new { parent_id = Protocol.RequireIdentifier(parent), name }, options: Json));
        request.Headers.Add("Idempotency-Key", Protocol.RequireIdentifier(key));
        using var response = await SendAsync(request, HttpStatusCode.Created, deadline.Token).ConfigureAwait(false);
        var node = await ReadAsync<Node>(response, deadline.Token).ConfigureAwait(false);
        Protocol.RequireIdentifier(node.Id);
        if (node.Kind != "folder" || node.ParentId != parent) throw new ClientException("Сервер вернул некорректную папку");
        return node;
    }

    public async Task TrashNodeAsync(string id, CancellationToken cancellationToken)
    {
        using var deadline = Deadline(cancellationToken);
        using var request = Request(HttpMethod.Delete, "/api/v1/nodes/" + Protocol.RequireIdentifier(id));
        try { using var response = await SendAsync(request, HttpStatusCode.NoContent, deadline.Token).ConfigureAwait(false); }
        catch (ApiException error) when (error.Status == 404) { }
    }

    public async Task<IReadOnlyList<Node>> FoldersAsync(string parent, CancellationToken cancellationToken)
    {
        if (parent.Length != 0) Protocol.RequireIdentifier(parent);
        var result = new List<Node>();
        for (var offset = 0; offset < 100000; offset += 100)
        {
            var nodes = await JsonAsync<Node[]>(HttpMethod.Get,
                $"/api/v1/folders?parent_id={parent}&limit=100&offset={offset}", null, HttpStatusCode.OK, cancellationToken).ConfigureAwait(false);
            result.AddRange(nodes.Where(n => n.Kind == "folder" && Protocol.IsIdentifier(n.Id)));
            if (nodes.Length < 100) return result;
        }
        throw new ClientException("Слишком много объектов в папке");
    }

    public async Task<Pairing> StartPairingAsync(string name, CancellationToken cancellationToken)
    {
        var pairing = await JsonAsync<Pairing>(HttpMethod.Post, "/api/v1/device-requests", new { name }, HttpStatusCode.Created, cancellationToken).ConfigureAwait(false);
        Protocol.ValidatePairing(pairing, Origin);
        return pairing with { Interval = Math.Max(pairing.Interval, 12) };
    }

    public async Task<string?> PollPairingAsync(PairRequest pairing, CancellationToken cancellationToken)
    {
        using var deadline = Deadline(cancellationToken);
        using var request = Request(HttpMethod.Post, "/api/v1/device-requests/" + Protocol.RequireIdentifier(pairing.Id) + "/token",
            JsonContent.Create(new { secret = pairing.Secret }));
        // 202 is the pending state; no retry policy is attached to this one-time token exchange.
        HttpResponseMessage response;
        try { response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, deadline.Token).ConfigureAwait(false); }
        catch (HttpRequestException) { throw new ClientException("Не удалось проверить подтверждение подключения"); }
        using (response)
        {
            if (response.StatusCode == HttpStatusCode.Accepted) return null;
            if (response.StatusCode != HttpStatusCode.OK) throw new ApiException((int)response.StatusCode);
            var result = await ReadAsync<TokenResponse>(response, deadline.Token).ConfigureAwait(false);
            return !string.IsNullOrEmpty(result.Token) ? result.Token : throw new ClientException("Сервер не вернул токен устройства");
        }
    }
    private sealed record TokenResponse(string Token);

    private static string Header(HttpResponseMessage response, string name) =>
        response.Headers.TryGetValues(name, out var values) ? values.SingleOrDefault() ?? "" : "";
    private static long Number(HttpResponseMessage response, string name) =>
        long.TryParse(Header(response, name), NumberStyles.None, CultureInfo.InvariantCulture, out var value)
            ? value : throw new ClientException("Сервер вернул некорректное состояние загрузки");

    public async Task<UploadJob> CreateUploadAsync(UploadJob job, CancellationToken cancellationToken)
    {
        using var deadline = Deadline(cancellationToken);
        using var request = Request(HttpMethod.Post, "/api/v1/uploads", new ByteArrayContent([]), tus: true);
        request.Headers.Add("Upload-Length", job.Size.ToString(CultureInfo.InvariantCulture));
        request.Headers.Add("Idempotency-Key", Protocol.RequireIdentifier(job.Id));
        request.Headers.Add("Upload-Metadata", "filename " + Convert.ToBase64String(Encoding.UTF8.GetBytes(job.Name)) +
            ",folder_id " + Convert.ToBase64String(Encoding.UTF8.GetBytes(Protocol.RequireIdentifier(job.Folder))));
        using var response = await SendAsync(request, HttpStatusCode.Created, deadline.Token).ConfigureAwait(false);
        return job with
        {
            Upload = Protocol.UploadPath(Header(response, "Location")),
            FileId = Protocol.RequireIdentifier(Header(response, "Sharefile-File-ID"))
        };
    }

    public async Task<UploadHead> HeadAsync(string path, CancellationToken cancellationToken)
    {
        using var deadline = Deadline(cancellationToken);
        using var request = Request(HttpMethod.Head, Protocol.UploadPath(path), tus: true);
        using var response = await SendAsync(request, HttpStatusCode.OK, deadline.Token).ConfigureAwait(false);
        var head = new UploadHead(Number(response, "Upload-Offset"), Number(response, "Upload-Length"),
            Number(response, "Sharefile-Chunk-Size"), Protocol.RequireIdentifier(Header(response, "Sharefile-File-ID")),
            Header(response, "Sharefile-Upload-State"));
        if (head.Offset < 0 || head.Offset > head.Length || head.ChunkSize < 1 ||
            head.State is not ("open" or "committing" or "complete")) throw new ClientException("Некорректное состояние загрузки на сервере");
        return head with { ChunkSize = Math.Min(head.ChunkSize, 4 * 1024 * 1024) };
    }

    public async Task PatchAsync(string path, long offset, Stream source, long count, CancellationToken cancellationToken)
    {
        using var deadline = Deadline(cancellationToken, chunk: true);
        using var content = new ChunkContent(source, count);
        content.Headers.ContentType = new MediaTypeHeaderValue("application/offset+octet-stream");
        using var request = Request(HttpMethod.Post, Protocol.UploadPath(path), content, tus: true);
        request.Headers.Add("Upload-Offset", offset.ToString(CultureInfo.InvariantCulture));
        request.Headers.Add("X-HTTP-Method-Override", "PATCH");
        using var response = await SendAsync(request, HttpStatusCode.NoContent, deadline.Token).ConfigureAwait(false);
    }

    public async Task CancelUploadAsync(string path, CancellationToken cancellationToken)
    {
        using var deadline = Deadline(cancellationToken);
        using var request = Request(HttpMethod.Post, Protocol.UploadPath(path), new ByteArrayContent([]), tus: true);
        request.Headers.Add("X-HTTP-Method-Override", "DELETE");
        try { using var response = await SendAsync(request, HttpStatusCode.NoContent, deadline.Token).ConfigureAwait(false); }
        catch (ApiException error) when (error.Status == 404) { }
    }

    public async Task<ShareLink> ShareAsync(string fileId, ShareOptions options, CancellationToken cancellationToken)
    {
        if ((!options.Never && options.Days is < 1 or > 365) ||
            (options.Password.Length != 0 && Encoding.UTF8.GetByteCount(options.Password) is < 12 or > 256))
            throw new ClientException("Срок ссылки: 1–365 дней; пароль: 12–256 байт UTF-8");
        var body = new Dictionary<string, object?>
        {
            ["node_id"] = Protocol.RequireIdentifier(fileId),
            ["password"] = options.Password,
            ["never_expires"] = options.Never
        };
        if (!options.Never) body["expires_at"] = DateTimeOffset.UtcNow.AddDays(options.Days).ToUnixTimeSeconds();
        var result = await JsonAsync<ShareLink>(HttpMethod.Post, "/api/v1/shares", body, HttpStatusCode.Created, cancellationToken).ConfigureAwait(false);
        Protocol.ValidateLink(result);
        return result;
    }

    public async Task RevokeShareAsync(string id, CancellationToken cancellationToken)
    {
        using var deadline = Deadline(cancellationToken);
        using var request = Request(HttpMethod.Delete, "/api/v1/shares/" + Protocol.RequireIdentifier(id));
        using var response = await SendAsync(request, HttpStatusCode.NoContent, deadline.Token).ConfigureAwait(false);
    }

    public void Dispose() => _http.Dispose();

    // This content does not own the source file. It streams a bounded slice without buffering the chunk.
    private sealed class ChunkContent(Stream source, long length) : HttpContent
    {
        private readonly long _length = length;
        protected override bool TryComputeLength(out long computed) { computed = _length; return true; }
        protected override Task SerializeToStreamAsync(Stream target, TransportContext? context) => CopyAsync(target, CancellationToken.None);
        protected override Task SerializeToStreamAsync(Stream target, TransportContext? context, CancellationToken cancellationToken) => CopyAsync(target, cancellationToken);
        private async Task CopyAsync(Stream target, CancellationToken ct)
        {
            var buffer = System.Buffers.ArrayPool<byte>.Shared.Rent(64 * 1024);
            try
            {
                var remaining = _length;
                while (remaining > 0)
                {
                    var read = await source.ReadAsync(buffer.AsMemory(0, (int)Math.Min(buffer.Length, remaining)), ct).ConfigureAwait(false);
                    if (read == 0) throw new ClientException("Исходный файл неожиданно закончился");
                    await target.WriteAsync(buffer.AsMemory(0, read), ct).ConfigureAwait(false);
                    remaining -= read;
                }
            }
            finally { System.Buffers.ArrayPool<byte>.Shared.Return(buffer); }
        }
    }
}
