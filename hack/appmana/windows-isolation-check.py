#!/usr/bin/env python3
"""Disposable VM-cluster policy checks. Requires Python 3 locally and in Linux pods.

This complements ipv6-health-check.sh; it does not configure routing, CNI, NAT,
or production resources. Both Windows clients run on the SAME node to expose
host-address exemptions and source identity lost by Service masquerading.
"""

import argparse
import ipaddress
import json
from pathlib import Path
import re
import subprocess
import sys
import time
import uuid


PORT = 8080
WINDOWS_SERVER = r"""
$ErrorActionPreference = 'Stop'
$listener = [System.Net.HttpListener]::new()
$listener.Prefixes.Add('http://+:8080/')
$listener.Start()
while ($true) {
  $ctx = $listener.GetContext()
  $body = @{source=$ctx.Request.RemoteEndPoint.Address.ToString()} | ConvertTo-Json -Compress
  $bytes = [Text.Encoding]::UTF8.GetBytes($body)
  $ctx.Response.ContentType = 'application/json'
  $ctx.Response.OutputStream.Write($bytes, 0, $bytes.Length)
  $ctx.Response.Close()
}
"""
LINUX_SERVER = """
import http.server, json, socket
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({'source': self.client_address[0]}).encode()
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)
class Server(http.server.ThreadingHTTPServer):
    address_family = socket.AF_INET6
    def server_bind(self):
        self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        super().server_bind()
Server(('::', 8080), Handler).serve_forever()
"""
LINUX_CLIENT = """
import json, socket, sys, urllib.error, urllib.request
try:
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(sys.argv[1], timeout=3) as response:
        print(json.dumps({'reachable': True, 'source': json.load(response)['source']}))
except urllib.error.HTTPError:
    raise
except (urllib.error.URLError, TimeoutError, socket.timeout) as error:
    print(json.dumps({'reachable': False, 'error': str(error)}))
"""
WINDOWS_CLIENT = r"""
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http
$handler = [System.Net.Http.HttpClientHandler]::new()
$handler.UseProxy = $false
$client = [System.Net.Http.HttpClient]::new($handler)
$client.Timeout = [TimeSpan]::FromSeconds(3)
try {
  $response = $client.GetAsync($target).GetAwaiter().GetResult()
  $response.EnsureSuccessStatusCode() | Out-Null
  $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult() | ConvertFrom-Json
  @{reachable=$true; source=$body.source} | ConvertTo-Json -Compress
} catch {
  $errorType = $_.Exception.GetBaseException()
  if ($errorType -isnot [System.Net.Sockets.SocketException] -and
      $errorType -isnot [System.Threading.Tasks.TaskCanceledException]) { throw }
  @{reachable=$false; error=$errorType.Message} | ConvertTo-Json -Compress
} finally { $client.Dispose(); $handler.Dispose() }
"""


def normalized_ip(value):
    address = ipaddress.ip_address(value)
    return str(address.ipv4_mapped or address) if address.version == 6 else str(address)


