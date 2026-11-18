"""Fail-closed npm audit gate with a short-lived, lock-bound dev-only exception.

Remove the exception once a patched picomatch release is available. The
unpatched advisory is GHSA-fc5q-39xc-vjpf (CVE-2026-112366).
"""

from __future__ import annotations

from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
LOCKFILE = Path('web/package-lock.json')
# Reviewed v1.6.2 web/package-lock.json; any change requires a fresh review.
EXCEPTION_LOCK_SHA256 = '73ef78c39977f0a065fbf0b04e045797457194edd2edf34e5aae7f5e625cbcac'
EXCEPTION_EXPIRES = datetime(2026, 11, 25, tzinfo=timezone.utc)
ADVISORY_URL = 'https://github.com/advisories/GHSA-fc5q-39xc-vjpf'
ADVISORY_SOURCE = 1247710
SEVERITIES = ('info', 'low', 'moderate', 'high', 'critical')
EXPECTED_VIA = {
    '@vitejs/plugin-react': ('vite',),
    'fdir': ('picomatch',),
    'picomatch': ('ADVISORY',),
    'tinyglobby': ('fdir', 'picomatch'),
    'vite': ('picomatch', 'tinyglobby'),
    'vitest': ('picomatch', 'tinyglobby', 'vite'),
}
DIRECT = {'@vitejs/plugin-react', 'vite', 'vitest'}


class AuditGateError(ValueError):
    """The audit did not prove a clean or precisely acknowledged state."""


def _require(condition: bool, explanation: str) -> None:
    if not condition:
        raise AuditGateError(explanation)


def _report(result: subprocess.CompletedProcess[str], label: str) -> tuple[dict, dict]:
    try:
        report = json.loads(result.stdout)
    except (TypeError, ValueError) as exc:
        raise AuditGateError(f'{label}: npm audit did not return valid JSON') from exc
    _require(isinstance(report, dict) and report.get('auditReportVersion') == 2,
             f'{label}: expected npm audit report version 2')
    _require('error' not in report, f'{label}: npm audit returned an error')
    findings = report.get('vulnerabilities')
    metadata = report.get('metadata')
    _require(isinstance(findings, dict) and isinstance(metadata, dict),
             f'{label}: missing vulnerability report or metadata')
    counts = metadata.get('vulnerabilities')
    dependencies = metadata.get('dependencies')
    _require(isinstance(counts, dict) and isinstance(dependencies, dict),
             f'{label}: missing vulnerability or dependency counts')
    for field in (*SEVERITIES, 'total'):
        _require(type(counts.get(field)) is int and counts[field] >= 0,
                 f'{label}: invalid {field} count')
    for field in ('prod', 'dev', 'optional', 'peer', 'peerOptional', 'total'):
        _require(type(dependencies.get(field)) is int and dependencies[field] >= 0,
                 f'{label}: invalid dependency {field} count')
    _require(counts['total'] == sum(counts[field] for field in SEVERITIES),
             f'{label}: inconsistent vulnerability totals')
    actual = dict.fromkeys(SEVERITIES, 0)
    for name, finding in findings.items():
        _require(isinstance(name, str) and isinstance(finding, dict)
                 and finding.get('name') == name, f'{label}: malformed finding {name!r}')
        severity = finding.get('severity')
        _require(severity in actual, f'{label}: invalid severity for {name}')
        actual[severity] += 1
    _require(all(counts[field] == actual[field] for field in SEVERITIES),
             f'{label}: metadata counts do not match findings')
    return report, findings


