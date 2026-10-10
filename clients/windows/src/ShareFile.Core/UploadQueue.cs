namespace ShareFile.Core;

public sealed record BatchCompletion(IReadOnlyList<string> Names, IReadOnlyList<string> Links, bool Notify);
public sealed record BatchFailure(int FailedCount, int TotalCount, bool Notify);

// One worker owns network transfers. Mutations and durable checkpoints share a gate;
// callbacks receive immutable snapshots and never run under that gate.
public sealed class UploadQueue(IStateStore store, UploadEngine engine, LinkHistory? history = null) : IAsyncDisposable
{
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly CancellationTokenSource _lifetime = new();
    private readonly HashSet<string> _completedBatches = [];
    private readonly HashSet<string> _reportedFailures = [];
    private ClientState _state = new();
    private IShareFileApi? _api;
    private string _password = "";
    private Task _worker = Task.CompletedTask;
    private bool _running;
    private string? _activeId;
    private CancellationTokenSource? _active;
    private bool _loaded;
    private readonly object _progressLock = new();
    private readonly Dictionary<string, TransferMeter> _meters = [];
    public double TransferRate(string id)
    {
        lock (_progressLock)
            return _state.Jobs.Any(j => j.Id == id && j.Status == UploadStatus.Uploading) && _meters.TryGetValue(id, out var meter) ? meter.Rate : 0;
    }
    public LinkHistory? History => history;
    public ClientState State => _state;
    public bool Connected => _api is not null;
    public event Action<ClientState>? Changed;
    public event Action<BatchCompletion>? Completed;
    public event Action<BatchFailure>? BatchFailed;
    public event Action<string>? Failed;

    private async Task CommitAsync(ClientState next, CancellationToken ct)
    {
        await store.SaveAsync(next, ct).ConfigureAwait(false);
        _state = next;
        lock (_progressLock)
            foreach (var id in _meters.Keys.Where(id => !next.Jobs.Any(j => j.Id == id)).ToArray()) _meters.Remove(id);
    }
    private void Publish() => Changed?.Invoke(_state);
    private async Task UpdateAsync(string id, Func<UploadJob, UploadJob> change, CancellationToken ct)
    {
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try { await CommitAsync(_state with { Jobs = _state.Jobs.Select(j => j.Id == id ? change(j) : j).ToArray() }, ct).ConfigureAwait(false); }
        finally { _gate.Release(); }
        Publish();
    }

