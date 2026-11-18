"""Offline regression tests for the narrow npm audit exception."""

from copy import deepcopy
from contextlib import redirect_stdout
from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import audit_npm


NOW = datetime(2026, 11, 18, tzinfo=timezone.utc)


def result(findings: dict, returncode: int) -> subprocess.CompletedProcess[str]:
    counts = {severity: 0 for severity in audit_npm.SEVERITIES}
    for finding in findings.values():
        counts[finding['severity']] += 1
    counts['total'] = sum(counts.values())
    report = {
        'auditReportVersion': 2,
        'vulnerabilities': findings,
        'metadata': {
            'vulnerabilities': counts,
            'dependencies': {
                'prod': 80, 'dev': 156, 'optional': 27,
                'peer': 10, 'peerOptional': 0, 'total': 235,
            },
        },
    }
    return subprocess.CompletedProcess(['npm', 'audit'], returncode,
                                       stdout=json.dumps(report), stderr='')


def affected_findings() -> dict:
    findings = {}
    for name, via in audit_npm.EXPECTED_VIA.items():
        entries = list(via)
        if name == 'picomatch':
            entries = [{
                'source': 1247710,
                'name': 'picomatch',
                'dependency': 'picomatch',
                'url': audit_npm.ADVISORY_URL,
                'severity': 'high',
                'range': '<=4.0.7',
            }]
        findings[name] = {
            'name': name,
            'severity': 'high',
            'isDirect': name in audit_npm.DIRECT,
            'via': entries,
            'nodes': [f'node_modules/{name}'],
        }
    return findings


