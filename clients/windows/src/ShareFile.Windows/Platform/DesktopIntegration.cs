using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Windows;
using ShareFile.Core;
using Forms = System.Windows.Forms;

namespace ShareFile.Windows.Platform;

public sealed class DesktopIntegration : IDisposable
{
    private readonly Forms.NotifyIcon _tray = new()
    {
        Text = "ShareFile",
        Visible = true,
        Icon = System.Drawing.Icon.ExtractAssociatedIcon(Environment.ProcessPath!) ?? (System.Drawing.Icon)System.Drawing.SystemIcons.Application.Clone()
    };
    public Action? OpenRequested { get; set; }
    public Action? HistoryRequested { get; set; }
    public Func<IReadOnlyList<SentLink>>? RecentLinks { get; set; }
    public Action<SentLink>? CopyHistoryRequested { get; set; }
    public Action<SentLink>? OpenHistoryRequested { get; set; }
    public Action<SentLink>? RevokeHistoryRequested { get; set; }
    public Func<SentLink, bool>? CanRevokeHistory { get; set; }
    public Action? ExitRequested { get; set; }
    public Action? ClipboardRequested { get; set; }

    public DesktopIntegration()
    {
        var menu = new Forms.ContextMenuStrip();
        menu.Items.Add("Открыть ShareFile", null, (_, _) => OpenRequested?.Invoke());
        menu.Items.Add("Из буфера обмена (Ctrl+Alt+U)", null, (_, _) => ClipboardRequested?.Invoke());
        menu.Items.Add("История ссылок", null, (_, _) => HistoryRequested?.Invoke());
        var recent = new Forms.ToolStripMenuItem("Недавние ссылки");
        menu.Items.Add(recent);
        menu.Opening += (_, _) =>
        {
            foreach (var child in recent.DropDownItems.Cast<Forms.ToolStripItem>().ToArray()) child.Dispose();
            recent.DropDownItems.Clear();
            foreach (var entry in RecentLinks?.Invoke() ?? [])
            {
                var item = new Forms.ToolStripMenuItem(entry.Name);
                item.DropDownItems.Add(new Forms.ToolStripMenuItem(entry.Status) { Enabled = false });
                item.DropDownItems.Add(new Forms.ToolStripMenuItem("Копировать", null, (_, _) => CopyHistoryRequested?.Invoke(entry)) { Enabled = entry.Active });
                item.DropDownItems.Add(new Forms.ToolStripMenuItem("Открыть", null, (_, _) => OpenHistoryRequested?.Invoke(entry)) { Enabled = entry.Active });
                item.DropDownItems.Add(new Forms.ToolStripMenuItem("Отозвать", null, (_, _) => RevokeHistoryRequested?.Invoke(entry)) { Enabled = CanRevokeHistory?.Invoke(entry) == true });
                recent.DropDownItems.Add(item);
            }
            if (recent.DropDownItems.Count == 0) recent.DropDownItems.Add(new Forms.ToolStripMenuItem("История пока пуста") { Enabled = false });
        };
        menu.Items.Add("Выход", null, (_, _) => ExitRequested?.Invoke());
        _tray.ContextMenuStrip = menu;
        _tray.DoubleClick += (_, _) => OpenRequested?.Invoke();
        _tray.BalloonTipClicked += (_, _) => OpenRequested?.Invoke();
    }

    public static void OpenBrowser(string url)
    {
        if (!Uri.TryCreate(url, UriKind.Absolute, out var uri) || uri.Scheme is not ("https" or "http"))
            throw new ClientException("Некорректный адрес браузера");
        Process.Start(new ProcessStartInfo(uri.AbsoluteUri) { UseShellExecute = true });
    }

    public async Task<bool> CopyAsync(string text)
    {
        for (var attempt = 0; attempt < 5; attempt++)
        {
            try { Clipboard.SetText(text); return true; }
            catch (ExternalException) { await Task.Delay(100 * (attempt + 1)); }
        }
        return false;
    }

    public void Notify(BatchCompletion batch, bool copied)
    {
        if (!batch.Notify) return;
        var files = batch.Names.Count == 1 ? batch.Names[0] : $"Загружено объектов: {batch.Names.Count}";
        var status = copied ? "Ссылки скопированы" : "Не удалось скопировать ссылки. Откройте ShareFile";
        // No capabilities in banners or logs. Windows can suppress banners without affecting uploads.
        _tray.ShowBalloonTip(5000, "Загрузка завершена", files + Environment.NewLine + status, Forms.ToolTipIcon.Info);
    }
    public void NotifyFailure(string message, bool notify)
    {
        if (notify) _tray.ShowBalloonTip(5000, "Нужна ваша помощь", message[..Math.Min(message.Length, 240)], Forms.ToolTipIcon.Warning);
    }
    public void Dispose() { _tray.Visible = false; _tray.ContextMenuStrip?.Dispose(); _tray.Icon?.Dispose(); _tray.Dispose(); }
}
