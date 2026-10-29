#!/usr/bin/env python3
"""Run a built Linux image against disposable volumes, without upstream calls."""
import argparse
import datetime
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import urllib.request


def command(*args, stdin=None, env=None):
    result = subprocess.run(args, input=stdin, env=env, text=True, capture_output=True, timeout=780)
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


def compose_environment(volume):
    # Override only this disposable test's deployment values. In particular a
    # developer's public URL must not change these local HTTP-only fixtures.
    return dict(os.environ, RELAYLAB_DATA_VOLUME=volume, RELAYLAB_BIND_IP='127.0.0.1',
                RELAYLAB_PORT='0', RELAYLAB_PUBLIC_URL='', RELAYLAB_UPSTREAM_DNS_OVER_HTTPS='')


def assert_compose_volume(configuration, volume):
    actual = configuration.get('volumes', {}).get('relaylab-data', {}).get('name')
    assert actual == volume, f'Compose did not select the requested data volume: {actual}'
    mounts = configuration.get('services', {}).get('relaylab', {}).get('volumes', [])
    assert any(mount.get('type') == 'volume' and mount.get('source') == 'relaylab-data'
               and mount.get('target') == '/var/lib/relaylab' for mount in mounts), \
        'Compose data volume is not mounted by the application'


def volume_fingerprints(image, volume):
    # The source container must already be stopped. Read-only mounting prevents
    # the inspection itself from changing either SQLite or its encryption key.
    output = command('docker', 'run', '--rm', '--read-only', '--volume', volume + ':/source:ro',
                     '--entrypoint', 'sha256sum', image, '/source/data/relaylab.db',
                     '/source/data/master.key', '/source/backups/compose-smoke.zip')
    result = {}
    for line in output.splitlines():
        checksum, name = line.split(None, 1)
        assert len(checksum) == 64
        result[name.strip()] = checksum
    assert len(result) == 3
    return result


def assert_container_volume(container, volume):
    mounts = json.loads(command('docker', 'inspect', '--format', '{{json .Mounts}}', container))
    actual = [mount.get('Name') for mount in mounts if mount.get('Destination') == '/var/lib/relaylab']
    assert actual == [volume], f'application mounted the wrong data volume: {actual}'


