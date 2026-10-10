using System.Text.Json;
using System.Windows;
using ShareFile.Core;

namespace ShareFile.Windows;

internal static class LocalizationResources
{
    private static readonly Dictionary<string, string> Keys = LoadKeys();
    public static void Apply(ResourceDictionary resources)
    {
        foreach (var (key, source) in Keys) resources[key] = Localization.Text(source);
    }
    private static Dictionary<string, string> LoadKeys()
    {
        using var stream = typeof(LocalizationResources).Assembly.GetManifestResourceStream("ShareFile.Windows.LocalizationKeys.json")!;
        return JsonSerializer.Deserialize<Dictionary<string, string>>(stream)!;
    }
}
