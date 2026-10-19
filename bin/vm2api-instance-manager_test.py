import importlib.util
import json
import pathlib
import tempfile
import unittest
import http.client
import socket
import threading
import subprocess
from concurrent.futures import ThreadPoolExecutor

spec = importlib.util.spec_from_file_location('manager', pathlib.Path(__file__).with_name('vm2api-instance-manager.py'))
manager = importlib.util.module_from_spec(spec)
spec.loader.exec_module(manager)

class Docker:
    def __init__(self):
        self.containers = {}
        self.calls = []
        self.fail_remove = False
    def __call__(self, args):
        self.calls.append(args)
        if args[:2] == ['image', 'inspect']:
            return [{'Architecture': 'amd64', 'Os': 'linux', 'Id': 'sha256:' + 'a'*64}]
        if args[0] == 'inspect':
            if args[-1] not in self.containers:
                raise manager.ManagerError('container_missing')
            return [self.containers[args[-1]]]
        if args[0] == 'run':
            name = args[args.index('--name') + 1]
            labels = dict(args[i+1].split('=', 1) for i, x in enumerate(args) if x == '--label')
            mounts = [{'Source': args[i+1].split(':')[0], 'Destination': args[i+1].split(':')[1]} for i,x in enumerate(args) if x == '--volume']
            self.containers[name] = {'Config': {'Labels': labels}, 'State': {'Running': True, 'Status': 'running'}, 'Mounts': mounts}
        if args[0] in ('stop', 'start', 'restart'):
            self.containers[args[-1]]['State'] = {'Running': args[0] != 'stop', 'Status': 'running' if args[0] != 'stop' else 'exited'}
        if args[0] == 'rm':
            if self.fail_remove: raise manager.ManagerError('docker_failed')
            self.containers.pop(args[-1], None)
        return []

class ManagerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name).resolve()
        binary = self.root / 'kin-computer'
        binary.write_bytes(b'approved-binary')
        self.config = {'root': str(self.root / 'instances'), 'image': 'sha256:' + 'a'*64, 'host': '127.0.0.1', 'port_start': 19300, 'port_end': 19309, 'capacity': 2, 'kin_computer': str(binary), 'slot_images': ['slot:test']}
        self.docker = Docker()
        self.manager = manager.InstanceManager(self.config, self.docker)
    def tearDown(self): self.temp.cleanup()
    def test_accounts_have_independent_roots_ports_and_secrets_and_retry_reuses_them(self):
        a = self.manager.ensure('a'*32)
        b = self.manager.ensure('b'*32)
        self.assertNotEqual(a['management_key'], b['management_key'])
        self.assertNotEqual(a['port'], b['port'])
        self.assertNotEqual(a['data_directory'], b['data_directory'])
        restored = manager.InstanceManager(self.config, self.docker).ensure('a'*32)
        self.assertEqual(a['management_key'], restored['management_key'])
        self.assertEqual(a['port'], restored['port'])
        self.assertEqual(2, len([c for c in self.docker.calls if c[0] == 'run']))
        self.assertEqual(0o600, (pathlib.Path(a['data_directory']).parent / 'instance.json').stat().st_mode & 0o777)
    def test_status_and_actions_do_not_return_secrets(self):
        created = self.manager.ensure('a'*32)
        public = self.manager.status('a'*32)
        self.assertNotIn('management_key', public)
        self.assertNotIn(created['management_key'], json.dumps(public))
        stopped = self.manager.action('a'*32, 'stop')
        self.assertEqual('stopped', stopped['status'])
        self.assertNotIn('management_key', stopped)
        self.assertEqual('running', self.manager.action('a'*32, 'start')['status'])
    def test_capacity_and_invalid_identity_do_not_create_containers(self):
        for ident in ['../x', 'A'*32, '', 'a'*33]:
            with self.assertRaises(manager.ManagerError): self.manager.ensure(ident)
        self.manager.ensure('a'*32)
        self.manager.ensure('b'*32)
        with self.assertRaisesRegex(manager.ManagerError, 'capacity_exhausted'): self.manager.ensure('c'*32)
        self.assertEqual(2, len(self.docker.containers))
    def test_delete_failure_keeps_data_and_retry_cleans_only_owned_containers(self):
        a = self.manager.ensure('a'*32)
        self.docker.fail_remove = True
        with self.assertRaises(manager.ManagerError): self.manager.delete('a'*32)
        self.assertTrue(pathlib.Path(a['data_directory']).exists())
        self.docker.fail_remove = False
        self.manager.delete('a'*32)
        removal = [c for c in self.docker.calls if c[0] in ('stop', 'rm')]
        self.assertEqual('stop', removal[0][0])
        self.assertFalse(pathlib.Path(a['data_directory']).exists())
        self.manager.delete('a'*32)
    def test_refuses_foreign_container_and_symlink(self):
        a = self.manager.ensure('a'*32)
        self.docker.containers[a['container_name']]['Config']['Labels'] = {}
        with self.assertRaisesRegex(manager.ManagerError, 'ownership_conflict'): self.manager.action('a'*32, 'stop')
        victim = self.root / 'victim'
        victim.mkdir()
        (self.manager.root / ('b'*32)).symlink_to(victim, target_is_directory=True)
        with self.assertRaisesRegex(manager.ManagerError, 'unsafe_path'): self.manager.ensure('b'*32)
        self.assertEqual([], list(victim.iterdir()))
    def test_rebuild_preserves_data_and_keys(self):
        a = self.manager.ensure('a'*32)
        data = pathlib.Path(a['data_directory']) / 'keep'
        data.write_text('keep')
        self.manager.action('a'*32, 'rebuild')
        self.assertEqual('keep', data.read_text())
        self.assertEqual(a['management_key'], self.manager.ensure('a'*32)['management_key'])
    def test_child_cleanup_requires_owned_mount(self):
        a = self.manager.ensure('a'*32)
        child = 'kin-new-api-' + 'a'*32 + '-isolated'
        self.docker.containers[child] = {'Mounts': [{'Source': '/somebody/else'}], 'State': {'Running': True}}
        with self.assertRaisesRegex(manager.ManagerError, 'ownership_conflict'): self.manager.delete('a'*32)
        self.assertIn(child, self.docker.containers)
        self.assertTrue(pathlib.Path(a['data_directory']).exists())

    def test_concurrent_ensure_creates_one_container(self):
        with ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(self.manager.ensure, ['a'*32, 'a'*32]))
        self.assertEqual(results[0]['management_key'], results[1]['management_key'])
        self.assertEqual(1, len([c for c in self.docker.calls if c[0] == 'run']))
    def test_runtime_wrapper_forbids_production_builds(self):
        self.manager.ensure('a'*32)
        wrapper = self.manager.root / ('a'*32) / 'restricted' / 'docker'
        for command in ('build', 'buildx', 'builder', 'compose'):
            result = subprocess.run([str(wrapper), command], capture_output=True)
            self.assertEqual(126, result.returncode)
    def test_rejects_symlinked_kernel_before_container_creation(self):
        directory = self.manager.root / ('a'*32)
        (directory / 'bin').mkdir(parents=True)
        (directory / 'bin' / 'kin-computer').symlink_to('/etc/passwd')
        with self.assertRaisesRegex(manager.ManagerError, 'unsafe_path'):
            self.manager.ensure('a'*32)
        self.assertEqual({}, self.docker.containers)
    def test_unix_http_rejects_extra_payload_and_redacts_status(self):
        address = str(self.root / 'manager.sock')
        server = manager.Server(address, manager.Handler)
        server.manager = self.manager
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        def request(method, path, body='{}'):
            client = http.client.HTTPConnection('localhost', timeout=5)
            client.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            client.sock.connect(address)
            client.request(method, path, body, {'Content-Type':'application/json'})
            response = client.getresponse()
            result = (response.status, json.loads(response.read()))
            client.close()
            return result
        try:
            path = '/v1/instances/' + 'a'*32
            code, _ = request('PUT', path, '{"image":"untrusted"}')
            self.assertEqual(400, code)
            self.assertEqual({}, self.docker.containers)
            code, created = request('PUT', path)
            self.assertEqual(200, code)
            code, status = request('GET', path, '')
            self.assertEqual(200, code)
            self.assertNotIn(created['management_key'], json.dumps(status))
            code, _ = request('POST', path + '/shell')
            self.assertEqual(400, code)
        finally:
            server.shutdown()
            server.server_close()
            worker.join()

    def test_config_image_upgrade_keeps_status_and_rebuild_updates_image(self):
        first = self.manager.ensure('a'*32)
        changed = dict(self.config, image='sha256:' + 'b'*64)
        upgraded = manager.InstanceManager(changed, self.docker)
        self.assertEqual(first['image'], upgraded.status('a'*32)['image'])
        self.assertEqual(changed['image'], upgraded.action('a'*32, 'rebuild')['image'])
        self.assertEqual(first['management_key'], upgraded.ensure('a'*32)['management_key'])

    def test_docker_guard_pins_images_and_caps_guest_resources(self):
        guard_spec = importlib.util.spec_from_file_location('guard', pathlib.Path(__file__).with_name('vm2api-docker-guard.py'))
        guard = importlib.util.module_from_spec(guard_spec)
        guard_spec.loader.exec_module(guard)
        pinned = {'slot:test': 'sha256:' + 'b'*64}
        args = guard.guarded_arguments(['run', '--memory', '8g', '--cpus=0', 'slot:test'], pinned)
        self.assertIn('--memory=512m', args)
        self.assertIn('--cpus=1', args)
        self.assertNotIn('8g', args)
        self.assertNotIn('--cpus=0', args)
        self.assertEqual('sha256:' + 'b'*64, args[-1])
        self.assertEqual(['image', 'inspect', 'sha256:'+'b'*64], guard.guarded_arguments(['pull', 'slot:test'], pinned))
        with self.assertRaises(ValueError): guard.guarded_arguments(['run', 'unapproved:latest'], pinned)
        with self.assertRaises(ValueError): guard.guarded_arguments(['image', 'build', '.'], pinned)

    def test_missing_image_does_not_poison_later_provisioning(self):
        actual = self.manager.run
        def unavailable(args):
            if args[:2] == ['image', 'inspect']: raise manager.ManagerError('docker_failed')
            return actual(args)
        self.manager.run = unavailable
        with self.assertRaises(manager.ManagerError): self.manager.ensure('a'*32)
        self.manager.run = actual
        self.assertEqual('running', self.manager.ensure('a'*32)['status'])
        self.assertEqual('running', self.manager.ensure('b'*32)['status'])

if __name__ == '__main__': unittest.main()
