using System.Globalization;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace ShareFile.Core;

public static class Localization
{
    private static readonly IReadOnlyDictionary<string, string> English = LoadCatalog();
    private static readonly IReadOnlyDictionary<string, string> Sources = English
        .GroupBy(pair => pair.Value).ToDictionary(group => group.Key, group => group.First().Key);
    private static readonly Regex Slot = new(@"\{(\d+)(?::[^}]+)?\}");
    private static readonly (string Source, string English, Regex Pattern, int[] Indices)[] Templates = English
        .Where(pair => Slot.IsMatch(pair.Key)).SelectMany(pair => new[] { pair.Key, pair.Value }.Select(text =>
        {
            var indices = new List<int>();
            var parts = new List<string>(); var start = 0;
            foreach (Match slot in Slot.Matches(text))
            {
                parts.Add(Regex.Escape(text[start..slot.Index])); parts.Add("(.*?)");
                indices.Add(int.Parse(slot.Groups[1].Value, CultureInfo.InvariantCulture)); start = slot.Index + slot.Length;
            }
            parts.Add(Regex.Escape(text[start..]));
            return (pair.Key, pair.Value, new Regex("\\A" + string.Concat(parts) + "\\z", RegexOptions.Singleline), indices.ToArray());
        })).ToArray();
    private static string _language = "en";
    public static string Normalize(string? value) => value == "ru" ? "ru" : "en";
    public static string Language
    {
        get => Volatile.Read(ref _language);
        set => Volatile.Write(ref _language, Normalize(value));
    }
    public static CultureInfo Culture => CultureInfo.GetCultureInfo(Language == "ru" ? "ru-RU" : "en-US");
    public static string Text(string key, params object[] arguments)
    {
        if (arguments.Length == 0 && !English.ContainsKey(key) && !Sources.ContainsKey(key))
        {
            foreach (var template in Templates)
            {
                var match = template.Pattern.Match(key);
                if (!match.Success) continue;
                var values = template.Indices.Select((index, group) => (index, value: match.Groups[group + 1].Value)).ToDictionary(pair => pair.index, pair => pair.value);
                return Slot.Replace(Language == "ru" ? template.Source : template.English,
                    slot => values[int.Parse(slot.Groups[1].Value, CultureInfo.InvariantCulture)]);
            }
        }
        var source = English.ContainsKey(key) ? key : Sources.GetValueOrDefault(key, key);
        var text = Language == "ru" ? source : English.GetValueOrDefault(source, source);
        return arguments.Length == 0 ? text : string.Format(Culture, text, arguments);
    }
    private static IReadOnlyDictionary<string, string> LoadCatalog()
    {
        using var stream = typeof(Localization).Assembly.GetManifestResourceStream("ShareFile.Localization.en.json")
            ?? throw new InvalidOperationException("ShareFile localization resource is missing");
        return JsonSerializer.Deserialize<Dictionary<string, string>>(stream)
            ?? throw new InvalidOperationException("ShareFile localization resource is invalid");
    }
}
