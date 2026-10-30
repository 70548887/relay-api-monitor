#!/usr/bin/env python3
"""Build and package the same release contract locally and in GitHub Actions."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import zipfile
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[1]
TARGETS = (('windows', 'amd64'), ('linux', 'amd64'), ('linux', 'arm64'))
PROGRAMS = ('relaylab', 'relaylab-launcher')
SCRIPT_NAMES = ('start.ps1', 'stop.ps1', 'process-common.ps1', 'backup.ps1', 'restore.ps1', 'start.sh', 'restore-docker.sh')
DOCKER_RUNTIME_FILES = {
    'Dockerfile': 'deploy/docker/Dockerfile.release',
    'compose.yaml': 'deploy/docker/compose.release.yaml',
    '.dockerignore': '.dockerignore',
    'deploy/docker/config.container.yaml': 'deploy/docker/config.container.yaml',
}


def read_version(root=ROOT):
    version = (root / 'VERSION').read_text(encoding='utf-8').strip()
    if not re.fullmatch(r'(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)', version):
        raise ValueError('VERSION must contain MAJOR.MINOR.PATCH; prereleases cannot be stable releases')
    return version


def filename(program, goos, arch):
    return f'{program}-{goos}-{arch}' + ('.exe' if goos == 'windows' else '')


def atomic_write(path, content):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as handle:
        temporary = Path(handle.name)
        handle.write(content)
    try:
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def digest(path):
    with path.open('rb') as handle:
        return hashlib.file_digest(handle, 'sha256').hexdigest()


def validate_metadata(commit, build_time):
    if not re.fullmatch(r'(?:[0-9a-f]{40}|[0-9a-f]{64})', commit):
        raise ValueError('commit must be a full hexadecimal Git commit')
    if not re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z', build_time):
        raise ValueError('build_time must be one UTC RFC3339 timestamp')
    datetime.strptime(build_time, '%Y-%m-%dT%H:%M:%SZ')


def package_files(root, goos, arch, binary_dir, local_names=False):
    files = {}
    for program in PROGRAMS:
        installed = program + ('.exe' if goos == 'windows' else '')
        source = binary_dir / (installed if local_names else filename(program, goos, arch))
        if not source.is_file():
            raise ValueError(f'missing executable: {source}')
        files[f'bin/{installed}'] = (source, 0o755)
    required = ('VERSION', 'README.md', 'config.example.yaml')
    for name in required:
        files[name] = (root / name, 0o644)
    for name in SCRIPT_NAMES:
        files[f'scripts/{name}'] = (root / 'scripts' / name, 0o755 if name.endswith('.sh') else 0o644)
    for name in ('UPDATES.md', 'RELEASE.md', 'DOCKER.md', 'BACKUPS.md', 'BACKUP-VALIDATION.md', 'BACKUP-AUDIT.md', 'BACKUP-REPAIR.md'):
        files[f'docs/{name}'] = (root / 'docs' / name, 0o644)
    if goos == 'linux':
        # Complete Linux archives have binaries but no Go/React source tree.
        # Use the runtime-only recipe, rather than the source-build Dockerfile.
        for installed, source in DOCKER_RUNTIME_FILES.items():
            files[installed] = (root / source, 0o644)
    notes = root / 'docs' / 'releases' / f'v{read_version(root)}.md'
    files[f'docs/releases/{notes.name}'] = (notes, 0o644)
    for path in sorted((root / 'deploy').glob('*')):
        if path.is_file() and (path.name.endswith('.service') or path.name.endswith('.example') or path.name.endswith('.md')):
            files[f'deploy/{path.name}'] = (path, 0o644)
    files['THIRD_PARTY_NOTICES.txt'] = (root / 'release' / 'THIRD_PARTY_NOTICES.txt', 0o644)
    for name, (path, _) in files.items():
        if not path.is_file():
            raise ValueError(f'missing package input {name}: {path}')
        if name.endswith('.ps1'):
            raw = path.read_bytes()
            if any(byte >= 128 for byte in raw) and not raw.startswith(b'\xef\xbb\xbf'):
                raise ValueError(f'Windows PowerShell 5 requires UTF-8 BOM for non-ASCII script: {name}')
    return files


def archive(path, prefix, files):
    # Stable timestamps/modes make packaging repeatable for identical inputs.
    buffer = io.BytesIO()
    if path.suffix == '.zip':
        with zipfile.ZipFile(buffer, 'w', zipfile.ZIP_DEFLATED) as output:
            for name, (source, mode) in sorted(files.items()):
                info = zipfile.ZipInfo(f'{prefix}/{name}', (2020, 1, 1, 0, 0, 0))
                info.external_attr = (0o100000 | mode) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                output.writestr(info, source.read_bytes())
    else:
        with gzip.GzipFile(fileobj=buffer, mode='wb', mtime=0, filename='') as compressed:
            with tarfile.open(fileobj=compressed, mode='w') as output:
                for name, (source, mode) in sorted(files.items()):
                    content = source.read_bytes()
                    info = tarfile.TarInfo(f'{prefix}/{name}')
                    info.size, info.mode, info.mtime = len(content), mode, 0
                    output.addfile(info, io.BytesIO(content))
    atomic_write(path, buffer.getvalue())


def build(root, output, version, commit, build_time, targets):
    flags = f'-s -w -X main.version={version} -X main.commit={commit} -X main.buildTime={build_time}'
    for goos, arch in targets:
        environment = dict(os.environ, GOOS=goos, GOARCH=arch, CGO_ENABLED='0')
        for program in PROGRAMS:
            subprocess.run(['go', 'build', '-trimpath', '-ldflags', flags, '-o', str(output / filename(program, goos, arch)), f'./cmd/{program}'], cwd=root, env=environment, check=True)


def create_release(root, binary_dir, output, version, commit, build_time, targets=TARGETS, local_names=False):
    if version != read_version(root):
        raise ValueError('requested version does not match VERSION')
    validate_metadata(commit, build_time)
    output.mkdir(parents=True, exist_ok=True)
    assets = []
    names = []
    for goos, arch in targets:
        for program in PROGRAMS:
            name = filename(program, goos, arch)
            source = binary_dir / (program + ('.exe' if goos == 'windows' else '') if local_names else name)
            if source.resolve() != (output / name).resolve():
                atomic_write(output / name, source.read_bytes())
            names.append(name)
        name = f'relaylab-{goos}-{arch}-{version}' + ('.zip' if goos == 'windows' else '.tar.gz')
        archive(output / name, f'relaylab-{goos}-{arch}-{version}', package_files(root, goos, arch, binary_dir, local_names))
        names.append(name)
        for name in names[-3:]:
            path = output / name
            assets.append(dict(name=name, os=goos, arch=arch, size=path.stat().st_size, sha256=digest(path)))
    manifest = dict(version=version, commit=commit, build_time=build_time, launcher_protocol=2, usage_guard=1, database_schema=4, min_database_schema=1, assets=assets)
    atomic_write(output / 'manifest.json', (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode('utf-8'))
    names.append('manifest.json')
    atomic_write(output / 'SHA256SUMS', ''.join(f'{digest(output / name)}  {name}\n' for name in sorted(names)).encode('ascii'))
    validate_release(output, version, targets)
    return names + ['SHA256SUMS']


def validate_release(output, version, targets=TARGETS):
    manifest = json.loads((output / 'manifest.json').read_text(encoding='utf-8'))
    validate_metadata(manifest['commit'], manifest['build_time'])
    if manifest['version'] != version or manifest['launcher_protocol'] != 2 or manifest.get('usage_guard') != 1 or manifest['database_schema'] != 4 or manifest['min_database_schema'] != 1:
        raise ValueError('invalid release manifest compatibility contract')
    expected = {filename(program, goos, arch) for goos, arch in targets for program in PROGRAMS}
    expected |= {f'relaylab-{goos}-{arch}-{version}' + ('.zip' if goos == 'windows' else '.tar.gz') for goos, arch in targets}
    if {item['name'] for item in manifest['assets']} != expected or len(manifest['assets']) != len(expected):
        raise ValueError('unexpected or missing release assets')
    for asset in manifest['assets']:
        path = output / asset['name']
        platform_prefix = ('relaylab-launcher-' if asset['name'].startswith('relaylab-launcher-') else 'relaylab-') + f'{asset["os"]}-{asset["arch"]}'
        if not asset['name'].startswith(platform_prefix) or (asset['os'], asset['arch']) not in targets:
            raise ValueError(f'asset platform mismatch: {path.name}')
        if asset['size'] <= 0 or path.stat().st_size != asset['size'] or digest(path) != asset['sha256']:
            raise ValueError(f'asset checksum/size mismatch: {path.name}')
    sums = {}
    for line in (output / 'SHA256SUMS').read_text(encoding='ascii').splitlines():
        checksum, name = line.split('  ', 1)
        if Path(name).name != name or name in sums:
            raise ValueError('checksum names must be unique base filenames')
        sums[name] = checksum
    if set(sums) != expected | {'manifest.json'}:
        raise ValueError('checksum list must cover every asset and manifest')
    for name, checksum in sums.items():
        if digest(output / name) != checksum:
            raise ValueError(f'checksum mismatch: {name}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--output', type=Path, default=ROOT / 'dist')
    parser.add_argument('--binary-dir', type=Path)
    parser.add_argument('--windows-only', action='store_true')
    parser.add_argument('--local-names', action='store_true')
    parser.add_argument('--skip-build', action='store_true')
    parser.add_argument('--validate-only', action='store_true')
    parser.add_argument('--tag')
    parser.add_argument('--commit')
    parser.add_argument('--build-time')
    args = parser.parse_args()
    version = read_version(args.root)
    if args.tag and args.tag != f'v{version}':
        parser.error('release tag must exactly match v + VERSION')
    targets = TARGETS[:1] if args.windows_only else TARGETS
    if args.validate_only:
        validate_release(args.output, version, targets)
        print('Release assets and checksums verified')
        return
    commit = args.commit or subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=args.root, text=True).strip()
    build_time = args.build_time or datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    args.output.mkdir(parents=True, exist_ok=True)
    binary_dir = args.binary_dir or args.output
    if not args.skip_build:
        build(args.root, binary_dir, version, commit, build_time, targets)
    names = create_release(args.root, binary_dir, args.output, version, commit, build_time, targets, args.local_names)
    print(f'Packaged {version}: ' + ', '.join(names))


if __name__ == '__main__':
    main()