class Checker:
    def __init__(self, args):
        self.args = args
        self.namespace = 'cldt-win-isolation-' + uuid.uuid4().hex[:12]
        self.namespace_uid = None
        self.results = []
        self.phases = []
        self.pods = {}
        self.targets = []

    def kubectl(self, *args, body=None):
        command = ['kubectl', '--kubeconfig', self.args.kubeconfig,
                   '--context', self.args.context, '--request-timeout=30s', *args]
        result = subprocess.run(command, input=json.dumps(body) if body else None,
                                text=True, capture_output=True, timeout=90)
        if result.returncode:
            raise RuntimeError(f'kubectl {args[0]} failed: {result.stderr.strip()}')
        return result.stdout

    def get(self, resource, name, namespace=None):
        args = ['get', resource, name, '-o', 'json']
        if namespace:
            args += ['-n', namespace]
        return json.loads(self.kubectl(*args))

    def guard(self):
        # Explicit context does not change kubeconfig's current-context.
        config = json.loads(self.kubectl('config', 'view', '--minify', '-o', 'json'))
        if config['clusters'][0]['cluster']['server'] != self.args.server:
            raise RuntimeError('explicit context API server does not match --server')
        if self.get('namespace', 'kube-system')['metadata']['uid'] != self.args.cluster_uid:
            raise RuntimeError('cluster identity does not match --cluster-uid')
        for name, expected in [(self.args.windows_node, 'windows'), (self.args.linux_node, 'linux')]:
            node = self.get('node', name)
            labels = node['metadata']['labels']
            if labels.get('kubernetes.io/os') != expected:
                raise RuntimeError(f'{name} is not a {expected} node')
            if expected == 'windows' and labels.get('node.kubernetes.io/windows-build') != self.args.windows_build:
                raise RuntimeError('Windows node build does not match the requested test row')
            if not any(c['type'] == 'Ready' and c['status'] == 'True' for c in node['status']['conditions']):
                raise RuntimeError(f'{name} is not Ready')

    def create(self, resource):
        return json.loads(self.kubectl('create', '-f', '-', '-o', 'json', body=resource))

    def setup(self):
        namespace = self.create({'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {
            'name': self.namespace, 'labels': {'cloud-provisioning.io/isolated-test': 'windows-policy'}}})
        self.namespace_uid = namespace['metadata']['uid']
        for os_name, node, image in [('windows', self.args.windows_node, self.args.windows_image),
                                      ('linux', self.args.linux_node, self.args.linux_image)]:
            for role in ('backend', 'allow', 'deny'):
                name = f'{os_name}-{role}'
                if role == 'backend':
                    command = ['powershell.exe', '-NoProfile', '-Command', WINDOWS_SERVER] if os_name == 'windows' else ['python3', '-u', '-c', LINUX_SERVER]
                else:
                    command = ['powershell.exe', '-NoProfile', '-Command', 'Start-Sleep -Seconds 86400'] if os_name == 'windows' else ['python3', '-c', 'import time; time.sleep(86400)']
                labels = {'role': 'backend' if role == 'backend' else 'client', 'access': role, 'target': name}
                self.create({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {
                    'name': name, 'namespace': self.namespace, 'labels': labels}, 'spec': {
                        'nodeSelector': {'kubernetes.io/hostname': node}, 'restartPolicy': 'Never',
                        'automountServiceAccountToken': False,
                        'tolerations': [{'operator': 'Exists'}],
                        'containers': [{'name': 'test', 'image': image, 'command': command}]}})
                self.pods[name] = {'os': os_name, 'role': role}
        self.kubectl('wait', 'pod', '--all', '-n', self.namespace, '--for=condition=Ready', '--timeout=60s')
        for name, spec in self.pods.items():
            pod = self.get('pod', name, self.namespace)
            spec['ips'] = {ipaddress.ip_address(i['ip']).version: i['ip'] for i in pod['status']['podIPs']}
            if set(spec['ips']) != {4, 6}:
                raise RuntimeError(f'{name} is not dual-stack; refusing a partial result')
            if spec['role'] != 'backend':
                continue
            for family, ip in spec['ips'].items():
                self.targets.append((name, 'pod', family, ip))
                service = self.create({'apiVersion': 'v1', 'kind': 'Service', 'metadata': {
                    'name': f'{name}-v{family}', 'namespace': self.namespace}, 'spec': {
                        'selector': {'target': name}, 'ipFamilyPolicy': 'SingleStack',
                        'ipFamilies': [f'IPv{family}'], 'ports': [{'port': PORT, 'targetPort': PORT}]}})
                self.targets.append((name, 'service', family, service['spec']['clusterIP']))

    def probe(self, client, address):
        url = f'http://[{address}]:{PORT}/' if ':' in address else f'http://{address}:{PORT}/'
        command = ['powershell.exe', '-NoProfile', '-Command', f"$target = '{url}'\n" + WINDOWS_CLIENT] if self.pods[client]['os'] == 'windows' else ['python3', '-c', LINUX_CLIENT, url]
        result = json.loads(self.kubectl('exec', client, '-n', self.namespace, '--', *command))
        if not isinstance(result.get('reachable'), bool):
            raise RuntimeError('client returned no valid reachability observation')
        if result['reachable']:
            result['source'] = normalized_ip(result['source'])
        return result

    def matrix(self, phase):
        observations = []
        for client, spec in self.pods.items():
            if spec['role'] == 'backend':
                continue
            expected = phase == 'baseline' or spec['role'] == 'allow'
            for backend, path, family, address in self.targets:
                observed = self.probe(client, address)
                source = normalized_ip(spec['ips'][family])
                observations.append({'phase': phase, 'client': client, 'backend': backend,
                    'path': path, 'family': family, 'expectedReachable': expected,
                    'expectedSource': source, **observed,
                    'pass': observed['reachable'] == expected and (not expected or observed['source'] == source)})
        return observations

    def policy(self, name, selector, direction, rules):
        self.create({'apiVersion': 'networking.k8s.io/v1', 'kind': 'NetworkPolicy',
            'metadata': {'name': name, 'namespace': self.namespace}, 'spec': {
                'podSelector': {'matchLabels': selector}, 'policyTypes': [direction], direction.lower(): rules}})

    def phase(self, name):
        deadline = time.monotonic() + self.args.convergence_timeout
        # Require repeated observations; a single early deny during a routing
        # outage must not stand in for an enforced, stable policy matrix.
        stable = 0
        while True:
            observations = self.matrix(name)
            stable = stable + 1 if all(row['pass'] for row in observations) else 0
            if stable >= 2 or time.monotonic() >= deadline:
                self.results.extend(observations)
                self.phases.append({'name': name, 'stableRounds': stable, 'pass': stable >= 2})
                return
            time.sleep(1)

    def run(self):
        self.guard()
        try:
            self.setup()
            self.phase('baseline')
            # Baseline connectivity must work, or a policy denial could be a
            # broken network. Source rewrites are still recorded as failures.
            if not all(r['reachable'] for r in self.results):
                raise RuntimeError('baseline connectivity failed; policy results would be inconclusive')
            self.policy('ingress', {'role': 'backend'}, 'Ingress', [{'from': [{'podSelector': {'matchLabels': {'access': 'allow'}}}], 'ports': [{'protocol': 'TCP', 'port': PORT}]}])
            self.phase('ingress')
            self.kubectl('delete', 'networkpolicy', 'ingress', '-n', self.namespace)
            self.policy('egress', {'access': 'deny'}, 'Egress', [])
            self.phase('egress')
            return all(phase['pass'] for phase in self.phases)
        finally:
            if self.namespace_uid:
                current = self.get('namespace', self.namespace)
                if current['metadata']['uid'] != self.namespace_uid:
                    raise RuntimeError('namespace identity changed; refusing cleanup')
                self.kubectl('delete', 'namespace', self.namespace, '--wait=false')


