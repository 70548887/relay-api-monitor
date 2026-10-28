#!/usr/bin/env python3
"""Run a built Linux image against disposable volumes, without upstream calls."""
import argparse
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


def wait_ready(container):
    deadline = time.monotonic() + 150
    while time.monotonic() < deadline:
        state = command('docker', 'inspect', '--format', '{{.State.Status}} {{.State.Health.Status}}', container)
        if state == 'running healthy':
            return
        if not state.startswith('running '):
            raise RuntimeError(f'container stopped before readiness: {state}')
        time.sleep(2)
    raise RuntimeError('container health check timed out')


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
    for role in ('serve', 'worker'):
        report = json.loads(command('docker', 'exec', container, 'cat',
                                    f'/var/lib/relaylab/data/runtime-{role}.json'))
        assert report['role'] == role and report['ready'] and not report['maintenance']
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
