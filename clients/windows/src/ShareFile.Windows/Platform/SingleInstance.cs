using System.Buffers.Binary;
using System.Diagnostics;
using System.IO.Pipes;
using System.Security.Cryptography;
using System.Security.Principal;
using System.Text;
using System.Text.Json;
using ShareFile.Core;

namespace ShareFile.Windows.Platform;

public sealed record LaunchRequest(bool Show, string[] Paths)
{
    public static LaunchRequest Parse(string[] args)
    {
        var upload = args.FirstOrDefault() == "--upload";
        if (args.SequenceEqual(new[] { "--background" })) return new LaunchRequest(false, []);
        if (args.Length != 0 && !upload) throw new ClientException("Ожидалось: ShareFile.exe [--upload <файлы>]");
        var paths = upload ? args.Skip(1).ToArray() : [];
        if (paths.Length > 100 || paths.Any(p => !Path.IsPathFullyQualified(p)))
            throw new ClientException("Передайте не более 100 абсолютных путей к файлам");
        return new LaunchRequest(!upload, paths);
    }
}

public sealed class SingleInstance : IAsyncDisposable
{
    private readonly string _name = "ShareFile-" + Convert.ToHexStringLower(SHA256.HashData(
        Encoding.UTF8.GetBytes(WindowsIdentity.GetCurrent().User!.Value + ":" + Process.GetCurrentProcess().SessionId)))[..24];
    private Mutex? _mutex;
    private readonly CancellationTokenSource _stop = new();
    private Task _listener = Task.CompletedTask;
    public bool Acquire()
    {
        // Handle existence is the lease. No thread-affine mutex ownership crosses await boundaries.
        _mutex = new Mutex(initiallyOwned: false, "Local\\" + _name, out var created);
        return created;
    }

    public async Task ForwardAsync(LaunchRequest request)
    {
        using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(20));
        await using var pipe = new NamedPipeClientStream(".", _name, PipeDirection.InOut, PipeOptions.Asynchronous | PipeOptions.CurrentUserOnly);
        await pipe.ConnectAsync(timeout.Token).ConfigureAwait(false);
        var bytes = JsonSerializer.SerializeToUtf8Bytes(request);
        if (bytes.Length > 256 * 1024) throw new ClientException("Слишком длинный список путей");
        var prefix = new byte[4];
        BinaryPrimitives.WriteInt32LittleEndian(prefix, bytes.Length);
        await pipe.WriteAsync(prefix, timeout.Token).ConfigureAwait(false);
        await pipe.WriteAsync(bytes, timeout.Token).ConfigureAwait(false);
        var ack = new byte[1];
        await pipe.ReadExactlyAsync(ack, timeout.Token).ConfigureAwait(false);
        if (ack[0] != 1) throw new ClientException("ShareFile не принял файлы. Откройте приложение и проверьте очередь");
    }

    public void Listen(Func<LaunchRequest, Task> receive, Action<string> failure) => _listener = Task.Run(async () =>
    {
        while (!_stop.IsCancellationRequested)
        {
            try
            {
                await using var pipe = new NamedPipeServerStream(_name, PipeDirection.InOut, 1, PipeTransmissionMode.Byte,
                    PipeOptions.Asynchronous | PipeOptions.CurrentUserOnly);
                await pipe.WaitForConnectionAsync(_stop.Token).ConfigureAwait(false);
                using var timeout = CancellationTokenSource.CreateLinkedTokenSource(_stop.Token);
                timeout.CancelAfter(TimeSpan.FromSeconds(30));
                var prefix = new byte[4];
                await pipe.ReadExactlyAsync(prefix, timeout.Token).ConfigureAwait(false);
                var length = BinaryPrimitives.ReadInt32LittleEndian(prefix);
                if (length is <= 0 or > 256 * 1024) throw new ClientException("Некорректный запрос Проводника");
                var data = new byte[length];
                await pipe.ReadExactlyAsync(data, timeout.Token).ConfigureAwait(false);
                var request = JsonSerializer.Deserialize<LaunchRequest>(data) ?? throw new ClientException("Пустой запрос Проводника");
                if (request.Paths is null || request.Paths.Length > 100 || request.Paths.Any(p => !Path.IsPathFullyQualified(p)))
                    throw new ClientException("Некорректные пути файлов");
                await receive(request).ConfigureAwait(false);
                await pipe.WriteAsync(new byte[] { 1 }, timeout.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (_stop.IsCancellationRequested) { break; }
            catch (Exception error)
            {
                failure(UploadQueue.SafeMessage(error));
                try { await Task.Delay(500, _stop.Token).ConfigureAwait(false); }
                catch (OperationCanceledException) { break; }
            }
        }
    });

    public async ValueTask DisposeAsync()
    {
        _stop.Cancel();
        await _listener.ConfigureAwait(false);
        // Closing the last handle removes the lease, including after process termination.
        _mutex?.Dispose();
        _stop.Dispose();
    }
}
