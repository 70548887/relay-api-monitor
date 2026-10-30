"""Exercise destructive-operation guards with a fake Docker CLI, not a daemon."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
BASH = shutil.which('bash') or ('C:/Program Files/Git/bin/bash.exe' if Path('C:/Program Files/Git/bin/bash.exe').exists() else None)


@unittest.skipUnless(BASH, 'POSIX shell is unavailable')
class DockerRestoreGuards(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix='relaylab-restore-script-')
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.log = self.root / 'docker.log'
        self.owner = self.root / 'owner'
        self.backup = self.root / 'backup with space.zip.age'
        self.backup.write_bytes(b'opaque encrypted fixture')
        self.identity = self.root / 'recovery.key'
        self.identity.write_text('SECRET-MUST-NOT-ENTER-ARGV')
        docker = self.root / 'docker'
        docker.write_text('''#!/bin/sh
set -eu
printf '%s\\n' "$*" >> "$FAKE_LOG"
case "$1:${2:-}" in
 compose:version|image:inspect) exit 0;;
 volume:inspect)
   if [ "${3:-}" = --format ]; then
     if [ "${FAKE_RACE:-0}" = 1 ]; then printf 'another-owner\\n'; else cat "$FAKE_OWNER"; fi
     exit 0
   fi
   [ "${FAKE_EXISTING:-0}" = 1 ] && exit 0
   exit 1;;
 volume:create) printf '%s' "${4#relaylab.restore=}" > "$FAKE_OWNER"; exit 0;;
 compose:config) printf 'relaylab:test\\n'; exit 0;;
 compose:stop) exit 0;;
 run:*) [ "${FAKE_RUN_FAIL:-0}" = 1 ] && exit 42; exit 0;;
esac
[ "$1" = info ] && exit 0
exit 1
''', encoding='utf-8', newline='\n')
        docker.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ['PATH'], FAKE_LOG=str(self.log).replace('\\', '/'), FAKE_OWNER=str(self.owner).replace('\\', '/'))

    def run_script(self, **extra):
        return subprocess.run([BASH, str(ROOT / 'scripts/restore-docker.sh').replace('\\', '/'), str(self.backup).replace('\\', '/'), 'relaylab-restored-test', str(self.identity).replace('\\', '/')], env=dict(self.env, **extra), text=True, capture_output=True, timeout=20)

    def commands(self):
        return self.log.read_text() if self.log.exists() else ''

    def test_existing_volume_never_stops_or_runs(self):
        result = self.run_script(FAKE_EXISTING='1')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn('compose stop', self.commands())
        self.assertNotIn('run --rm', self.commands())

    def test_racing_volume_owner_never_stops_or_runs(self):
        result = self.run_script(FAKE_RACE='1')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn('compose stop', self.commands())
        self.assertNotIn('run --rm', self.commands())

    def test_new_volume_restore_uses_readonly_files_and_does_not_switch(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        commands = self.commands()
        self.assertIn('compose stop -t 720 relaylab', commands)
        self.assertLess(commands.index('run --rm'), commands.index('compose stop'))
        self.assertIn('target=/restore-input/identity,readonly', commands)
        self.assertIn('--network none --read-only', commands)
        self.assertIn('--identity-file /restore-input/identity', commands)
        self.assertNotIn('SECRET-MUST-NOT-ENTER-ARGV', commands)
        self.assertNotIn('docker.sock', commands)
        self.assertNotIn('compose up', commands)
        self.assertNotIn('volume rm', commands)
        self.assertIn('RELAYLAB_DATA_VOLUME=relaylab-restored-test', result.stdout)

    def test_failed_restore_keeps_original_and_new_volume(self):
        result = self.run_script(FAKE_RUN_FAIL='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Original service and volume are unchanged', result.stderr)
        self.assertNotIn('compose stop', self.commands())
        self.assertNotIn('volume rm', self.commands())
        self.assertNotIn('compose up', self.commands())


if __name__ == '__main__':
    unittest.main()
