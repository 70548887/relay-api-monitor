import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace
import zipfile
import release
import publish_release


class PackagingTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='relaylab-packaging-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.binaries, self.output = self.root / 'bin', self.root / 'dist'
        self.binaries.mkdir()
        files = ('README.md', 'config.example.yaml', 'docs/UPDATES.md', 'docs/RELEASE.md', 'docs/DOCKER.md', 'docs/releases/v9.9.9.md', 'release/THIRD_PARTY_NOTICES.txt', 'deploy/relaylab-launcher.service', 'deploy/relaylab.env.example', '.dockerignore', *release.DOCKER_RUNTIME_FILES.values(), *[f'scripts/{name}' for name in release.SCRIPT_NAMES])
        for name in files:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('fixture', encoding='utf-8')
        (self.root / 'VERSION').write_text('9.9.9\n', encoding='utf-8')
        for goos, arch in release.TARGETS:
            for program in release.PROGRAMS:
                (self.binaries / release.filename(program, goos, arch)).write_bytes(f'{program}/{goos}/{arch}'.encode())
        for name in ('config.yaml', '.env.production', 'data/master.key', 'deploy/relaylab.env', 'deploy/private.json', 'deploy/docker/private.env'):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('PRIVATE MUST NOT SHIP')

    def package(self):
        return release.create_release(self.root, self.binaries, self.output, '9.9.9', 'a' * 40, '2026-10-26T00:00:00Z')

    def test_complete_archives_manifest_and_base_filename_checksums(self):
        self.assertEqual(len(self.package()), 11)
        manifest = json.loads((self.output / 'manifest.json').read_text())
        self.assertEqual(len(manifest['assets']), 9)
        self.assertEqual(manifest['launcher_protocol'], 2)
        self.assertEqual(manifest['usage_guard'], 1)
        self.assertEqual(manifest['min_database_schema'], 1)
        for goos, arch in release.TARGETS:
            stem = f'relaylab-{goos}-{arch}-9.9.9'
            path = self.output / (stem + ('.zip' if goos == 'windows' else '.tar.gz'))
            if goos == 'windows':
                with zipfile.ZipFile(path) as archive:
                    files = archive.namelist()
            else:
                with tarfile.open(path) as archive:
                    files = archive.getnames()
                    self.assertEqual(archive.getmember(stem + '/bin/relaylab').mode, 0o755)
            ext = '.exe' if goos == 'windows' else ''
            for required in (f'bin/relaylab{ext}', f'bin/relaylab-launcher{ext}', 'VERSION', 'config.example.yaml', 'scripts/start.ps1', 'scripts/stop.ps1', 'scripts/start.sh', 'docs/UPDATES.md', 'docs/RELEASE.md', 'docs/DOCKER.md', 'deploy/relaylab-launcher.service', 'deploy/relaylab.env.example', 'THIRD_PARTY_NOTICES.txt'):
                self.assertIn(f'{stem}/{required}', files)
            if goos == 'linux':
                for required in release.DOCKER_RUNTIME_FILES:
                    self.assertIn(f'{stem}/{required}', files)
            else:
                self.assertNotIn(f'{stem}/Dockerfile', files)
                self.assertNotIn(f'{stem}/compose.yaml', files)
            self.assertFalse(any(name.endswith(('config.yaml', 'master.key', '.env.production', 'relaylab.env', 'private.json', 'private.env')) for name in files))
        self.assertNotIn('dist/', (self.output / 'SHA256SUMS').read_text())
        release.validate_release(self.output, '9.9.9')

    def test_manifest_requires_usage_guard(self):
        self.package()
        path = self.output / 'manifest.json'
        original = json.loads(path.read_text())
        for guard in (None, 0, 2):
            with self.subTest(usage_guard=guard):
                manifest = dict(original)
                if guard is None:
                    manifest.pop('usage_guard')
                else:
                    manifest['usage_guard'] = guard
                path.write_text(json.dumps(manifest))
                with self.assertRaisesRegex(ValueError, 'compatibility contract'):
                    release.validate_release(self.output, '9.9.9')

    def test_complete_linux_package_uses_prebuilt_binary_docker_recipe(self):
        (self.root / 'Dockerfile').write_text('source build requires source tree')
        (self.root / 'compose.yaml').write_text('source compose')
        (self.root / 'deploy/docker/Dockerfile.release').write_text('COPY bin/relaylab /app/relaylab')
        (self.root / 'deploy/docker/compose.release.yaml').write_text('prebuilt binary compose')
        self.package()
        with tarfile.open(self.output / 'relaylab-linux-amd64-9.9.9.tar.gz') as package:
            prefix = 'relaylab-linux-amd64-9.9.9/'
            self.assertEqual(package.extractfile(prefix + 'Dockerfile').read(), b'COPY bin/relaylab /app/relaylab')
            self.assertEqual(package.extractfile(prefix + 'compose.yaml').read(), b'prebuilt binary compose')
            self.assertNotIn(prefix + 'deploy/docker/private.env', package.getnames())

    def test_tampering_or_incomplete_release_is_rejected(self):
        self.package()
        (self.output / 'relaylab-linux-arm64').write_bytes(b'tampered')
        with self.assertRaisesRegex(ValueError, 'mismatch'):
            release.validate_release(self.output, '9.9.9')

    def test_packaging_is_deterministic_and_uses_one_build_time(self):
        names = self.package()
        first = {name: (self.output / name).read_bytes() for name in names}
        self.package()
        self.assertEqual(first, {name: (self.output / name).read_bytes() for name in names})
        self.assertEqual(json.loads(first['manifest.json'])['build_time'], '2026-10-26T00:00:00Z')

    def test_version_mismatch_and_prerelease_are_rejected(self):
        with self.assertRaisesRegex(ValueError, 'VERSION'):
            release.create_release(self.root, self.binaries, self.output, '9.9.8', 'a' * 40, '2026-10-26T00:00:00Z')
        (self.root / 'VERSION').write_text('9.9.9-rc.1')
        with self.assertRaisesRegex(ValueError, 'stable'):
            release.read_version(self.root)

    def test_invalid_commit_time_and_manifest_platform_are_rejected(self):
        with self.assertRaisesRegex(ValueError, 'commit'):
            release.create_release(self.root, self.binaries, self.output, '9.9.9', 'a' * 41, '2026-10-26T00:00:00Z')
        with self.assertRaises(ValueError):
            release.create_release(self.root, self.binaries, self.output, '9.9.9', 'a' * 40, '2026-99-26T00:00:00Z')
        self.package()
        manifest = json.loads((self.output / 'manifest.json').read_text())
        manifest['assets'][0]['os'] = 'linux'
        (self.output / 'manifest.json').write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'platform mismatch'):
            release.validate_release(self.output, '9.9.9')


class PublicationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='relaylab-publish-test-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        notes = self.root / 'docs/releases/v9.9.9.md'
        notes.parent.mkdir(parents=True)
        notes.write_text('# Release v9.9.9 - 测试发布\n\n修复与安装说明。', encoding='utf-8')
        self.dist = self.root / 'dist'
        self.dist.mkdir()
        (self.dist / 'relaylab-linux-amd64').write_bytes(b'fixture executable')
        (self.dist / 'manifest.json').write_text(json.dumps({'assets': [{'name': 'relaylab-linux-amd64'}]}))
        (self.dist / 'SHA256SUMS').write_text('checksums validated separately')
        for target in (patch.object(publish_release, 'ROOT', self.root), patch.object(publish_release, 'read_version', return_value='9.9.9'), patch.object(publish_release, 'validate_release'), patch('sys.argv', ['publish_release.py', '--tag', 'v9.9.9', '--dist', str(self.dist)])):
            target.start()
            self.addCleanup(target.stop)

    def fake_gh(self, draft=True, corrupt=False):
        names = ['relaylab-linux-amd64', 'manifest.json', 'SHA256SUMS']
        def response(*args, **kwargs):
            result = {'isDraft': draft, 'assets': [{'name': name, 'size': (self.dist / name).stat().st_size} for name in names]}
            if args[:2] == ('release', 'download'):
                destination = Path(args[args.index('--dir') + 1])
                for name in names:
                    (destination / name).write_bytes((self.dist / name).read_bytes())
                if corrupt:
                    (destination / 'relaylab-linux-amd64').write_bytes(b'corrupt upload')
            return SimpleNamespace(returncode=0, stdout=json.dumps(result), stderr='')
        return response

    def test_published_release_is_immutable(self):
        with patch.object(publish_release, 'gh', side_effect=self.fake_gh(draft=False)) as github:
            with self.assertRaisesRegex(RuntimeError, 'immutable'):
                publish_release.main()
            self.assertFalse(any(call.args[:2] in (('release', 'upload'), ('release', 'edit')) for call in github.call_args_list))

    def test_only_verified_draft_is_published_with_chinese_notes(self):
        with patch.object(publish_release, 'gh', side_effect=self.fake_gh()) as github:
            publish_release.main()
            calls = [call.args for call in github.call_args_list]
            self.assertIn('--notes-file', next(call for call in calls if call[:2] == ('release', 'edit')))
            self.assertIn('--draft=false', calls[-1])
            self.assertLess(next(i for i, call in enumerate(calls) if call[:2] == ('release', 'download')), len(calls)-1)

    def test_corrupt_draft_never_becomes_latest(self):
        with patch.object(publish_release, 'gh', side_effect=self.fake_gh(corrupt=True)) as github:
            with self.assertRaisesRegex(RuntimeError, 'checksum mismatch'):
                publish_release.main()
            self.assertFalse(any('--draft=false' in call.args for call in github.call_args_list))


if __name__ == '__main__':
    unittest.main()