    public async Task LoadAsync(CancellationToken ct = default)
    {
        if (history is not null)
        {
            try { await history.LoadAsync(ct).ConfigureAwait(false); }
            catch (ClientException error) { Failed?.Invoke(error.Message); }
        }
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            var state = await store.LoadAsync(ct).ConfigureAwait(false);
            var jobs = state.Jobs.Select(j => j.Status switch
            {
                UploadStatus.Queued or UploadStatus.Uploading or UploadStatus.Cancelling => j with { Status = UploadStatus.Paused },
                UploadStatus.Sharing => j with { Status = UploadStatus.Uncertain, Message = "Сервер мог создать ссылку. Проверьте ссылки в Web UI перед созданием новой" },
                _ => j
            }).ToArray();
            await CommitAsync(state with { Jobs = jobs }, ct).ConfigureAwait(false);
            _loaded = true;
        }
        finally { _gate.Release(); }
        Publish();
    }

    public async Task AttachAsync(IShareFileApi api, string password, CancellationToken ct = default)
    {
        var session = await api.MeAsync(ct).ConfigureAwait(false);
        Protocol.RequireIdentifier(session.User.Id);
        Protocol.RequireIdentifier(session.User.HomeId);
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (!_loaded) throw new ClientException("Очередь ещё не загружена");
            if (_running) throw new ClientException("Приостановите очередь перед сменой подключения");
            var settings = _state.Settings;
            if (_state.Jobs.Count != 0 && (settings.Origin != api.Origin || settings.Owner != session.User.Id))
                throw new ClientException("Очередь принадлежит другому серверу или аккаунту. Завершите или отмените её перед сменой подключения");
            var resetFolder = settings.Origin != api.Origin || settings.Owner != session.User.Id || settings.Folder.Length == 0;
            settings = settings with { Origin = api.Origin, Owner = session.User.Id, Username = session.User.Username };
            if (resetFolder)
            {
                var folders = await api.FoldersAsync(session.User.HomeId, ct).ConfigureAwait(false);
                var uploads = folders.FirstOrDefault(n => n.Name == "Uploads" && n.Role >= 3);
                settings = settings with { Folder = uploads?.Id ?? session.User.HomeId, FolderName = uploads?.Name ?? "Личная папка" };
            }
            await CommitAsync(_state with { Settings = settings }, ct).ConfigureAwait(false);
            _api = api;
            _password = password;
        }
        finally { _gate.Release(); }
        Publish();
    }

    public async Task DetachAsync(CancellationToken ct = default)
    {
        await PauseAllAsync(ct).ConfigureAwait(false);
        await _worker.WaitAsync(ct).ConfigureAwait(false);
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try { _api = null; _password = ""; }
        finally { _gate.Release(); }
        Publish();
    }

    public async Task ConfigureAsync(ClientSettings settings, string password, CancellationToken ct = default)
    {
        if (settings.Days is < 1 or > 365 || (password.Length != 0 && System.Text.Encoding.UTF8.GetByteCount(password) is < 12 or > 256))
            throw new ClientException("Срок ссылки: 1–365 дней; пароль: 12–256 байт UTF-8");
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (settings.Origin != _state.Settings.Origin || settings.Owner != _state.Settings.Owner)
                throw new ClientException("Подключение меняется только после подтверждения в браузере");
            if (settings.Folder.Length != 0) Protocol.RequireIdentifier(settings.Folder);
            await CommitAsync(_state with { Settings = settings }, ct).ConfigureAwait(false);
            _password = password;
        }
        finally { _gate.Release(); }
        Publish();
    }

    public async Task AddAsync(IEnumerable<string> paths, CancellationToken ct = default)
    {
        var settings = _state.Settings;
        if (_api is null) throw new ClientException("Сначала подключите хранилище");
        var batch = UploadEngine.NewId();
        var prepared = new List<UploadJob>();
        var errors = new List<string>();
        foreach (var path in paths.Take(101))
        {
            try
            {
                var job = await UploadEngine.PrepareAsync(path, settings.Folder, ct).ConfigureAwait(false);
                prepared.Add(job with { Batch = batch, Days = settings.Days, Never = settings.Never });
            }
            catch (Exception error) when (error is IOException or UnauthorizedAccessException or ClientException)
            { errors.Add(Path.GetFileName(path) + ": " + SafeMessage(error)); }
        }
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (_api is null || _state.Settings.Origin != settings.Origin || _state.Settings.Owner != settings.Owner)
                throw new ClientException("Подключение изменилось во время подготовки файлов. Добавьте файлы заново");
            if (_state.Jobs.Count + prepared.Count > 100) throw new ClientException("В очереди может быть не более 100 файлов или папок. Удалите завершённые записи");
            await CommitAsync(_state with { Jobs = _state.Jobs.Concat(prepared).ToArray() }, ct).ConfigureAwait(false);
            StartWorker();
        }
        finally { _gate.Release(); }
        Publish();
        if (errors.Count != 0) Failed?.Invoke(string.Join(Environment.NewLine, errors));
    }

    // Called under _gate. The task catches all errors and reports safe, capability-free messages.
    private void StartWorker()
    {
        if (_running || _api is null || _lifetime.IsCancellationRequested || !_state.Jobs.Any(j => j.Status == UploadStatus.Queued)) return;
        _running = true;
        _worker = Task.Run(RunAsync);
    }

    private async Task RunAsync()
    {
        var faulted = false;
        try
        {
            while (!_lifetime.IsCancellationRequested)
            {
                UploadJob job;
                IShareFileApi api;
                string password;
                CancellationTokenSource active;
                await _gate.WaitAsync(_lifetime.Token).ConfigureAwait(false);
                try
                {
                    var next = _state.Jobs.FirstOrDefault(j => j.Status == UploadStatus.Queued);
                    if (next is null || _api is null) return;
                    job = next;
                    api = _api;
                    password = _password;
                    _activeId = job.Id;
                    _active = active = CancellationTokenSource.CreateLinkedTokenSource(_lifetime.Token);
                }
                finally { _gate.Release(); }
                try
                {
                    lock (_progressLock) _meters[job.Id] = new TransferMeter();
                    await UpdateAsync(job.Id, j => j with { Status = UploadStatus.Uploading, Message = "", Transferred = 0 }, active.Token).ConfigureAwait(false);
                    job = await engine.UploadAsync(api, job, (progress, ct) =>
                    {
                        lock (_progressLock) _meters[job.Id].Record(progress.Transferred);
                        return UpdateAsync(job.Id, j => j with { Upload = progress.Upload, FileId = progress.FileId, Offset = progress.Offset, Transferred = progress.Transferred,
                            Directory = progress.Directory ?? j.Directory }, ct);
                    }, active.Token).ConfigureAwait(false);
                    await UpdateAsync(job.Id, j => j with { Status = UploadStatus.Uploaded }, active.Token).ConfigureAwait(false);
                    await CreateLinkAsync(api, job, password, active.Token).ConfigureAwait(false);
                }
                catch (Exception error)
                {
                    await UpdateAsync(job.Id, j => j.Status == UploadStatus.Sharing
                        ? j with { Status = UploadStatus.Uncertain, Message = "Сервер мог создать ссылку. Проверьте ссылки в Web UI перед созданием новой" }
                        : j with
                        {
                            Status = error is OperationCanceledException ? UploadStatus.Paused : UploadStatus.Error,
                            Message = error is OperationCanceledException ? "" : SafeMessage(error)
                        }, CancellationToken.None).ConfigureAwait(false);
                }
                finally
                {
                    await _gate.WaitAsync().ConfigureAwait(false);
                    try { active.Dispose(); _active = null; _activeId = null; }
                    finally { _gate.Release(); }
                }
                FinishBatch(job.Batch);
            }
        }
        catch (Exception error) { faulted = true; Failed?.Invoke(SafeMessage(error)); }
        finally
        {
            await _gate.WaitAsync().ConfigureAwait(false);
            try { _running = false; if (!faulted) StartWorker(); }
            finally { _gate.Release(); }
        }
    }

    private async Task CreateLinkAsync(IShareFileApi api, UploadJob job, string password, CancellationToken ct)
    {
        await UpdateAsync(job.Id, j => j with { Status = UploadStatus.Sharing, Message = "" }, ct).ConfigureAwait(false);
        // This operation is never retried. Persist Sharing before sending its one-time request.
        var link = await api.ShareAsync(job.FileId, new ShareOptions(job.Days, job.Never, password), ct).ConfigureAwait(false);
        var warning = await RememberLinkAsync(job, link).ConfigureAwait(false);
        await UpdateAsync(job.Id, j => j with { Status = UploadStatus.Done, Link = link.Url, ShareId = link.Share.Id, Message = warning }, CancellationToken.None).ConfigureAwait(false);
    }

    private void FinishBatch(string batch)
    {
        var jobs = _state.Jobs.Where(j => j.Batch == batch).ToArray();
        var failed = jobs.Count(j => j.Status is UploadStatus.Error or UploadStatus.Uncertain);
        if (failed != 0 && jobs.All(j => j.Status is UploadStatus.Done or UploadStatus.Error or UploadStatus.Uncertain) && _reportedFailures.Add(batch))
            BatchFailed?.Invoke(new BatchFailure(failed, jobs.Length, _state.Settings.NotifyUploads));
        if (jobs.Length == 0 || jobs.Any(j => j.Status != UploadStatus.Done || j.Link.Length == 0) || !_completedBatches.Add(batch)) return;
        Completed?.Invoke(new BatchCompletion(jobs.Select(j => j.Name).ToArray(), jobs.Select(j => j.Link).ToArray(), _state.Settings.NotifyUploads));
    }

    public async Task ResumeAsync(string id, CancellationToken ct = default)
    {
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (_activeId == id) throw new ClientException("Дождитесь остановки текущей операции");
            if (_api is null) throw new ClientException("Подключите хранилище");
            var job = _state.Jobs.Single(j => j.Id == id);
            if (job.Status is not (UploadStatus.Paused or UploadStatus.Error)) return;
            await CommitAsync(_state with { Jobs = _state.Jobs.Select(j => j.Id == id ? j with { Status = UploadStatus.Queued, Message = "" } : j).ToArray() }, ct).ConfigureAwait(false);
            StartWorker();
        }
        finally { _gate.Release(); }
        Publish();
    }

    public async Task PauseAllAsync(CancellationToken ct = default)
    {
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            await CommitAsync(_state with
            {
                Jobs = _state.Jobs.Select(j => j.Status is UploadStatus.Queued or UploadStatus.Uploading
                ? j with { Status = UploadStatus.Paused } : j).ToArray()
            }, ct).ConfigureAwait(false);
            _active?.Cancel();
        }
        finally { _gate.Release(); }
        Publish();
    }

    public async Task RemoveAsync(string id, CancellationToken ct = default)
    {
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (_running) throw new ClientException("Приостановите очередь и дождитесь завершения операции");
            var job = _state.Jobs.Single(j => j.Id == id);
            if (job.Status is UploadStatus.Done or UploadStatus.Uploaded or UploadStatus.Uncertain)
            {
                await CommitAsync(_state with { Jobs = _state.Jobs.Where(j => j.Id != id).ToArray() }, ct).ConfigureAwait(false);
            }
            else
            {
                if (_api is null) throw new ClientException("Для отмены серверной загрузки нужно подключение");
                // Keep the checkpoint on disk until server cancellation is confirmed.
                await engine.CancelAsync(_api, job, ct).ConfigureAwait(false);
                await CommitAsync(_state with { Jobs = _state.Jobs.Where(j => j.Id != id).ToArray() }, ct).ConfigureAwait(false);
            }
        }
        finally { _gate.Release(); }
        Publish();
    }

    public async Task NewLinkAsync(string id, CancellationToken ct = default)
    {
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (_running || _api is null) throw new ClientException("Приостановите очередь перед созданием новой ссылки");
            var job = _state.Jobs.Single(j => j.Id == id);
            if (job.Status is not (UploadStatus.Done or UploadStatus.Uploaded or UploadStatus.Uncertain))
                throw new ClientException("Сначала завершите загрузку файла");
            await CommitAsync(_state with { Jobs = _state.Jobs.Select(j => j.Id == id ? j with { Status = UploadStatus.Sharing } : j).ToArray() }, ct).ConfigureAwait(false);
            try
            {
                var link = await _api.ShareAsync(job.FileId, new ShareOptions(job.Days, job.Never, _password), ct).ConfigureAwait(false);
                var warning = await RememberLinkAsync(job, link).ConfigureAwait(false);
                await CommitAsync(_state with
                {
                    Jobs = _state.Jobs.Select(j => j.Id == id
                    ? j with { Status = UploadStatus.Done, Link = link.Url, ShareId = link.Share.Id, Message = warning } : j).ToArray()
                }, CancellationToken.None).ConfigureAwait(false);
            }
            catch
            {
                await CommitAsync(_state with
                {
                    Jobs = _state.Jobs.Select(j => j.Id == id ? j with
                    {
                        Status = UploadStatus.Uncertain,
                        Message = "Сервер мог создать ссылку. Проверьте ссылки в Web UI"
                    } : j).ToArray()
                }, CancellationToken.None).ConfigureAwait(false);
                throw;
            }
        }
        finally { _gate.Release(); Publish(); }
    }

    public Task WaitForIdleAsync(CancellationToken ct = default) => _worker.WaitAsync(ct);

    private async Task<string> RememberLinkAsync(UploadJob job, ShareLink link)
    {
        if (history is null) return "";
        try { await history.RecordAsync(_state.Settings, job, link).ConfigureAwait(false); return ""; }
        catch
        {
            const string message = "Ссылка создана, но не сохранена в защищённой истории. Скопируйте её из очереди";
            return message;
        }
    }

    private async Task RevokeShareAsync(string shareId, CancellationToken ct)
    {
        if (_api is null) throw new ClientException("Подключите тот же сервер и аккаунт для отзыва ссылки");
        await _api.RevokeShareAsync(shareId, ct).ConfigureAwait(false);
        // Confirmed revocation invalidates in-memory links even if the local disk fails.
        _state = _state with { Jobs = _state.Jobs.Select(j => j.ShareId == shareId ? j with { Link = "", ShareId = "" } : j).ToArray() };
        try
        {
            try { await store.SaveAsync(_state, CancellationToken.None).ConfigureAwait(false); }
            finally
            {
                if (history is not null) await history.MarkRevokedAsync(_state.Settings.Origin, _state.Settings.Owner, shareId).ConfigureAwait(false);
            }
        }
        catch { throw new ClientException("Ссылка отозвана на сервере, но локальная история не сохранена. Проверьте Credential Manager и диск"); }
    }

    public async Task RevokeLinkAsync(string id, CancellationToken ct = default)
    {
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            var job = _state.Jobs.Single(j => j.Id == id);
            if (job.ShareId.Length == 0) throw new ClientException("У этой записи нет известной ссылки");
            await RevokeShareAsync(job.ShareId, ct).ConfigureAwait(false);
        }
        finally { _gate.Release(); Publish(); }
    }

    public async Task RevokeHistoryAsync(SentLink entry, CancellationToken ct = default)
    {
        await _gate.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            if (entry.Origin != _state.Settings.Origin || entry.Owner != _state.Settings.Owner || history is null ||
                !history.Entries.Any(e => e.Account == entry.Account && !e.Revoked))
                throw new ClientException("Для отзыва подключите тот же сервер и аккаунт");
            await RevokeShareAsync(entry.ShareId, ct).ConfigureAwait(false);
        }
        finally { _gate.Release(); Publish(); }
    }
    public static string SafeMessage(Exception error) => error switch
    {
        ClientException or ApiException => error.Message,
        OperationCanceledException => "Операция отменена или сервер не ответил вовремя",
        UnauthorizedAccessException => "Нет доступа к файлу или каталогу приложения",
        IOException => "Не удалось прочитать файл или сохранить очередь. Проверьте диск",
        _ => "Не удалось выполнить операцию. Проверьте подключение и попробуйте снова"
    };

    public async ValueTask DisposeAsync()
    {
        _lifetime.Cancel();
        await _worker.ConfigureAwait(false);
        _active?.Dispose();
        _lifetime.Dispose();
        _gate.Dispose();
    }
}
