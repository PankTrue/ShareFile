using System.Windows;
using ShareFile.Core;
using ShareFile.Windows.Platform;
using ShareFile.Windows.ViewModels;

namespace ShareFile.Windows.Tests;

[Collection("Windows integration")]
public sealed class WindowTests
{
    private static async Task RunOnStaAsync(Action action)
    {
        var completed = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var thread = new Thread(() =>
        {
            try
            {
                action();
                completed.SetResult();
            }
            catch (Exception error) { completed.TrySetException(error); }
        }) { IsBackground = true };
        thread.SetApartmentState(ApartmentState.STA); thread.Start();
        await completed.Task.WaitAsync(TimeSpan.FromSeconds(30), TestContext.Current.CancellationToken);
        Assert.True(thread.Join(TimeSpan.FromSeconds(5)), "The STA test thread did not exit.");
    }

    [Fact]
    public Task NativeClipboardSupportsBitmapAndPngAndShortcutIsReleased() => RunOnStaAsync(() =>
    {
        try
        {
            var image = System.Windows.Media.Imaging.BitmapSource.Create(2, 2, 96, 96,
                System.Windows.Media.PixelFormats.Bgra32, null, new byte[16], 8);
            Clipboard.SetImage(image);
            var bitmap = ClipboardUpload.ReadPngAsync().GetAwaiter().GetResult();
            Assert.True(bitmap.AsSpan(0, 8).SequenceEqual(new byte[] { 137, 80, 78, 71, 13, 10, 26, 10 }));
            var data = new DataObject(); data.SetData("PNG", new MemoryStream(bitmap)); Clipboard.SetDataObject(data);
            var png = ClipboardUpload.ReadPngAsync().GetAwaiter().GetResult();
            using var stream = new MemoryStream(png);
            var decoded = System.Windows.Media.Imaging.BitmapFrame.Create(stream);
            Assert.Equal(2, decoded.PixelWidth); Assert.Equal(2, decoded.PixelHeight);
            Clipboard.SetText("not an image");
            Assert.Throws<ClientException>(() => ClipboardUpload.ReadPngAsync().GetAwaiter().GetResult());
            using (var shortcut = new ClipboardHotKey(() => { }))
                Assert.Throws<ClientException>(() => new ClipboardHotKey(() => { }));
            using var rebound = new ClipboardHotKey(() => { });
        }
        finally { Clipboard.Clear(); }
    });

    [Fact]
    public Task WindowLoadsXamlResourcesAndStaysHiddenUntilRequested() => RunOnStaAsync(() =>
    {
        var app = new App(startClient: false);
        var startupProcessed = false;
        app.Startup += (_, _) => startupProcessed = true;
        app.InitializeComponent();
        Task? scenario = null;
        _ = app.Dispatcher.InvokeAsync(() => { scenario = CheckWindowAsync(app, startupProcessed); });
        app.Run();
        Assert.True(app.Dispatcher.HasShutdownFinished);
        Assert.NotNull(scenario);
        scenario.GetAwaiter().GetResult();
    });

    private static async Task CheckWindowAsync(App app, bool startupProcessed)
    {
        var directory = Path.Combine(Path.GetTempPath(), "sharefile-window-test-" + Guid.NewGuid().ToString("N"));
        try
        {
            Assert.True(startupProcessed);
            Assert.Equal(ShutdownMode.OnExplicitShutdown, app.ShutdownMode);
            Assert.IsType<Style>(app.Resources[typeof(System.Windows.Controls.Button)]);
            await using var queue = new UploadQueue(new JsonStateStore(Path.Combine(directory, "state.json")), new UploadEngine());
            using var desktop = new DesktopIntegration();
            var model = new MainViewModel(queue, new CredentialStore(), desktop);
            MainWindow? window = null;
            try
            {
                window = new MainWindow(model);
                Assert.False(window.IsVisible);
                await model.InitializeAsync();
                Assert.Same(window, Assert.Single(app.Windows.Cast<Window>()));
                Assert.False(window.IsVisible);
                model.LoadSettings();
                window.Show(); window.UpdateLayout();
                Assert.True(window.IsVisible); Assert.Same(model, window.DataContext); Assert.True(window.ActualWidth >= 760);
                var tabs = Assert.IsType<System.Windows.Controls.TabControl>(window.FindName("ClientTabs"));
                Assert.Equal(3, tabs.Items.Count);
                tabs.SelectedIndex = 2; window.UpdateLayout();
                Assert.Equal("Link history", Assert.IsType<System.Windows.Controls.TabItem>(tabs.SelectedItem).Header);
                model.ServerInput = "https://unsaved.example";
                model.LinkPassword = "unsaved-password";
                model.Days = 90;
                model.HistorySearch = "日本語";
                await model.ChangeLanguageAsync("ru");
                window.UpdateLayout();
                Assert.Equal("История ссылок", Assert.IsType<System.Windows.Controls.TabItem>(tabs.SelectedItem).Header);
                Assert.Equal("https://unsaved.example", model.ServerInput);
                Assert.Equal("unsaved-password", model.LinkPassword);
                Assert.Equal(90, model.Days);
                Assert.Equal("日本語", model.HistorySearch);
                Assert.Equal("ru", queue.State.Settings.Language);
                await model.ChangeLanguageAsync("en");
                window.UpdateLayout();
                Assert.Equal("Link history", Assert.IsType<System.Windows.Controls.TabItem>(tabs.SelectedItem).Header);
                Assert.Empty(model.HistoryRows);
                Assert.False(model.CopyHistoryCommand.CanExecute(null));
                Assert.False(model.RevokeHistoryCommand.CanExecute(null));
                window.Hide(); Assert.False(window.IsVisible);
            }
            finally
            {
                try { window?.Close(); }
                finally { await model.ShutdownAsync(); }
            }
        }
        finally
        {
            try { if (Directory.Exists(directory)) Directory.Delete(directory, true); }
            finally { app.Shutdown(); }
        }
    }
}
