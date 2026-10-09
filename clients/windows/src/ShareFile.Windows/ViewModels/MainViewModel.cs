using System.Collections.ObjectModel;
using System.Windows;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Microsoft.Win32;
using ShareFile.Core;
using ShareFile.Windows.Platform;

namespace ShareFile.Windows.ViewModels;

public sealed record UploadRow(UploadJob Job)
{
    public string Name => Job.Name;
    public double Progress => Job.Size == 0 ? (Job.Status == UploadStatus.Done ? 100 : 0) : 100.0 * Job.Offset / Job.Size;
    public string Bytes => $"{Job.Offset / 1048576.0:F1} / {Job.Size / 1048576.0:F1} MiB";
    public string Message => Job.Message;
    public string Status => Job.Status switch
    {
        UploadStatus.Queued => "В очереди",
        UploadStatus.Uploading => "Загружается",
        UploadStatus.Paused => "На паузе",
        UploadStatus.Uploaded => "Загрузка завершена",
        UploadStatus.Sharing => "Создаётся ссылка",
        UploadStatus.Done => "Готово",
        UploadStatus.Uncertain => "Проверьте ссылки в Web UI",
        UploadStatus.Cancelling => "Отменяется",
        _ => "Нужна ваша помощь"
    };
}

public sealed partial class MainViewModel : ObservableObject
{
    private readonly UploadQueue _queue;
    private readonly ICredentialStore _credentials;
    private readonly DesktopIntegration _desktop;
    private readonly List<string> _pending = [];
    private ShareFileApi? _api;
    private readonly Stack<Node> _trail = [];
    public Action? ShowRequested { get; set; }
    public ObservableCollection<UploadRow> Jobs { get; } = [];
    public IReadOnlyList<SentLink> HistoryEntries => _queue.History?.ForAccount(_queue.State.Settings.Origin, _queue.State.Settings.Owner) ?? [];
    public ObservableCollection<SentLink> HistoryRows { get; } = [];
    [ObservableProperty] private string _historySearch = "";
    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(CopyHistoryCommand), nameof(OpenHistoryCommand), nameof(RevokeHistoryCommand))]
    private SentLink? _selectedHistory;
    partial void OnHistorySearchChanged(string value) => RefreshHistory();
    private bool CanReadHistory() => SelectedHistory?.Active == true;
    private bool CanRevokeHistory() => IsConnected && SelectedHistory is { Revoked: false };
    public ObservableCollection<Node> Folders { get; } = [];
    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(ResumeCommand), nameof(RemoveCommand), nameof(NewLinkCommand), nameof(CopyLinkCommand), nameof(RevokeLinkCommand))]
    private UploadRow? _selectedJob;
    [ObservableProperty] private Node? _selectedFolder;
    [ObservableProperty] private string _serverInput = "https://192.168.1.1:8081";
    [ObservableProperty] private string _notice = "";
    [ObservableProperty] private string _username = "Не подключено";
    [ObservableProperty] private string _folderName = "Uploads";
    [ObservableProperty] private int _days = 7;
    [ObservableProperty] private bool _never;
    [ObservableProperty] private bool _silentExplorerUploads = true;
    [ObservableProperty] private bool _notifyUploads = true;
    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(ConnectCommand), nameof(DisconnectCommand), nameof(ResumeCommand), nameof(NewLinkCommand), nameof(RevokeLinkCommand), nameof(RevokeHistoryCommand))]
    private bool _isConnected;
    [ObservableProperty] private int _selectedTab;
    [ObservableProperty] private string _pairingUrl = "";
    [ObservableProperty] private string _linkPassword = "";
    private bool CanConnect() => !IsConnected;
    private bool CanDisconnect() => IsConnected;
    private bool CanResume() => IsConnected && SelectedJob?.Job.Status is UploadStatus.Paused or UploadStatus.Error;
    private bool CanNewLink() => IsConnected && SelectedJob?.Job.Status is UploadStatus.Done or UploadStatus.Uploaded or UploadStatus.Uncertain;
    private bool CanCopyLink() => SelectedJob is { } row && (row.Job.Link.Length > 0 || HistoryEntries.Any(e => e.ShareId == row.Job.ShareId && e.Active));
    private bool CanRevokeLink() => IsConnected && SelectedJob?.Job.ShareId.Length > 0;
    private bool CanRemove() => SelectedJob is not null;
    private static string TokenAccount(string origin) => "device:" + origin;
    private static string PasswordAccount(string origin) => "password:" + origin;

    public MainViewModel(UploadQueue queue, ICredentialStore credentials, DesktopIntegration desktop)
    {
        _queue = queue; _credentials = credentials; _desktop = desktop;
        if (_queue.History is { } history) history.Changed += () => Application.Current.Dispatcher.BeginInvoke(RefreshHistory);
        _queue.Changed += state => Application.Current.Dispatcher.BeginInvoke(() => Refresh(state));
        _queue.Failed += error => Application.Current.Dispatcher.BeginInvoke(() =>
        {
            Notice = error;
            _desktop.NotifyFailure(error, _queue.State.Settings.NotifyUploads);
        });
        _queue.BatchFailed += batch => Application.Current.Dispatcher.BeginInvoke(() =>
        {
            Notice = $"Не удалось завершить объектов: {batch.FailedCount} из {batch.TotalCount}. Откройте очередь и проверьте состояние";
            _desktop.NotifyFailure(Notice, batch.Notify);
        });
        _queue.Completed += batch => Application.Current.Dispatcher.BeginInvoke(async () =>
        {
            var copied = await _desktop.CopyAsync(string.Join(Environment.NewLine, batch.Links));
            Notice = copied ? $"Загружено объектов: {batch.Names.Count}. Ссылки скопированы" : "Загрузка завершена. Буфер обмена недоступен — нажмите «Скопировать ссылку»";
            _desktop.Notify(batch, copied);
        });
    }

    public async Task InitializeAsync()
    {
        await _queue.LoadAsync();
        var state = _queue.State;
        Refresh(state);
        if (state.Settings.Origin.Length == 0) return;
        ServerInput = state.Settings.Origin;
        try
        {
            var token = await Task.Run(() => _credentials.Read(TokenAccount(ServerInput)));
            if (token is null) return;
            LinkPassword = await Task.Run(() => _credentials.Read(PasswordAccount(ServerInput))) ?? "";
            var api = new ShareFileApi(ServerInput, token);
            try { await _queue.AttachAsync(api, LinkPassword); }
            catch { api.Dispose(); throw; }
            _api = api;
            IsConnected = true;
        }
        catch (Exception error) { Notice = UploadQueue.SafeMessage(error); }
    }

    private void Refresh(ClientState state)
    {
        var selected = SelectedJob?.Job.Id;
        Jobs.Clear();
        foreach (var job in state.Jobs) Jobs.Add(new UploadRow(job));
        SelectedJob = Jobs.FirstOrDefault(j => j.Job.Id == selected);
        Username = _queue.Connected ? state.Settings.Username : "Не подключено";
        FolderName = state.Settings.FolderName;
        RefreshHistory();
        // Editable settings are initialized once; progress updates must not overwrite unsaved input.
    }
    private void RefreshHistory()
    {
        var selected = SelectedHistory?.Account;
        HistoryRows.Clear();
        foreach (var entry in HistoryEntries.Where(e => e.Name.Contains(HistorySearch.Trim(), StringComparison.CurrentCultureIgnoreCase))) HistoryRows.Add(entry);
        SelectedHistory = HistoryRows.FirstOrDefault(e => e.Account == selected);
    }
    public Task CopyHistoryEntryAsync(SentLink entry) => GuardAsync(async () =>
    {
        if (_queue.History is null) return;
        var link = await _queue.History.GetLinkAsync(entry);
        Notice = await _desktop.CopyAsync(link) ? "Ссылка скопирована" : "Буфер обмена недоступен. Попробуйте снова";
    });
    public Task OpenHistoryEntryAsync(SentLink entry) => GuardAsync(async () =>
    {
        if (_queue.History is { } history) DesktopIntegration.OpenBrowser(await history.GetLinkAsync(entry));
    });
    public Task RevokeHistoryEntryAsync(SentLink entry) => GuardAsync(async () =>
    {
        await _queue.RevokeHistoryAsync(entry);
        Notice = "Ссылка отозвана";
    });
    [RelayCommand(CanExecute = nameof(CanReadHistory))]
    private async Task CopyHistoryAsync() { if (SelectedHistory is { } entry) await CopyHistoryEntryAsync(entry); }
    [RelayCommand(CanExecute = nameof(CanReadHistory))]
    private async Task OpenHistoryAsync() { if (SelectedHistory is { } entry) await OpenHistoryEntryAsync(entry); }
    [RelayCommand(CanExecute = nameof(CanRevokeHistory))]
    private async Task RevokeHistoryAsync() { if (SelectedHistory is { } entry) await RevokeHistoryEntryAsync(entry); }

    public void LoadSettings()
    {
        var settings = _queue.State.Settings;
        Days = settings.Days; Never = settings.Never;
        SilentExplorerUploads = settings.SilentExplorerUploads; NotifyUploads = settings.NotifyUploads;
    }

    [RelayCommand(IncludeCancelCommand = true, CanExecute = nameof(CanConnect))]
    private async Task ConnectAsync(CancellationToken ct)
    {
        if (IsConnected) { Notice = "Сначала отключите текущее подключение"; return; }
        try
        {
            var origin = Protocol.NormalizeOrigin(ServerInput);
            if (_queue.State.Jobs.Count != 0 && origin != _queue.State.Settings.Origin)
                throw new ClientException("Очередь связана с текущим сервером. Завершите или отмените её перед сменой адреса");
            using var anonymous = new ShareFileApi(origin);
            var name = "Windows · " + Environment.MachineName;
            var pairing = await anonymous.StartPairingAsync(name[..Math.Min(name.Length, 80)], ct);
            PairingUrl = pairing.VerificationUrl;
            DesktopIntegration.OpenBrowser(PairingUrl);
            Notice = "Подтвердите подключение в браузере";
            var expires = DateTimeOffset.FromUnixTimeSeconds(pairing.Request.ExpiresAt);
            while (DateTimeOffset.UtcNow < expires)
            {
                await Task.Delay(TimeSpan.FromSeconds(pairing.Interval), ct);
                var token = await anonymous.PollPairingAsync(pairing.Request, ct);
                if (token is null) continue;
                LinkPassword = await Task.Run(() => _credentials.Read(PasswordAccount(origin)), ct) ?? "";
                var api = new ShareFileApi(origin, token);
                try
                {
                    await _queue.AttachAsync(api, LinkPassword, ct);
                    await Task.Run(() => _credentials.Write(TokenAccount(origin), token), ct);
                }
                catch { await _queue.DetachAsync(CancellationToken.None); api.Dispose(); throw; }
                _api?.Dispose(); _api = api;
                IsConnected = true; ServerInput = origin; SelectedTab = 0;
                Notice = "Компьютер подключён. Можно загружать файлы и папки";
                await FlushPendingAsync();
                return;
            }
            throw new ClientException("Время подтверждения истекло. Подключитесь заново");
        }
        catch (Exception error) { Notice = UploadQueue.SafeMessage(error); }
        finally { PairingUrl = ""; }
    }

    [RelayCommand]
    private void OpenPairing() { if (PairingUrl.Length != 0) DesktopIntegration.OpenBrowser(PairingUrl); }

    [RelayCommand(CanExecute = nameof(CanDisconnect))]
    private async Task DisconnectAsync() => await GuardAsync(async () =>
    {
        await _queue.DetachAsync();
        IsConnected = false;
        _api?.Dispose(); _api = null;
        await Task.Run(() => _credentials.Delete(TokenAccount(_queue.State.Settings.Origin)));
        Notice = "Локальное подключение удалено. Отозвать устройство можно в Web UI";
    });

    [RelayCommand]
    private async Task SaveOptionsAsync() => await GuardAsync(async () =>
    {
        var settings = _queue.State.Settings with
        {
            Days = Days,
            Never = Never,
            SilentExplorerUploads = SilentExplorerUploads,
            NotifyUploads = NotifyUploads
        };
        var password = LinkPassword;
        if (Days is < 1 or > 365 || (password.Length != 0 && System.Text.Encoding.UTF8.GetByteCount(password) is < 12 or > 256))
            throw new ClientException("Срок ссылки: 1–365 дней; пароль: 12–256 байт UTF-8");
        if (settings.Origin.Length == 0 && password.Length != 0) throw new ClientException("Сначала подключите сервер");
        if (settings.Origin.Length != 0)
            await Task.Run(() =>
            {
                if (password.Length == 0) _credentials.Delete(PasswordAccount(settings.Origin));
                else _credentials.Write(PasswordAccount(settings.Origin), password);
            });
        await _queue.ConfigureAsync(settings, password);
        Notice = "Настройки сохранены";
    });

    [RelayCommand]
    private async Task ChooseFilesAsync()
    {
        var dialog = new OpenFileDialog { Multiselect = true, CheckFileExists = true, Title = "Загрузить и поделиться" };
        if (dialog.ShowDialog() == true) await AcceptFilesAsync(dialog.FileNames, false);
    }

    [RelayCommand]
    private async Task ChooseDirectoryAsync()
    {
        var dialog = new OpenFolderDialog { Multiselect = true, Title = "Загрузить папку целиком" };
        if (dialog.ShowDialog() == true) await AcceptFilesAsync(dialog.FolderNames, false);
    }

    public async Task AcceptFilesAsync(IEnumerable<string> paths, bool explorer)
    {
        var files = paths.ToArray();
        if (!IsConnected)
        {
            if (_pending.Count + files.Length > 100) { Notice = "Слишком много объектов: максимум 100"; ShowRequested?.Invoke(); return; }
            _pending.AddRange(files); SelectedTab = 1;
            Notice = "Подключите хранилище. Выбранные файлы и папки добавятся после подтверждения";
            ShowRequested?.Invoke(); return;
        }
        if (!explorer || !_queue.State.Settings.SilentExplorerUploads) ShowRequested?.Invoke();
        await GuardAsync(() => _queue.AddAsync(files));
    }
    private async Task FlushPendingAsync()
    {
        if (_pending.Count == 0) return;
        var files = _pending.ToArray(); _pending.Clear();
        await _queue.AddAsync(files);
    }

    [RelayCommand] private async Task PauseAllAsync() => await GuardAsync(() => _queue.PauseAllAsync());
    [RelayCommand(CanExecute = nameof(CanResume))] private async Task ResumeAsync() { if (SelectedJob is { } row) await GuardAsync(() => _queue.ResumeAsync(row.Job.Id)); }
    [RelayCommand(CanExecute = nameof(CanRemove))] private async Task RemoveAsync() { if (SelectedJob is { } row) await GuardAsync(() => _queue.RemoveAsync(row.Job.Id)); }
    [RelayCommand(CanExecute = nameof(CanNewLink))]
    private async Task NewLinkAsync()
    {
        if (SelectedJob is not { } row) return;
        if (row.Job.Status == UploadStatus.Uncertain && MessageBox.Show(
            "Сервер мог создать предыдущую ссылку. Проверьте ссылки объекта в Web UI и отзовите неизвестные. Создать новую ссылку?",
            "Новая публичная ссылка", MessageBoxButton.YesNo, MessageBoxImage.Information) != MessageBoxResult.Yes) return;
        await GuardAsync(async () => { await _queue.NewLinkAsync(row.Job.Id); await CopyLinkAsync(); });
    }
    [RelayCommand(CanExecute = nameof(CanCopyLink))]
    private async Task CopyLinkAsync()
    {
        var job = _queue.State.Jobs.FirstOrDefault(j => j.Id == SelectedJob?.Job.Id);
        if (job?.Link is not { Length: > 0 } link)
        {
            var saved = HistoryEntries.FirstOrDefault(e => e.ShareId == job?.ShareId && e.Active);
            if (saved is not null) { await CopyHistoryEntryAsync(saved); return; }
            Notice = "Откройте историю ссылок. Если ссылка там отсутствует, создайте новую";
            return;
        }
        Notice = await _desktop.CopyAsync(link) ? "Ссылка скопирована" : "Буфер обмена недоступен. Попробуйте снова";
    }
    [RelayCommand(CanExecute = nameof(CanRevokeLink))]
    private async Task RevokeLinkAsync()
    {
        if (SelectedJob is not { } row || _api is null || row.Job.ShareId.Length == 0) return;
        await GuardAsync(async () => { await _queue.RevokeLinkAsync(row.Job.Id); Notice = "Ссылка отозвана. Для доступа создайте новую"; });
    }
    [RelayCommand]
    private void OpenWeb()
    {
        if (_queue.State.Settings.Origin.Length != 0) DesktopIntegration.OpenBrowser(_queue.State.Settings.Origin);
    }

    [RelayCommand]
    private async Task BrowseFoldersAsync() => await GuardAsync(async () =>
    {
        if (_api is null) throw new ClientException("Подключите хранилище");
        _trail.Clear(); Folders.Clear();
        foreach (var node in await _api.FoldersAsync("", CancellationToken.None)) Folders.Add(node);
    });
    [RelayCommand]
    private async Task EnterFolderAsync() => await GuardAsync(async () =>
    {
        if (_api is null || SelectedFolder is not { } folder) return;
        var nodes = await _api.FoldersAsync(folder.Id, CancellationToken.None);
        _trail.Push(folder); Folders.Clear(); foreach (var node in nodes) Folders.Add(node);
    });
    [RelayCommand]
    private async Task FolderBackAsync() => await GuardAsync(async () =>
    {
        if (_api is null) return;
        if (_trail.Count > 0) _trail.Pop();
        var nodes = await _api.FoldersAsync(_trail.TryPeek(out var parent) ? parent.Id : "", CancellationToken.None);
        Folders.Clear(); foreach (var node in nodes) Folders.Add(node);
    });
    [RelayCommand]
    private async Task SelectFolderAsync() => await GuardAsync(async () =>
    {
        var folder = SelectedFolder ?? (_trail.TryPeek(out var current) ? current : null);
        if (folder is null || folder.Role < 3) throw new ClientException("Выберите папку с правами управляющего");
        await _queue.ConfigureAsync(_queue.State.Settings with { Folder = folder.Id, FolderName = folder.Name }, LinkPassword);
        Notice = "Папка назначения: " + folder.Name;
    });

    public async Task GuardAsync(Func<Task> action)
    {
        try { await action(); }
        catch (Exception error) { Notice = UploadQueue.SafeMessage(error); }
    }
    public async Task ShutdownAsync()
    {
        ConnectCommand.Cancel();
        if (ConnectCommand.ExecutionTask is { } pairing) await pairing;
        await _queue.PauseAllAsync();
        await _queue.WaitForIdleAsync();
        _api?.Dispose();
    }
}
