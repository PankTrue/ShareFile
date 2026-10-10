using ShareFile.Core;

namespace ShareFile.Core.Tests;

public sealed class ProgressTests
{
    [Fact]
    public void RateAgesDuringStallsAndResetsOnResume()
    {
        var now = 0.0; var meter = new TransferMeter(() => now);
        Assert.Equal(0, meter.Rate);
        now = 2; meter.Record(200); Assert.Equal(100, meter.Rate);
        now = 12; Assert.Equal(0, meter.Rate);
        meter = new TransferMeter(() => now);
        now = 14; meter.Record(100); Assert.Equal(50, meter.Rate);
    }
    [Fact]
    public void RollingWindowKeepsItsBoundarySample()
    {
        var now = 0.0; var meter = new TransferMeter(() => now);
        now = 1; meter.Record(1000);
        now = 11; meter.Record(1100);
        Assert.Equal(10, meter.Rate);
    }
    private static UploadJob Job(string id, string batch, long size, long offset, UploadStatus status) => new()
    { Id = id, Batch = batch, Size = size, Offset = offset, Status = status, Source = "", Name = id, Fingerprint = "", Folder = "" };
    [Fact]
    public void ByteWeightedSummaryKeepsCompletedSiblingsAndExcludesOldBatches()
    {
        UploadJob[] jobs = [Job("old", "old", 9000, 9000, UploadStatus.Done), Job("small", "new", 100, 100, UploadStatus.Done), Job("large", "new", 900, 400, UploadStatus.Uploading)];
        var summary = TransferSummary.Create(TransferSummary.Selection(jobs), 100);
        Assert.Equal(50, summary.Percent); Assert.Equal(1000, summary.Size); Assert.Equal(1, summary.Completed);
        Assert.Contains("Осталось ≈ 5 с", summary.Detail);
        jobs[2] = jobs[2] with { Status = UploadStatus.Paused };
        Assert.DoesNotContain("Осталось ≈", TransferSummary.Create(TransferSummary.Selection(jobs), 100).Detail);
    }
    [Fact]
    public void EmptyFileWaitsForFinalization()
    {
        var job = Job("empty", "", 0, 0, UploadStatus.Sharing);
        Assert.Equal(0, TransferSummary.Create([job], 0).Percent);
        Assert.Equal(100, TransferSummary.Create([job with { Status = UploadStatus.Done }], 0).Percent);
        Assert.Equal("2 ч", TransferSummary.RemainingTime(7199));
    }
}
