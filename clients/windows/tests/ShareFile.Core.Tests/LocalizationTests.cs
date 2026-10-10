using System.Text.Json;
using ShareFile.Core;

namespace ShareFile.Core.Tests;

[CollectionDefinition("Localization", DisableParallelization = true)]
public sealed class LocalizationCollection;

[Collection("Localization")]
public sealed class LocalizationTests
{
    [Fact]
    public void TranslatesStatusAndParameterizedMessagesInBothLanguages()
    {
        try
        {
            Localization.Language = "en";
            Assert.Equal("Settings", Localization.Text("Настройки"));
            Assert.Equal("Items uploaded: 2", Localization.Text("Загружено объектов: {0}", 2));
            Assert.Equal("{1} of файл · {0}", Localization.Text("{0} из {1} · {2}", "{1}", "файл", "{0}"));
            Localization.Language = "ru";
            Assert.Equal("Настройки", Localization.Text("Settings"));
            Assert.Equal("Загружено объектов: 2", Localization.Text("Items uploaded: 2"));
            Assert.Equal("2 ч", TransferSummary.RemainingTime(7199));
            Localization.Language = "de";
            Assert.Equal("en", Localization.Language);
            Assert.Equal("Upload complete", Localization.Text("Загрузка завершена"));
        }
        finally { Localization.Language = "en"; }
    }

    [Theory]
    [InlineData("en")]
    [InlineData("ru")]
    public async Task TransientFailuresResumeAcrossLanguageChanges(string language)
    {
        var directory = Path.Combine(Path.GetTempPath(), "sharefile-language-upload-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        var ct = TestContext.Current.CancellationToken;
        try
        {
            Localization.Language = language;
            var source = Path.Combine(directory, "payload.bin");
            await File.WriteAllBytesAsync(source, new byte[2500], ct);
            var job = await UploadEngine.PrepareAsync(source, FakeApi.Folder, ct);
            var api = new FakeApi { LoseCreate = true, LosePatch = true };
            var engine = new UploadEngine((_, _) => Task.CompletedTask);
            var result = await engine.UploadAsync(api, job, (_, _) =>
            {
                Localization.Language = language == "en" ? "ru" : "en";
                return Task.CompletedTask;
            }, ct);
            Assert.Equal(job.Size, result.Offset);
            Assert.Equal(1, api.CreatedObjects);
        }
        finally { Localization.Language = "en"; Directory.Delete(directory, true); }
    }

    [Fact]
    public async Task LegacyDefaultAndRestartPreserveQueueAndUploadSettings()
    {
        var directory = Path.Combine(Path.GetTempPath(), "sharefile-language-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(directory);
        var ct = TestContext.Current.CancellationToken;
        try
        {
            var path = Path.Combine(directory, "state.json");
            await File.WriteAllTextAsync(path, """{"version":1,"settings":{"days":30,"notifyUploads":false},"jobs":[]}""", ct);
            var store = new JsonStateStore(path);
            Assert.Equal("en", (await store.LoadAsync(ct)).Settings.Language);
            await using var queue = new UploadQueue(store, new UploadEngine());
            await queue.LoadAsync(ct);
            await queue.SetLanguageAsync("ru", ct);
            var restored = await store.LoadAsync(ct);
            Assert.Equal("ru", restored.Settings.Language);
            Assert.Equal(30, restored.Settings.Days);
            Assert.False(restored.Settings.NotifyUploads);
            Assert.Empty(restored.Jobs);
            await queue.SetLanguageAsync("unsupported", ct);
            Assert.Equal("en", (await store.LoadAsync(ct)).Settings.Language);
        }
        finally { Localization.Language = "en"; Directory.Delete(directory, true); }
    }
}
