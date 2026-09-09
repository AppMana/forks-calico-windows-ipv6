import importlib.util
import http.server
import json
from pathlib import Path
import subprocess
import shutil
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / 'windows-isolation-check.py'
spec = importlib.util.spec_from_file_location('windows_isolation', SCRIPT)
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


def arguments():
    return SimpleNamespace(kubeconfig='/isolated/config', context='test-vms',
        server='https://192.0.2.10:6443', cluster_uid='isolated-cluster',
        windows_node='windows-2022', linux_node='linux-worker',
        windows_build='10.0.20348', windows_image='windows@sha256:' + 'a' * 64,
        linux_image='linux@sha256:' + 'b' * 64, convergence_timeout=10)


class GuardChecker(checker.Checker):
    def __init__(self):
        super().__init__(arguments())
        self.server = self.args.server
        self.cluster_uid = self.args.cluster_uid
        self.build = self.args.windows_build
        self.setup_called = False

    def kubectl(self, *args, body=None):
        if args == ('config', 'view', '--minify', '-o', 'json'):
            return json.dumps({'clusters': [{'cluster': {'server': self.server}}]})
        raise AssertionError(f'Unexpected or mutating kubectl call: {args}')

    def get(self, resource, name, namespace=None):
        if resource == 'namespace':
            return {'metadata': {'uid': self.cluster_uid}}
        return {'metadata': {'labels': {
            'kubernetes.io/os': 'windows' if name == self.args.windows_node else 'linux',
            'node.kubernetes.io/windows-build': self.build}},
            'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}

    def setup(self):
        self.setup_called = True
        raise AssertionError('guard allowed setup')


class SafetyTests(unittest.TestCase):
    def test_wrong_cluster_cannot_create_or_delete_resources(self):
        for field, value in [('server', 'https://production.invalid'),
                             ('cluster_uid', 'production-cluster'),
                             ('build', '10.0.26100')]:
            with self.subTest(field=field):
                runner = GuardChecker()
                setattr(runner, field, value)
                with self.assertRaises(RuntimeError):
                    runner.run()
                self.assertFalse(runner.setup_called)

    def test_explicit_context_and_kubeconfig_on_every_command(self):
        runner = checker.Checker(arguments())
        with patch.object(checker.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout='{}')) as run:
            runner.get('namespace', 'kube-system')
        command = run.call_args.args[0]
        self.assertEqual(command[:5], ['kubectl', '--kubeconfig', '/isolated/config', '--context', 'test-vms'])

    def test_failed_exec_is_never_a_policy_denial(self):
        runner = checker.Checker(arguments())
        runner.pods = {'client': {'os': 'windows'}}
        with patch.object(checker.subprocess, 'run', return_value=SimpleNamespace(returncode=1, stderr='unable to upgrade connection')):
            with self.assertRaisesRegex(RuntimeError, 'exec failed'):
                runner.probe('client', 'fd00::1')

    def test_malformed_probe_is_not_a_policy_denial(self):
        runner = checker.Checker(arguments())
        runner.pods = {'client': {'os': 'windows'}}
        with patch.object(runner, 'kubectl', return_value='{"error":"bad client"}'):
            with self.assertRaisesRegex(RuntimeError, 'reachability'):
                runner.probe('client', 'fd00::1')

    def test_cleanup_does_not_delete_replaced_namespace(self):
        runner = GuardChecker()
        runner.setup = lambda: setattr(runner, 'namespace_uid', 'owned-namespace')
        runner.phase = lambda _: runner.results.append({'reachable': True})
        runner.policy = lambda *args: (_ for _ in ()).throw(RuntimeError('test interruption'))
        with self.assertRaisesRegex(RuntimeError, 'namespace identity changed'):
            runner.run()


