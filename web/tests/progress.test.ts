import { describe, expect, it } from 'vitest';
import { progressSummary, remainingTime, selectionJobs, TransferMeter } from '../src/progress';

describe('Confirmed transfer progress', () => {
  it('measures recent bytes, ages a stalled rate and resets on resume', () => {
    let time = 0;
    const meter = new TransferMeter(() => time);
    meter.start();
    expect(meter.rate()).toBe(0);
    time = 2;
    meter.record(200);
    expect(meter.rate()).toBe(100);
    time = 4;
    expect(meter.rate()).toBe(50);
    time = 12;
    expect(meter.rate()).toBe(0);
    meter.start();
    time = 14;
    meter.record(100);
    expect(meter.rate()).toBe(50);
  });
  it('uses the recent window, including the boundary sample', () => {
    let time = 0;
    const meter = new TransferMeter(() => time);
    meter.start();
    time = 1;
    meter.record(1000);
    time = 11;
    meter.record(1100);
    expect(meter.rate()).toBe(10);
  });
  it('weights by bytes and retains completed siblings without old batches', () => {
    const jobs = [
      { id: 'old', batch: 'old', size: 9000, offset: 9000, status: 'complete' },
      { id: 'small', batch: 'new', size: 100, offset: 100, status: 'complete' },
      { id: 'large', batch: 'new', size: 900, offset: 400, status: 'uploading' },
    ];
    expect(selectionJobs(jobs).map((job) => job.id)).toEqual(['small', 'large']);
    expect(progressSummary(selectionJobs(jobs), 100)).toMatchObject({
      percent: 50,
      seconds: 5,
      completed: 1,
      size: 1000,
    });
    jobs[2].status = 'paused';
    expect(progressSummary(selectionJobs(jobs), 100).seconds).toBeNull();
  });
  it('handles empty selections and zero byte files awaiting finalization', () => {
    const job = { id: 'empty', size: 0, offset: 0, status: 'sharing' };
    expect(progressSummary([job], 0).percent).toBe(0);
    job.status = 'complete';
    expect(progressSummary([job], 0).percent).toBe(100);
    expect(progressSummary([], 0).seconds).toBeNull();
    expect(remainingTime(61)).toBe('2 min');
    expect(remainingTime(7199)).toBe('2 h');
  });
});
