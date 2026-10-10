using System.Runtime.InteropServices;
using System.Windows;
using System.Windows.Interop;
using System.Windows.Media.Imaging;
using ShareFile.Core;

namespace ShareFile.Windows.Platform;

public static class ClipboardUpload
{
    public static async Task<byte[]> ReadPngAsync()
    {
        // Screenshots are commonly exposed as DIB/Bitmap. PNG streams preserve alpha.
        for (var attempt = 0; attempt < 5; attempt++)
        {
            try
            {
                BitmapSource? image = null;
                if (Clipboard.GetData("PNG") is Stream stream)
                {
                    using var bounded = new MemoryStream();
                    var buffer = new byte[65536];
                    int count;
                    while ((count = stream.Read(buffer, 0, buffer.Length)) > 0)
                    {
                        if (bounded.Length + count > ClipboardImages.MaxBytes) throw new ClientException(Localization.Text("Изображение слишком большое (максимум 32 МиБ)"));
                        bounded.Write(buffer, 0, count);
                    }
                    bounded.Position = 0;
                    image = BitmapFrame.Create(bounded, BitmapCreateOptions.PreservePixelFormat, BitmapCacheOption.OnLoad);
                }
                image ??= Clipboard.GetImage();
                if (image is null) throw new ClientException(Localization.Text("В буфере обмена нет изображения. Скопируйте скриншот и повторите"));
                if (image.PixelWidth <= 0 || image.PixelHeight <= 0 || (long)image.PixelWidth * image.PixelHeight > ClipboardImages.MaxPixels)
                    throw new ClientException(Localization.Text("Изображение слишком большое (максимум 25 Мп)"));
                var encoder = new PngBitmapEncoder(); encoder.Frames.Add(BitmapFrame.Create(image));
                using var png = new MemoryStream(); encoder.Save(png);
                if (png.Length > ClipboardImages.MaxBytes) throw new ClientException(Localization.Text("Изображение слишком большое (максимум 32 МиБ)"));
                return png.ToArray();
            }
            catch (ExternalException) when (attempt < 4) { await Task.Delay(100 * (attempt + 1)); }
        }
        throw new ClientException(Localization.Text("Буфер обмена занят. Повторите загрузку"));
    }
}

public sealed class ClipboardHotKey : IDisposable
{
    private const int Id = 0x5346;
    private readonly HwndSource _source;
    private readonly Action _action;
    private bool _disposed;
    public ClipboardHotKey(Action action)
    {
        _action = action;
        // Message-only window: the shortcut also works while the WPF window is hidden.
        _source = new HwndSource(new HwndSourceParameters("ShareFile clipboard shortcut") { ParentWindow = new IntPtr(-3) });
        _source.AddHook(Handle);
        if (!RegisterHotKey(_source.Handle, Id, 0x0001 | 0x0002 | 0x4000, 0x55))
        {
            _source.Dispose();
            throw new ClientException(Localization.Text("Ctrl+Alt+U занято другим приложением. Используйте «Из буфера обмена» в меню ShareFile"));
        }
    }
    private IntPtr Handle(IntPtr hwnd, int message, IntPtr wParam, IntPtr lParam, ref bool handled)
    {
        if (message == 0x0312 && wParam.ToInt32() == Id) { handled = true; _action(); }
        return IntPtr.Zero;
    }
    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        UnregisterHotKey(_source.Handle, Id); _source.RemoveHook(Handle); _source.Dispose();
    }
    [DllImport("user32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool RegisterHotKey(IntPtr hwnd, int id, uint modifiers, uint key);
    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool UnregisterHotKey(IntPtr hwnd, int id);
}
