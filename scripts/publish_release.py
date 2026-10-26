#!/usr/bin/env python3
"""Upload and verify a draft before making it the stable release."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile
from release import ROOT, digest, read_version, validate_release


def gh(*args, check=True):
    return subprocess.run(['gh', *map(str, args)], check=check, text=True, capture_output=True)


def require_draft(tag):
    result = gh('release', 'view', tag, '--json', 'tagName,name,isDraft,isPrerelease,assets')
    data = json.loads(result.stdout)
    if not data['isDraft']:
        raise RuntimeError('Published releases are immutable; use a new VERSION and tag')
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--dist', type=Path, default=ROOT / 'dist')
    args = parser.parse_args()
    version = read_version()
    if args.tag != f'v{version}':
        parser.error('tag must match VERSION exactly')
    validate_release(args.dist, version)
    notes = ROOT / 'docs' / 'releases' / f'{args.tag}.md'
    body = notes.read_text(encoding='utf-8')
    title = body.splitlines()[0].lstrip('# ').strip()
    if not title.startswith(f'Release {args.tag} - '):
        parser.error('notes must begin with Release vMAJOR.MINOR.PATCH - Chinese short title')
    gh('api', f'repos/{{owner}}/{{repo}}/git/ref/tags/{args.tag}')
    probe = gh('release', 'view', args.tag, '--json', 'isDraft', check=False)
    if probe.returncode == 0:
        require_draft(args.tag)
    else:
        gh('release', 'create', args.tag, '--verify-tag', '--draft', '--title', title, '--notes-file', notes)
    require_draft(args.tag)
    gh('release', 'edit', args.tag, '--draft=true', '--prerelease=false', '--latest=false', '--title', title, '--notes-file', notes)
    manifest = json.loads((args.dist / 'manifest.json').read_text(encoding='utf-8'))
    names = sorted([asset['name'] for asset in manifest['assets']] + ['manifest.json', 'SHA256SUMS'])
    for asset in require_draft(args.tag)['assets']:
        if asset['name'] not in names:
            gh('release', 'delete-asset', args.tag, asset['name'], '--yes')
    gh('release', 'upload', args.tag, *[args.dist / name for name in names], '--clobber')
    remote = require_draft(args.tag)
    if {asset['name'] for asset in remote['assets']} != set(names):
        raise RuntimeError('draft asset names are incomplete')
    for asset in remote['assets']:
        if asset['size'] != (args.dist / asset['name']).stat().st_size:
            raise RuntimeError(f'draft size mismatch: {asset["name"]}')
    with tempfile.TemporaryDirectory(prefix='relaylab-release-verify-') as temporary:
        gh('release', 'download', args.tag, '--dir', temporary, '--pattern', '*')
        for name in names:
            if digest(Path(temporary) / name) != digest(args.dist / name):
                raise RuntimeError(f'draft checksum mismatch: {name}')
    require_draft(args.tag)
    gh('release', 'edit', args.tag, '--draft=false', '--prerelease=false', '--latest=true')
    print(f'Published {args.tag} after verifying {len(names)} assets')


if __name__ == '__main__':
    main()