def _check_exception(findings: dict, lock_bytes: bytes, expected_sha256: str,
                     now: datetime) -> None:
    _require(now.tzinfo is not None and now < EXCEPTION_EXPIRES,
             'temporary picomatch exception expired; review the advisory and remove the exception')
    _require(len(expected_sha256) == 64 and
             all(character in '0123456789abcdefABCDEF' for character in expected_sha256),
             'temporary picomatch exception has no final lockfile SHA-256')
    _require(hashlib.sha256(lock_bytes).hexdigest() == expected_sha256.lower(),
             'web/package-lock.json changed since the temporary exception was reviewed')
    try:
        lock = json.loads(lock_bytes)
    except (TypeError, ValueError) as exc:
        raise AuditGateError('web/package-lock.json is not valid JSON') from exc
    _require(isinstance(lock, dict) and lock.get('lockfileVersion') == 3
             and isinstance(lock.get('packages'), dict), 'unexpected lockfile format')
    packages = lock['packages']
    _require(set(findings) == set(EXPECTED_VIA),
             'full audit has a new or missing finding outside the exact six-package exception')
    for name, expected_via in EXPECTED_VIA.items():
        finding = findings[name]
        _require(finding.get('severity') == 'high', f'{name}: changed severity')
        _require(finding.get('isDirect') is (name in DIRECT),
                 f'{name}: changed direct dependency status')
        expected_node = f'node_modules/{name}'
        nodes = finding.get('nodes')
        _require(nodes == [expected_node], f'{name}: changed affected node paths')
        for node in nodes:
            _require(node in packages and isinstance(packages[node], dict)
                     and packages[node].get('dev') is True,
                     f'{name}: affected node is missing from lockfile or not dev-only')
        via = finding.get('via')
        _require(isinstance(via, list) and len(via) == len(expected_via),
                 f'{name}: changed advisory/dependency chain')
        if name == 'picomatch':
            advisory = via[0]
            _require(isinstance(advisory, dict) and
                     advisory.get('source') == ADVISORY_SOURCE and
                     advisory.get('name') == 'picomatch' and
                     advisory.get('dependency') == 'picomatch' and
                     advisory.get('url') == ADVISORY_URL and
                     advisory.get('severity') == 'high' and
                     advisory.get('range') == '<=4.0.7' and
                     packages[expected_node].get('version') == '4.0.7',
                     'picomatch: different advisory or locked package version')
        else:
            _require(tuple(via) == expected_via,
                     f'{name}: changed advisory/dependency chain')


def evaluate_reports(production: subprocess.CompletedProcess[str],
                     full: subprocess.CompletedProcess[str], lock_bytes: bytes,
                     expected_sha256: str, now: datetime) -> bool:
    """Return True for the one permitted dev-only exception; False when clean."""
    _, production_findings = _report(production, 'production dependencies')
    _require(production.returncode == 0 and not production_findings,
             'production npm audit must pass with zero vulnerabilities of every severity')
    _, full_findings = _report(full, 'full dependency graph')
    if not full_findings:
        _require(full.returncode == 0, 'clean full audit exited unsuccessfully')
        return False
    _require(full.returncode == 1, 'full npm audit failed with an unexpected exit code')
    _check_exception(full_findings, lock_bytes, expected_sha256, now)
    return True


def run(root: Path = ROOT, expected_sha256: str = EXCEPTION_LOCK_SHA256,
        now: datetime | None = None, runner=subprocess.run) -> bool:
    npm = shutil.which('npm')
    _require(npm is not None, 'npm executable is unavailable')
    commands = (
        [npm, '--prefix', 'web', 'audit', '--omit=dev', '--audit-level=low', '--json'],
        [npm, '--prefix', 'web', 'audit', '--json'],
    )
    results = []
    for command in commands:
        try:
            results.append(runner(command, cwd=root, capture_output=True, text=True,
                                  timeout=120, check=False))
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise AuditGateError(f'npm audit could not complete: {exc}') from exc
    try:
        lock_bytes = (root / LOCKFILE).read_bytes()
    except OSError as exc:
        raise AuditGateError('web/package-lock.json cannot be read') from exc
    return evaluate_reports(results[0], results[1], lock_bytes, expected_sha256,
                            now if now is not None else datetime.now(timezone.utc))


def main() -> int:
    try:
        acknowledged = run()
    except AuditGateError as exc:
        print(f'npm audit gate FAILED: {exc}', file=sys.stderr)
        return 1
    if acknowledged:
        message = (f'Production npm audit: 0 vulnerabilities. Acknowledged unpatched '
                   f'BUILD/TEST-only {ADVISORY_URL} in picomatch 4.0.7 and exactly '
                   f'five affected dev dependencies; exception expires '
                   f'{EXCEPTION_EXPIRES.isoformat().replace("+00:00", "Z")}.')
    else:
        message = 'Production and full npm audit: 0 vulnerabilities.'
    print(message)
    summary = os.environ.get('GITHUB_STEP_SUMMARY')
    if summary:
        try:
            with open(summary, 'a', encoding='utf-8') as output:
                output.write(f'### npm audit gate\n\n{message}\n')
        except OSError as exc:
            print(f'npm audit gate FAILED: cannot write GitHub step summary: {exc}',
                  file=sys.stderr)
            return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