class AuditGateTests(unittest.TestCase):
    def setUp(self):
        self.findings = affected_findings()
        self.lock = {
            'lockfileVersion': 3,
            'packages': {
                f'node_modules/{name}': {
                    'version': '4.0.7' if name == 'picomatch' else 'fixture',
                    'dev': True,
                }
                for name in audit_npm.EXPECTED_VIA
            },
        }
        self.clean = result({}, 0)

    def check(self, findings=None, lock=None, now=NOW, digest=None,
              production=None, full=None):
        lock_bytes = json.dumps(self.lock if lock is None else lock).encode()
        digest = hashlib.sha256(lock_bytes).hexdigest() if digest is None else digest
        return audit_npm.evaluate_reports(
            self.clean if production is None else production,
            result(self.findings if findings is None else findings, 1) if full is None else full,
            lock_bytes, digest, now,
        )

    def test_exact_known_dev_only_advisory_passes(self):
        self.assertTrue(self.check())

    def test_clean_full_report_passes_after_expiry_and_without_lock_binding(self):
        self.assertFalse(self.check(now=datetime(2027, 1, 1, tzinfo=timezone.utc),
                                    digest='UNSET', full=self.clean))

    def test_production_audit_must_be_clean_with_successful_exit(self):
        for production in (result(self.findings, 1), result({}, 1)):
            with self.subTest(code=production.returncode, findings=bool(json.loads(production.stdout)['vulnerabilities'])):
                with self.assertRaisesRegex(audit_npm.AuditGateError, 'production npm audit'):
                    self.check(production=production)

    def test_metadata_fake_zero_or_mismatched_total_fails(self):
        report = json.loads(result(self.findings, 1).stdout)
        for counts in ({severity: 0 for severity in (*audit_npm.SEVERITIES, 'total')},
                       {**report['metadata']['vulnerabilities'], 'total': 5}):
            with self.subTest(counts=counts):
                changed = deepcopy(report)
                changed['metadata']['vulnerabilities'] = counts
                full = subprocess.CompletedProcess(['npm'], 1, json.dumps(changed), '')
                with self.assertRaisesRegex(audit_npm.AuditGateError, 'counts|totals'):
                    self.check(full=full)

    def test_non_dev_or_missing_lock_node_fails(self):
        for mode in ('production', 'missing', 'omitted-dev-field'):
            with self.subTest(mode=mode):
                lock = deepcopy(self.lock)
                node = 'node_modules/vite'
                if mode == 'production':
                    lock['packages'][node]['dev'] = False
                elif mode == 'missing':
                    del lock['packages'][node]
                else:
                    del lock['packages'][node]['dev']
                with self.assertRaisesRegex(audit_npm.AuditGateError, 'not dev-only'):
                    self.check(lock=lock)

    def test_extra_advisory_source_fails_even_on_known_package(self):
        findings = deepcopy(self.findings)
        findings['picomatch']['via'].append({
            'source': 999, 'name': 'picomatch', 'url': 'https://example.invalid/another',
        })
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'changed advisory'):
            self.check(findings=findings)
        findings = deepcopy(self.findings)
        findings['picomatch']['via'][0]['source'] = 999
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'different advisory'):
            self.check(findings=findings)

    def test_advisory_url_and_picomatch_version_must_match(self):
        findings = deepcopy(self.findings)
        findings['picomatch']['via'][0]['url'] = 'https://example.invalid/advisory'
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'different advisory'):
            self.check(findings=findings)
        lock = deepcopy(self.lock)
        lock['packages']['node_modules/picomatch']['version'] = '4.0.8'
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'locked package version'):
            self.check(lock=lock)

    def test_wrong_transitive_graph_or_node_fails(self):
        for mutation in ('via', 'nodes', 'direct'):
            with self.subTest(mutation=mutation):
                findings = deepcopy(self.findings)
                if mutation == 'via':
                    findings['vite']['via'] = ['picomatch']
                elif mutation == 'nodes':
                    findings['vite']['nodes'] = ['node_modules/unexpected/vite']
                else:
                    findings['vite']['isDirect'] = False
                with self.assertRaises(audit_npm.AuditGateError):
                    self.check(findings=findings)

    def test_other_finding_of_any_severity_fails(self):
        for severity in audit_npm.SEVERITIES:
            with self.subTest(severity=severity):
                findings = deepcopy(self.findings)
                findings['other'] = {
                    'name': 'other', 'severity': severity,
                    'via': ['picomatch'], 'nodes': ['node_modules/other'],
                }
                with self.assertRaisesRegex(audit_npm.AuditGateError, 'new or missing finding'):
                    self.check(findings=findings)

    def test_expiry_and_changed_lock_hash_fail(self):
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'expired'):
            self.check(now=audit_npm.EXCEPTION_EXPIRES)
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'package-lock.json changed'):
            self.check(digest='0' * 64)
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'no final lockfile'):
            self.check(digest='UNSET_LOCK_SHA256')

    def test_malformed_json_report_or_audit_error_fails(self):
        malformed = subprocess.CompletedProcess(['npm'], 1, '{', 'network error')
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'valid JSON'):
            self.check(full=malformed)
        report = json.loads(result(self.findings, 1).stdout)
        report['error'] = {'summary': 'registry unavailable'}
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'returned an error'):
            self.check(full=subprocess.CompletedProcess(['npm'], 1, json.dumps(report), ''))
        report.pop('error')
        report['auditReportVersion'] = 1
        with self.assertRaisesRegex(audit_npm.AuditGateError, 'version 2'):
            self.check(full=subprocess.CompletedProcess(['npm'], 1, json.dumps(report), ''))

    def test_subprocess_failure_rejects_without_suppressing(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'web').mkdir()
            (root / audit_npm.LOCKFILE).write_text(json.dumps(self.lock))
            with patch.object(audit_npm.shutil, 'which', return_value='npm'):
                with self.assertRaisesRegex(audit_npm.AuditGateError, 'could not complete'):
                    audit_npm.run(root=root, runner=lambda *args, **kwargs: (_ for _ in ()).throw(
                        subprocess.TimeoutExpired('npm audit', 120)))

    def test_audit_commands_and_exit_status_are_checked(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'web').mkdir()
            (root / audit_npm.LOCKFILE).write_text(json.dumps(self.lock))
            digest = hashlib.sha256((root / audit_npm.LOCKFILE).read_bytes()).hexdigest()
            called = []

            def runner(command, **kwargs):
                called.append(command)
                self.assertEqual(kwargs['cwd'], root)
                self.assertFalse(kwargs['check'])
                return (self.clean, result(self.findings, 1))[len(called) - 1]

            with patch.object(audit_npm.shutil, 'which', return_value='npm'):
                self.assertTrue(audit_npm.run(root=root, expected_sha256=digest,
                                              now=NOW, runner=runner))
            self.assertEqual(called, [
                ['npm', '--prefix', 'web', 'audit', '--omit=dev', '--audit-level=low', '--json'],
                ['npm', '--prefix', 'web', 'audit', '--json'],
            ])
            with self.assertRaisesRegex(audit_npm.AuditGateError, 'unexpected exit code'):
                self.check(full=result(self.findings, 2))

    def test_accepted_exception_is_visible_in_ci_summary(self):
        with tempfile.TemporaryDirectory() as temporary:
            summary = Path(temporary) / 'summary.md'
            output = io.StringIO()
            with patch.object(audit_npm, 'run', return_value=True), \
                    patch.dict('os.environ', {'GITHUB_STEP_SUMMARY': str(summary)}), \
                    redirect_stdout(output):
                self.assertEqual(audit_npm.main(), 0)
            for text in (audit_npm.ADVISORY_URL, 'BUILD/TEST-only',
                         'Production npm audit: 0 vulnerabilities', '2026-11-25'):
                self.assertIn(text, output.getvalue())
                self.assertIn(text, summary.read_text(encoding='utf-8'))
            self.assertNotIn('full npm audit: 0 vulnerabilities', output.getvalue())


if __name__ == '__main__':
    unittest.main()
