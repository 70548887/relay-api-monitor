#!/usr/bin/env python3
"""Run a built Linux image against disposable volumes, without upstream calls."""
import argparse
import datetime
import http.cookiejar
import json
import secrets
import subprocess
import time
import urllib.request


def command(*args, stdin=None):
    result = subprocess.run(args, input=stdin, text=True, capture_output=True, timeout=780)
    if result.returncode:
        raise RuntimeError(f"command failed: {' '.join(args)}\n{result.stdout}\n{result.stderr}")
    return result.stdout.strip()


def read_runtime_reports(container):
    snapshot = {}
    for name in ('launcher', 'runtime-serve', 'runtime-worker'):
        try:
            snapshot[name] = json.loads(command('docker', 'exec', container, 'cat',
                                               f'/var/lib/relaylab/data/{name}.json'))
        except (RuntimeError, json.JSONDecodeError) as error:
            snapshot[name] = {'read_error': str(error)}
    return snapshot


def runtime_reports_ready(snapshot, now=None):
    instance = snapshot.get('launcher', {}).get('instance_id')
    if not instance:
        return False
    current = now or datetime.datetime.now(datetime.timezone.utc)
    for role in ('serve', 'worker'):
        report = snapshot.get('runtime-' + role, {})
        if (report.get('role') != role or report.get('ready') is not True
                or report.get('maintenance') is not False
                or report.get('instance_id') != instance or not report.get('pid')):
            return False
        try:
            heartbeat = datetime.datetime.fromisoformat(report['heartbeat_at'].replace('Z', '+00:00'))
            age = (current - heartbeat).total_seconds()
        except (KeyError, TypeError, ValueError):
            return False
        if not -5 <= age <= 10:
            return False
    return True


def wait_ready(container, timeout=150, poll_interval=2):
    deadline = time.monotonic() + timeout
    actual = {}
    while time.monotonic() < deadline:
        state = command('docker', 'inspect', '--format', '{{.State.Status}} {{.State.Health.Status}}', container)
        actual = {'container': state, 'runtime': read_runtime_reports(container)}
        # A restored DB can retain a live worker lease for 45 seconds. HTTP
        # liveness is available while the new worker waits for that lease; only
        # current, fresh reports from both children establish full readiness.
        if state == 'running healthy' and runtime_reports_ready(actual['runtime']):
            return actual['runtime']
        if not state.startswith('running '):
            raise RuntimeError('container stopped before readiness: ' + json.dumps(actual, sort_keys=True))
        time.sleep(poll_interval)
    raise RuntimeError('container and both child runtime reports did not become ready: '
                       + json.dumps(actual, sort_keys=True))


def verify_http(container, username, password):
    port = command('docker', 'port', container, '42783/tcp').splitlines()[0]
    base = 'http://' + port
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(route, body=None):
        req = urllib.request.Request(base + route,
                                     data=None if body is None else json.dumps(body).encode(),
                                     headers={'Content-Type': 'application/json', 'Origin': base})
        with opener.open(req, timeout=60) as response:
            return json.load(response)

    assert request('/healthz')['status'] == 'ok'
    assert request('/api/v1/setup/status')['initialized'] is True
    request('/api/v1/public/monitor')
    request('/api/v1/auth/login', {'username': username, 'password': password})
    assert request('/api/v1/auth/me')['user']['username'] == username
    assert request('/api/v1/admin/overview')['worker']['online'] is True
    reports = read_runtime_reports(container)
    for role in ('serve', 'worker'):
        report = reports.get('runtime-' + role, {})
        assert report.get('role') == role and report.get('ready') is True and report.get('maintenance') is False, \
            f'unexpected {role} runtime report: {json.dumps(report, sort_keys=True)}'
    assert runtime_reports_ready(reports), 'stale or foreign runtime reports: ' + json.dumps(reports, sort_keys=True)
    assert command('docker', 'exec', container, 'id', '-u') == '10001'
    command('docker', 'exec', container, 'sh', '-c', 'test ! -w /usr/local/bin/relaylab')


def run(image):
    suffix = secrets.token_hex(5)
    volume, restored_volume = f'relaylab-smoke-{suffix}', f'relaylab-restored-{suffix}'
    container, restored_container = f'relaylab-smoke-app-{suffix}', f'relaylab-restored-app-{suffix}'
    username, password = 'smoke-admin', secrets.token_urlsafe(24)
    containers = []
    volumes = []

    def start(name, data_volume):
        command('docker', 'run', '--detach', '--name', name, '--read-only',
                '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m,mode=1777',
                '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                '--stop-timeout', '720', '--volume', data_volume + ':/var/lib/relaylab',
                '--publish', '127.0.0.1::42783', image)
        containers.append(name)
        wait_ready(name)
        verify_http(name, username, password)

    try:
        for name in (volume, restored_volume):
            command('docker', 'volume', 'create', name)
            volumes.append(name)
        command('docker', 'run', '--rm', '--interactive', '--volume', volume + ':/var/lib/relaylab',
                '--entrypoint', '/usr/local/bin/relaylab', image, 'init-admin',
                '--config', '/etc/relaylab/config.yaml', '--username', username,
                stdin=password + '\n')
        start(container, volume)
        command('docker', 'exec', container, 'mkdir', '-p', '/var/lib/relaylab/backups')
        command('docker', 'exec', container, '/usr/local/bin/relaylab', 'backup',
                '--config', '/etc/relaylab/config.yaml', '--output', '/var/lib/relaylab/backups/smoke.zip')
        command('docker', 'stop', '--time', '720', container)
        assert command('docker', 'inspect', '--format', '{{.State.ExitCode}}', container) == '0'
        command('docker', 'start', container)
        wait_ready(container)
        verify_http(container, username, password)
        command('docker', 'stop', '--time', '720', container)
        command('docker', 'run', '--rm', '--volume', restored_volume + ':/var/lib/relaylab',
                '--volume', volume + ':/restore-source:ro', '--entrypoint', '/usr/local/bin/relaylab',
                image, 'restore', '--config', '/etc/relaylab/config.yaml',
                '--input', '/restore-source/backups/smoke.zip')
        start(restored_container, restored_volume)
        command('docker', 'stop', '--time', '720', restored_container)
        assert command('docker', 'inspect', '--format', '{{.State.ExitCode}}', restored_container) == '0'
        print(json.dumps({'image': image, 'passed': ['non-root startup', 'serve and worker readiness',
              'administrator initialization and login', 'public monitor', 'read-only root filesystem',
              'graceful stop and restart', 'persistent login after restart', 'backup and restore'],
              'upstream_calls': 0}))
    except Exception:
        for name in containers:
            print(f'Actual runtime diagnostic: {name}\n{json.dumps(read_runtime_reports(name), sort_keys=True)}')
            logs = subprocess.run(['docker', 'logs', '--tail', '80', name], capture_output=True, text=True)
            print(f'Container diagnostic: {name}\n{logs.stdout}\n{logs.stderr}')
        raise
    finally:
        for name in containers:
            subprocess.run(['docker', 'rm', '--force', name], capture_output=True, check=False)
        for name in volumes:
            subprocess.run(['docker', 'volume', 'rm', name], capture_output=True, check=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--image', required=True)
    run(parser.parse_args().image)
