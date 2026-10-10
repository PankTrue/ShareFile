using System.Text.Json;
using System.Text.Json.Serialization;

namespace ShareFile.Core;

public interface IStateStore
{
    Task<ClientState> LoadAsync(CancellationToken ct = default);
    Task SaveAsync(ClientState state, CancellationToken ct = default);
}

public sealed class JsonStateStore(string path) : IStateStore
{
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web)
    {
        WriteIndented = true,
        Converters = { new JsonStringEnumConverter<UploadStatus>() }
    };
    public async Task<ClientState> LoadAsync(CancellationToken ct = default)
    {
        if (!File.Exists(path)) return new ClientState();
        if (new FileInfo(path).Length > 64 * 1024 * 1024) throw new ClientException(Localization.Text("Файл очереди слишком большой"));
        await using var file = File.OpenRead(path);
        try
        {
            var state = await JsonSerializer.DeserializeAsync<ClientState>(file, Json, ct).ConfigureAwait(false);
            if (state is null || state.Version != 1 || state.Settings is null || state.Jobs is null || state.Jobs.Count > 100)
                throw new ClientException(Localization.Text("Неизвестный формат очереди. Сохраните state.json перед восстановлением"));
            foreach (var job in state.Jobs) UploadEngine.Validate(job);
            if (state.Jobs.Select(j => j.Id).Distinct().Count() != state.Jobs.Count)
                throw new ClientException(Localization.Text("Повторяющиеся записи в очереди"));
            return state with { Settings = state.Settings with { Language = Localization.Normalize(state.Settings.Language) } };
        }
        catch (JsonException) { throw new ClientException(Localization.Text("Не удалось прочитать очередь. Сохраните state.json перед восстановлением")); }
    }

    public async Task SaveAsync(ClientState state, CancellationToken ct = default)
    {
        var directory = Path.GetDirectoryName(Path.GetFullPath(path))!;
        Directory.CreateDirectory(directory);
        var staging = Path.Combine(directory, ".state-" + Guid.NewGuid().ToString("N") + ".tmp");
        try
        {
            await using (var file = new FileStream(staging, FileMode.CreateNew, FileAccess.Write, FileShare.None,
                16384, FileOptions.Asynchronous | FileOptions.WriteThrough))
            {
                await JsonSerializer.SerializeAsync(file, state, Json, ct).ConfigureAwait(false);
                if (file.Length > 64 * 1024 * 1024) throw new ClientException(Localization.Text("Очередь слишком большая. Удалите завершённые записи"));
                await file.FlushAsync(ct).ConfigureAwait(false);
                file.Flush(flushToDisk: true);
            }
            File.Move(staging, path, overwrite: true);
        }
        finally { if (File.Exists(staging)) File.Delete(staging); }
    }
}