def run_compose_restore(image, compose_file):
    """Exercise the documented environment-selected new-volume recovery path."""
    source = Path(compose_file).resolve()
    if not source.is_file():
        raise RuntimeError(f'Compose fixture does not exist: {source}')
    suffix = secrets.token_hex(5)
    project = 'relaylab-compose-' + suffix
    old_volume, new_volume = 'relaylab-compose-old-' + suffix, 'relaylab-compose-new-' + suffix
    old_environment, new_environment = compose_environment(old_volume), compose_environment(new_volume)
    username, password = 'compose-smoke-admin', secrets.token_urlsafe(24)
    containers = set()
    with tempfile.TemporaryDirectory(prefix='relaylab-compose-smoke-') as temporary:
        override = Path(temporary) / 'override.json'
        # JSON is a Compose-supported YAML subset. The real source/package
        # configuration remains intact; use its volumes, ports and protections.
        architecture = command('docker', 'image', 'inspect', '--format', '{{.Os}}/{{.Architecture}}', image)
        override.write_text(json.dumps({'services': {'relaylab': {
            'image': image, 'platform': architecture, 'restart': 'no', 'pull_policy': 'never'}}}), encoding='utf-8')
        prefix = ('docker', 'compose', '--project-directory', str(source.parent), '--project-name', project,
                  '--file', str(source), '--file', str(override))

        def compose(*args, environment, stdin=None):
            return command(*prefix, *args, env=environment, stdin=stdin)

        def current_container(environment):
            container = compose('ps', '--quiet', 'relaylab', environment=environment)
            assert container and len(container.splitlines()) == 1, 'one application container required'
            containers.add(container)
            return container

        try:
            for volume in (old_volume, new_volume):
                probe = subprocess.run(['docker', 'volume', 'inspect', volume], capture_output=True, text=True)
                assert probe.returncode != 0, 'restore fixture volume must be new'
            for environment, volume in ((old_environment, old_volume), (new_environment, new_volume)):
                configuration = json.loads(compose('config', '--format', 'json', environment=environment))
                assert_compose_volume(configuration, volume)
            compose('run', '--rm', '--no-deps', '-T', '--entrypoint', '/usr/local/bin/relaylab',
                    'relaylab', 'init-admin', '--config', '/etc/relaylab/config.yaml', '--username', username,
                    environment=old_environment, stdin=password + '\n')
            compose('up', '--detach', '--no-build', environment=old_environment)
            old_container = current_container(old_environment)
            wait_ready(old_container)
            assert_container_volume(old_container, old_volume)
            verify_http(old_container, username, password)
            compose('exec', '-T', 'relaylab', '/usr/local/bin/relaylab', 'backup', '--config',
                    '/etc/relaylab/config.yaml', '--output', '/var/lib/relaylab/backups/compose-smoke.zip',
                    environment=old_environment)
            compose('stop', environment=old_environment)
            assert command('docker', 'inspect', '--format', '{{.State.ExitCode}}', old_container) == '0'
            original = volume_fingerprints(image, old_volume)
            # Compose now selects a different, never-initialized volume. The
            # backup source is a separate read-only mount and is not replaced.
            compose('run', '--rm', '--no-deps', '-T', '--volume', old_volume + ':/restore-source:ro',
                    '--entrypoint', '/usr/local/bin/relaylab', 'relaylab', 'restore', '--config',
                    '/etc/relaylab/config.yaml', '--input', '/restore-source/backups/compose-smoke.zip',
                    environment=new_environment)
            assert volume_fingerprints(image, old_volume) == original, 'restore changed the original data volume'
            compose('up', '--detach', '--no-build', '--force-recreate', environment=new_environment)
            restored_container = current_container(new_environment)
            wait_ready(restored_container)
            assert_container_volume(restored_container, new_volume)
            verify_http(restored_container, username, password)
            key = command('docker', 'exec', restored_container, 'sha256sum', '/var/lib/relaylab/data/master.key').split()[0]
            assert key == original['/source/data/master.key'], 'restored encryption key changed'
            compose('stop', environment=new_environment)
            assert command('docker', 'inspect', '--format', '{{.State.ExitCode}}', restored_container) == '0'
            assert volume_fingerprints(image, old_volume) == original, 'switching to restored volume changed the original'
            print(json.dumps({'image': image, 'compose': str(source), 'passed': [
                'environment selects requested volume', 'actual container mount matches selection',
                'new empty volume restore', 'original database/key/backup unchanged',
                'restored administrator login', 'restored master key matches',
                'restored serve and worker ready'], 'upstream_calls': 0}))
        except Exception:
            for container in containers:
                print(f'Compose runtime diagnostic: {container}\n{json.dumps(read_runtime_reports(container), sort_keys=True)}')
                logs = subprocess.run(['docker', 'logs', '--tail', '80', container], capture_output=True, text=True)
                print(logs.stdout + '\n' + logs.stderr)
            raise
        finally:
            # Test-created random resources only. Never use down --volumes: the
            # verification must prove normal Compose teardown preserves data.
            for environment in (new_environment, old_environment):
                subprocess.run([*prefix, 'down', '--remove-orphans'], env=environment,
                               capture_output=True, timeout=780, check=False)
            for volume in (old_volume, new_volume):
                subprocess.run(['docker', 'volume', 'rm', volume], capture_output=True, check=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--image', required=True)
    parser.add_argument('--compose-file', type=Path)
    args = parser.parse_args()
    run(args.image)
    if args.compose_file:
        run_compose_restore(args.image, args.compose_file)
