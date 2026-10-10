using System.Windows;
using Microsoft.Extensions.DependencyInjection;
using ShareFile.Core;
using ShareFile.Windows.Platform;
using ShareFile.Windows.ViewModels;

namespace ShareFile.Windows;

public partial class App : Application
{
    private SingleInstance? _instance;
    private ServiceProvider? _services;
    private MainViewModel? _model;
    private bool _exiting;
    private ClipboardHotKey? _clipboardHotKey;

    protected override async void OnStartup(StartupEventArgs e)
    {
        base.OnStartup(e);
        try
        {
            var request = LaunchRequest.Parse(e.Args);
            _instance = new SingleInstance();
            if (!_instance.Acquire())
            {
                await _instance.ForwardAsync(request);
                await _instance.DisposeAsync(); _instance = null;
                Shutdown(); return;
            }
            var directory = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "ShareFile");
            _services = new ServiceCollection()
                .AddSingleton<IStateStore>(new JsonStateStore(Path.Combine(directory, "state.json")))
                .AddSingleton<LinkHistory>(services => new LinkHistory(Path.Combine(directory, "history.json"), services.GetRequiredService<ICredentialStore>()))
                .AddSingleton<UploadEngine>().AddSingleton<UploadQueue>()
                .AddSingleton(new ClipboardImages(Path.Combine(directory, "clipboard")))
                .AddSingleton<ICredentialStore, CredentialStore>()
                .AddSingleton<DesktopIntegration>().AddSingleton<MainViewModel>()
                .BuildServiceProvider(new ServiceProviderOptions { ValidateOnBuild = true, ValidateScopes = true });
            _model = _services.GetRequiredService<MainViewModel>();
            var window = new MainWindow(_model);
            MainWindow = window;
            window.Closing += (_, args) => { if (!_exiting) { args.Cancel = true; window.Hide(); } };
            void ShowWindow() { window.Show(); if (window.WindowState == WindowState.Minimized) window.WindowState = WindowState.Normal; window.Activate(); }
            _model.ShowRequested = ShowWindow;
            var desktop = _services.GetRequiredService<DesktopIntegration>();
            desktop.OpenRequested = ShowWindow;
            desktop.HistoryRequested = () => { _model.SelectedTab = 2; ShowWindow(); };
            desktop.RecentLinks = () => _model.HistoryEntries.Take(8).ToArray();
            desktop.CopyHistoryRequested = entry => _ = _model.CopyHistoryEntryAsync(entry);
            desktop.OpenHistoryRequested = entry => _ = _model.OpenHistoryEntryAsync(entry);
            desktop.RevokeHistoryRequested = entry => _ = _model.RevokeHistoryEntryAsync(entry);
            desktop.CanRevokeHistory = entry => _model.IsConnected && !entry.Revoked;
            desktop.ExitRequested = () => _ = ExitAsync();
            desktop.ClipboardRequested = () => _model.UploadClipboardCommand.Execute(null);
            await _model.InitializeAsync();
            _model.LoadSettings();
            try { _clipboardHotKey = new ClipboardHotKey(() => _model.UploadClipboardCommand.Execute(null)); }
            catch (ClientException error) { _model.Notice = error.Message; }
            _instance.Listen(async forwarded => await Dispatcher.InvokeAsync(async () =>
            {
                if (forwarded.Show) ShowWindow();
                if (forwarded.Paths.Length != 0) await _model.AcceptFilesAsync(forwarded.Paths, true);
            }).Task.Unwrap(), error => Dispatcher.BeginInvoke(() => _model.Notice = error));
            if (request.Show) ShowWindow();
            if (request.Paths.Length != 0) await _model.AcceptFilesAsync(request.Paths, true);
        }
        catch (Exception error)
        {
            MessageBox.Show(UploadQueue.SafeMessage(error), "ShareFile", MessageBoxButton.OK, MessageBoxImage.Error);
            await ExitAsync();
        }
    }

    private async Task ExitAsync()
    {
        if (_exiting) return;
        _exiting = true;
        _clipboardHotKey?.Dispose(); _clipboardHotKey = null;
        try
        {
            if (_model is not null) await _model.ShutdownAsync();
            if (_instance is not null) await _instance.DisposeAsync();
            if (_services is not null) await _services.DisposeAsync();
        }
        catch (Exception error) { MessageBox.Show(UploadQueue.SafeMessage(error), "ShareFile"); }
        finally { Shutdown(); }
    }
}
