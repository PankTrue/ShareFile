using System.Windows;
using ShareFile.Windows.ViewModels;

namespace ShareFile.Windows;

public partial class MainWindow : Window
{
    private readonly MainViewModel _model;
    public MainWindow(MainViewModel model)
    {
        InitializeComponent(); _model = model; DataContext = model;
        Loaded += (_, _) => LinkPasswordBox.Password = model.LinkPassword;
        model.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName == nameof(MainViewModel.LinkPassword) && LinkPasswordBox.Password != model.LinkPassword)
                LinkPasswordBox.Password = model.LinkPassword;
        };
    }
    private void OnDragOver(object sender, DragEventArgs e)
    {
        e.Effects = e.Data.GetDataPresent(DataFormats.FileDrop) ? DragDropEffects.Copy : DragDropEffects.None;
        e.Handled = true;
    }
    private async void OnDrop(object sender, DragEventArgs e)
    {
        if (e.Data.GetData(DataFormats.FileDrop) is string[] files) await _model.AcceptFilesAsync(files, false);
    }
    private void OnPasswordChanged(object sender, RoutedEventArgs e) { if (_model is not null) _model.LinkPassword = LinkPasswordBox.Password; }
}
