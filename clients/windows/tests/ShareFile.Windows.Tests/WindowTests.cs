using System.Windows;
using ShareFile.Core;
using ShareFile.Windows.Platform;
using ShareFile.Windows.ViewModels;

namespace ShareFile.Windows.Tests;

[Collection("Windows integration")]
public sealed class WindowTests
{
    [Fact]
    public async Task WindowLoadsXamlResourcesAndStaysHiddenUntilRequested()
    {
        var completed = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var thread = new Thread(() =>
        {
            var directory = Path.Combine(Path.GetTempPath(), "sharefile-window-test-" + Guid.NewGuid().ToString("N"));
            try
            {
                var app = new App(); app.InitializeComponent();
                var queue = new UploadQueue(new JsonStateStore(Path.Combine(directory, "state.json")), new UploadEngine());
                using var desktop = new DesktopIntegration();
                var model = new MainViewModel(queue, new CredentialStore(), desktop);
                var window = new MainWindow(model);
                Assert.False(window.IsVisible);
                window.Show(); window.UpdateLayout();
                Assert.True(window.IsVisible); Assert.Same(model, window.DataContext); Assert.True(window.ActualWidth >= 760);
                var tabs = Assert.IsType<System.Windows.Controls.TabControl>(window.FindName("ClientTabs"));
                Assert.Equal(3, tabs.Items.Count);
                tabs.SelectedIndex = 2; window.UpdateLayout();
                Assert.Equal("История ссылок", Assert.IsType<System.Windows.Controls.TabItem>(tabs.SelectedItem).Header);
                model.HistorySearch = "日本語";
                Assert.Empty(model.HistoryRows);
                Assert.False(model.CopyHistoryCommand.CanExecute(null));
                Assert.False(model.RevokeHistoryCommand.CanExecute(null));
                window.Hide(); Assert.False(window.IsVisible);
                window.Close(); app.Shutdown();
                Assert.True(queue.DisposeAsync().IsCompletedSuccessfully);
                completed.SetResult();
            }
            catch (Exception error) { completed.TrySetException(error); }
            finally { if (Directory.Exists(directory)) Directory.Delete(directory, true); }
        });
        thread.SetApartmentState(ApartmentState.STA); thread.Start();
        await completed.Task.WaitAsync(TimeSpan.FromSeconds(30), TestContext.Current.CancellationToken);
    }
}
