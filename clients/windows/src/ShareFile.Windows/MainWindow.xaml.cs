using System.Windows;
using ShareFile.Windows.ViewModels;

namespace ShareFile.Windows;

public partial class MainWindow : Window
{
    private readonly MainViewModel _model;
    public MainWindow(MainViewModel model)
    {
        LocalizationResources.Apply(Application.Current.Resources);
        InitializeComponent(); _model = model; DataContext = model;
        model.LanguageChanged += ApplyLanguage;
        Closed += (_, _) => model.LanguageChanged -= ApplyLanguage;
        Loaded += (_, _) => LinkPasswordBox.Password = model.LinkPassword;
        model.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName == nameof(MainViewModel.LinkPassword) && LinkPasswordBox.Password != model.LinkPassword)
                LinkPasswordBox.Password = model.LinkPassword;
        };
    }
    private void ApplyLanguage()
    {
        LocalizationResources.Apply(Application.Current.Resources);
        Language = System.Windows.Markup.XmlLanguage.GetLanguage(ShareFile.Core.Localization.Language);
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