def arguments(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('kubeconfig', 'context', 'server', 'cluster-uid', 'windows-node', 'linux-node', 'windows-image', 'linux-image', 'windows-build'):
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--convergence-timeout', type=int, default=180)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(argv)
    if args.windows_build not in ('10.0.20348', '10.0.26100'):
        parser.error('--windows-build must identify Server 2022 or Server 2025')
    if not Path(args.kubeconfig).is_file():
        parser.error('--kubeconfig must be an explicit existing file')
    if args.convergence_timeout < 1:
        parser.error('--convergence-timeout must be positive')
    for value in (args.windows_image, args.linux_image):
        if not re.fullmatch(r'.+@sha256:[0-9a-f]{64}', value):
            parser.error('test images must be pinned by SHA256 digest')
    return args


def main():
    args = arguments()
    checker = Checker(args)
    error = None
    try:
        passed = checker.run()
    except (RuntimeError, subprocess.TimeoutExpired, ValueError, KeyError, KeyboardInterrupt) as exc:
        passed, error = False, str(exc) or type(exc).__name__
    args.output.write_text(json.dumps({'passed': passed, 'error': error,
        'windowsBuild': args.windows_build, 'namespace': checker.namespace,
        'phases': checker.phases, 'results': checker.results}, indent=2) + '\n')
    print(f'{len(checker.results)} observations; {"PASS" if passed else "FAIL"}; {args.output}')
    return 0 if passed else 1


if __name__ == '__main__':
    sys.exit(main())
