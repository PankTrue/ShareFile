import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sharefile_linux.progress import TransferMeter, remaining_time, selection_jobs, summary


class ProgressTests(unittest.TestCase):
    def test_rate_stalls_and_resume_starts_a_new_measurement(self):
        now = [0]
        meter = TransferMeter(lambda: now[0])
        self.assertEqual(meter.rate(), 0)
        now[0] = 2
        meter.record(200)
        self.assertEqual(meter.rate(), 100)
        now[0] = 12
        self.assertEqual(meter.rate(), 0)
        meter = TransferMeter(lambda: now[0])
        now[0] = 14
        meter.record(100)
        self.assertEqual(meter.rate(), 50)

    def test_recent_window_uses_boundary_sample(self):
        now = [0]
        meter = TransferMeter(lambda: now[0])
        now[0] = 1
        meter.record(1000)
        now[0] = 11
        meter.record(1100)
        self.assertEqual(meter.rate(), 10)

    def test_byte_weighting_completed_siblings_and_blocked_eta(self):
        jobs = [dict(id="old", batch="old", size=9000, offset=9000, status="done"),
                dict(id="small", batch="new", size=100, offset=100, status="done"),
                dict(id="large", batch="new", size=900, offset=400, status="uploading", rate=100)]
        result = summary(selection_jobs(jobs))
        self.assertEqual((result["percent"], result["size"], result["completed"]), (50, 1000, 1))
        self.assertIn("Осталось ≈ 5 с", result["detail"])
        jobs[2]["status"] = "paused"
        self.assertNotIn("Осталось ≈", summary(selection_jobs(jobs))["detail"])

    def test_empty_file_waits_for_finalization(self):
        self.assertEqual(remaining_time(7199), "2 ч")
        job = dict(id="empty", size=0, offset=0, status="sharing")
        self.assertEqual(summary([job])["percent"], 0)
        job["status"] = "done"
        self.assertEqual(summary([job])["percent"], 100)