class EvidenceTests(unittest.TestCase):
    def runner(self):
        runner = checker.Checker(arguments())
        runner.pods = {
            'windows-allow': {'os': 'windows', 'role': 'allow', 'ips': {6: 'fd00::a'}},
            'windows-deny': {'os': 'windows', 'role': 'deny', 'ips': {6: 'fd00::b'}},
        }
        runner.targets = [('linux-backend', 'service', 6, 'fd98::1')]
        return runner

    def test_service_connectivity_with_gateway_snat_fails_identity(self):
        runner = self.runner()
        runner.probe = lambda *args: {'reachable': True, 'source': 'fd00::gateway'}
        observed = runner.matrix('baseline')
        self.assertTrue(all(row['reachable'] for row in observed))
        self.assertFalse(any(row['pass'] for row in observed))

    def test_same_node_denied_client_cannot_inherit_allowed_identity(self):
        runner = self.runner()
        runner.probe = lambda client, _: {'reachable': True, 'source': runner.pods[client]['ips'][6]}
        rows = runner.matrix('ingress')
        self.assertTrue(rows[0]['pass'])
        self.assertFalse(rows[1]['pass'])

    def test_both_allowed_and_denied_are_required(self):
        runner = self.runner()
        runner.probe = lambda *args: {'reachable': False, 'error': 'connection timed out'}
        rows = runner.matrix('ingress')
        self.assertFalse(rows[0]['pass'])
        self.assertTrue(rows[1]['pass'])

    def test_one_good_round_at_deadline_is_not_stable_convergence(self):
        runner = self.runner()
        runner.matrix = lambda _: [{'pass': True}]
        with patch.object(checker.time, 'monotonic', side_effect=[0, 11]):
            runner.phase('ingress')
        self.assertEqual(runner.phases, [{'name': 'ingress', 'stableRounds': 1, 'pass': False}])

    def test_probe_normalizes_ipv4_mapped_source(self):
        runner = self.runner()
        with patch.object(runner, 'kubectl', return_value='{"reachable":true,"source":"::ffff:10.3.0.8"}'):
            self.assertEqual(runner.probe('windows-allow', '10.3.0.1')['source'], '10.3.0.8')

    def test_fixture_pins_same_node_clients_and_separate_ipv6_service(self):
        runner = checker.Checker(arguments())
        manifests = []
        def create(resource):
            manifests.append(resource)
            if resource['kind'] == 'Namespace':
                return {'metadata': {'uid': 'new-test-namespace'}}
            if resource['kind'] == 'Service':
                return {'spec': {'clusterIP': 'fd98::1' if resource['spec']['ipFamilies'] == ['IPv6'] else '10.96.0.1'}}
            return resource
        runner.create = create
        runner.kubectl = lambda *args: ''
        runner.get = lambda *args: {'status': {'podIPs': [{'ip': '10.3.0.1'}, {'ip': 'fd00::1'}]}}
        runner.setup()
        windows_clients = [m for m in manifests if m['kind'] == 'Pod' and m['metadata']['name'] in ('windows-allow', 'windows-deny')]
        self.assertEqual(len(windows_clients), 2)
        self.assertTrue(all(m['spec']['nodeSelector'] == {'kubernetes.io/hostname': 'windows-2022'} for m in windows_clients))
        services = [m for m in manifests if m['kind'] == 'Service']
        self.assertEqual(len(services), 4)
        self.assertEqual(sum(m['spec']['ipFamilies'] == ['IPv6'] for m in services), 2)


class ProbeRuntimeTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which('pwsh'), 'PowerShell is not installed')
    def test_powershell_probe_distinguishes_response_and_refused_connection(self):
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(json.dumps({'source': self.client_address[0]}).encode())

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        command = ['pwsh', '-NoProfile', '-Command',
                   f"$target = 'http://127.0.0.1:{server.server_port}/'\n" + checker.WINDOWS_CLIENT]
        try:
            response = subprocess.run(command, text=True, capture_output=True, timeout=15, check=True)
            self.assertEqual(json.loads(response.stdout), {'reachable': True, 'source': '127.0.0.1'})
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
        response = subprocess.run(command, text=True, capture_output=True, timeout=15, check=True)
        self.assertFalse(json.loads(response.stdout)['reachable'])


if __name__ == '__main__':
    unittest.main()
