import copy
import datetime
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('docker_smoke', Path(__file__).with_name('smoke.py'))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


def reports():
    timestamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
    return {'launcher': {'instance_id': 'current-launcher'}, **{
        'runtime-' + role: {'role': role, 'pid': 101 + index, 'instance_id': 'current-launcher',
                           'ready': True, 'maintenance': False, 'heartbeat_at': timestamp}
        for index, role in enumerate(('serve', 'worker'))}}


class ReadinessTests(unittest.TestCase):
    def test_http_liveness_does_not_bypass_the_worker_lease_wait(self):
        ready = reports()
        waiting = copy.deepcopy(ready)
        waiting['runtime-worker']['ready'] = False
        clock = [0]
        with patch.object(smoke, 'command', return_value='running healthy'), \
             patch.object(smoke, 'read_runtime_reports', side_effect=[waiting, waiting, ready]), \
             patch.object(smoke.time, 'monotonic', side_effect=lambda: clock[0]), \
             patch.object(smoke.time, 'sleep', side_effect=lambda seconds: clock.__setitem__(0, clock[0] + seconds)):
            actual = smoke.wait_ready('fixture', timeout=10, poll_interval=2)
        self.assertEqual(clock[0], 4)
        self.assertTrue(actual['runtime-worker']['ready'])

    def test_persistent_maintenance_times_out_with_both_actual_reports(self):
        paused = reports()
        paused['runtime-worker']['maintenance'] = True
        clock = [0]
        with patch.object(smoke, 'command', return_value='running healthy'), \
             patch.object(smoke, 'read_runtime_reports', return_value=paused), \
             patch.object(smoke.time, 'monotonic', side_effect=lambda: clock[0]), \
             patch.object(smoke.time, 'sleep', side_effect=lambda seconds: clock.__setitem__(0, clock[0] + seconds)):
            with self.assertRaises(RuntimeError) as raised:
                smoke.wait_ready('fixture', timeout=5, poll_interval=2)
        message = str(raised.exception)
        self.assertIn('runtime-serve', message)
        self.assertIn('runtime-worker', message)
        self.assertIn('"maintenance": true', message)

    def test_stale_or_foreign_reports_cannot_satisfy_readiness(self):
        ready = reports()
        self.assertTrue(smoke.runtime_reports_ready(ready))
        foreign = copy.deepcopy(ready)
        foreign['runtime-worker']['instance_id'] = 'previous-launcher'
        self.assertFalse(smoke.runtime_reports_ready(foreign))
        stale = copy.deepcopy(ready)
        stale['runtime-serve']['heartbeat_at'] = (
            datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(seconds=30)).isoformat()
        self.assertFalse(smoke.runtime_reports_ready(stale))
        missing = copy.deepcopy(ready)
        missing['runtime-worker'] = {'read_error': 'report does not exist yet'}
        self.assertFalse(smoke.runtime_reports_ready(missing))


if __name__ == '__main__':
    unittest.main()
