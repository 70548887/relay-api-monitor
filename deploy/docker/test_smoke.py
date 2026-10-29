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


class ComposeRestoreTests(unittest.TestCase):
    def configuration(self, volume):
        return {'volumes': {'relaylab-data': {'name': volume}}, 'services': {'relaylab': {
            'volumes': [{'type': 'volume', 'source': 'relaylab-data', 'target': '/var/lib/relaylab'}]}}}

    def test_old_and_new_volume_environments_do_not_mutate_the_process_environment(self):
        with patch.dict(smoke.os.environ, {'RELAYLAB_DATA_VOLUME': 'real-existing-volume',
                                          'RELAYLAB_PUBLIC_URL': 'https://external.example'}):
            old = smoke.compose_environment('random-old-test-volume')
            new = smoke.compose_environment('random-new-test-volume')
            self.assertEqual(old['RELAYLAB_DATA_VOLUME'], 'random-old-test-volume')
            self.assertEqual(new['RELAYLAB_DATA_VOLUME'], 'random-new-test-volume')
            self.assertEqual(new['RELAYLAB_PUBLIC_URL'], '')
            self.assertEqual(new['RELAYLAB_PORT'], '0')
            self.assertEqual(smoke.os.environ['RELAYLAB_DATA_VOLUME'], 'real-existing-volume')

    def test_missing_interpolation_or_wrong_mount_cannot_pass_the_restore_gate(self):
        smoke.assert_compose_volume(self.configuration('new-test-volume'), 'new-test-volume')
        with self.assertRaises(AssertionError):
            smoke.assert_compose_volume(self.configuration('old-test-volume'), 'new-test-volume')
        disconnected = self.configuration('new-test-volume')
        disconnected['services']['relaylab']['volumes'][0]['source'] = 'unrelated-data'
        with self.assertRaises(AssertionError):
            smoke.assert_compose_volume(disconnected, 'new-test-volume')

    def test_container_must_mount_the_requested_volume(self):
        with patch.object(smoke, 'command', return_value='[{"Destination":"/var/lib/relaylab","Name":"new-test-volume"}]'):
            smoke.assert_container_volume('fixture-container', 'new-test-volume')
        with patch.object(smoke, 'command', return_value='[{"Destination":"/var/lib/relaylab","Name":"old-test-volume"}]'):
            with self.assertRaises(AssertionError):
                smoke.assert_container_volume('fixture-container', 'new-test-volume')


if __name__ == '__main__':
    unittest.main()
