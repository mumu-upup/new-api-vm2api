#!/usr/bin/env python3
"""Restricted local VM2API lifecycle API. No TCP listener or arbitrary Docker API."""
import argparse
import contextlib
import fcntl
import http.server
import ipaddress
import json
import os
import pathlib
import re
import secrets
import shutil
import socket
import socketserver
import subprocess
import threading
import time

ID = re.compile(r'^[a-f0-9]{32}$')
IMAGE = re.compile(r'^(?:[^\s]+@)?sha256:[a-f0-9]{64}$')
OWNER = 'ai.new-api.vm2api.instance'

class ManagerError(Exception):
    pass


def docker(args):
    try:
        result = subprocess.run(['/usr/bin/docker', *args], capture_output=True, timeout=45, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise ManagerError('docker_unavailable') from None
    if result.returncode:
        # Only an explicit not-found result is absence. Daemon/network errors
        # must never lead to deleting registry state or recreating containers.
        if args[0] == 'inspect' and b'no such object:' in result.stderr.lower():
            raise ManagerError('container_missing')
        raise ManagerError('docker_failed')
    if args[0] == 'inspect' or args[:2] == ['image', 'inspect']:
        try:
            return json.loads(result.stdout)
        except ValueError:
            raise ManagerError('docker_invalid_response') from None
    return []


class InstanceManager:
    def __init__(self, config, run=docker):
        self.config = config
        self.run = run
        self.root = pathlib.Path(config['root'])
        if not self.root.is_absolute() or self.root.is_symlink() or str(self.root) == '/':
            raise ManagerError('unsafe_path')
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        if self.root.resolve() != self.root:
            raise ManagerError('unsafe_path')
        if not IMAGE.fullmatch(config['image']):
            raise ManagerError('image_must_be_pinned')
        host = ipaddress.ip_address(config['host'])
        if host.version != 4 or host.is_unspecified or host.is_multicast or not (host.is_private or host.is_loopback):
            raise ManagerError('private_host_required')
        start, end = config['port_start'], config['port_end']
        if not (1024 <= start <= end <= 65535):
            raise ManagerError('invalid_port_range')
        binary = pathlib.Path(config['kin_computer'])
        if not binary.is_file() or binary.is_symlink():
            raise ManagerError('kin_computer_missing')
        self.lock = threading.RLock()

    @contextlib.contextmanager
    def locked(self):
        with self.lock, open(self.root / '.lock', 'a', encoding='utf-8') as handle:
            os.chmod(handle.name, 0o600)
            fcntl.flock(handle, fcntl.LOCK_EX)
            yield

    def directory(self, ident):
        if not ID.fullmatch(ident):
            raise ManagerError('invalid_instance_id')
        path = self.root / ident
        if path.is_symlink() or path.resolve() != path:
            raise ManagerError('unsafe_path')
        # Reject mount substitutions, including a link planted by a compromised
        # guest into an otherwise owned instance directory.
        for name in ('data', 'bin', 'share', 'vms', 'config', 'restricted', 'approved', 'instance.json', 'runtime.env', 'bin/kin-computer', 'approved/kin-computer', 'restricted/docker', 'restricted/images.json'):
            if (path / name).is_symlink():
                raise ManagerError('unsafe_path')
        return path

    def read(self, ident):
        path = self.directory(ident) / 'instance.json'
        try:
            value = json.loads(path.read_text())
        except FileNotFoundError:
            raise ManagerError('instance_missing') from None
        except (ValueError, OSError):
            raise ManagerError('registry_invalid') from None
        if value.get('id') != ident or value.get('container_name') != 'vm2api-account-' + ident:
            raise ManagerError('registry_invalid')
        if not IMAGE.fullmatch(value.get('image', '')) or not self.config['port_start'] <= value.get('port', 0) <= self.config['port_end']:
            raise ManagerError('registry_config_conflict')
        return value

    def write(self, ident, value):
        path = self.directory(ident)
        tmp = path / ('.instance-' + secrets.token_hex(8))
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(fd, 'w') as handle:
                json.dump(value, handle)
                handle.flush()
                os.fsync(handle.fileno())
            os.replace(tmp, path / 'instance.json')
        finally:
            tmp.unlink(missing_ok=True)

    def inspect(self, name):
        try:
            result = self.run(['inspect', name])
        except ManagerError as error:
            if str(error) == 'container_missing':
                return None
            raise
        if not isinstance(result, list) or len(result) != 1:
            raise ManagerError('docker_invalid_response')
        return result[0]

    def owned(self, record, child=False):
        name = 'kin-new-api-' + record['id'] + '-isolated' if child else record['container_name']
        state = self.inspect(name)
        if state is None:
            return None
        root = str(self.directory(record['id']))
        mounts = [entry.get('Source', '') for entry in state.get('Mounts', [])]
        if child:
            expected = root + '/vms/' + 'new-api-' + record['id'] + '-isolated'
            if not any(source == expected or source.startswith(expected + '/') for source in mounts):
                raise ManagerError('ownership_conflict')
        elif state.get('Config', {}).get('Labels', {}).get(OWNER) != record['id'] or root + '/data' not in mounts:
            raise ManagerError('ownership_conflict')
        return state

    def public(self, record):
        state = self.owned(record)
        result = {key: record[key] for key in ('id', 'container_name', 'image', 'port', 'updated_at')}
        url = 'http://' + self.config['host'] + ':' + str(record['port'])
        result.update(management_url=url, relay_url=url, data_directory=str(self.directory(record['id']) / 'data'), error_code='')
        result['status'] = 'missing' if state is None else ('running' if state.get('State', {}).get('Running') else 'stopped')
        return result

    def status(self, ident):
        with self.locked():
            return self.public(self.read(ident))

    def allocate(self, ident):
        pinned_images = self.preflight()
        records = []
        for path in self.root.iterdir():
            if not ID.fullmatch(path.name):
                continue
            try:
                records.append(self.read(path.name))
            except ManagerError as error:
                if str(error) != 'instance_missing' or self.inspect('vm2api-account-' + path.name) is not None:
                    raise

        used = {item['port'] for item in records}
        port = None
        for candidate in range(self.config['port_start'], self.config['port_end'] + 1):
            if candidate in used:
                continue
            with socket.socket() as check:
                try:
                    check.bind((self.config['host'], candidate))
                except OSError:
                    continue
            port = candidate
            break
        if port is None:
            raise ManagerError('ports_exhausted')
        directory = self.directory(ident)
        directory.mkdir(mode=0o700, exist_ok=True)
        record = {'slot_images': pinned_images, 'id': ident, 'container_name': 'vm2api-account-' + ident, 'image': self.config['image'], 'port': port, 'updated_at': int(time.time()), 'api_key': secrets.token_hex(32), 'db_secret': secrets.token_hex(32), 'admin_password': secrets.token_hex(32)}
        self.write(ident, record)
        return record

    def create(self, record):
        for image in [record['image'], *record['slot_images'].values()]:
            self.run(['image', 'inspect', image])
        root = self.directory(record['id'])
        for subdir in ('data', 'bin', 'share', 'vms', 'config', 'restricted', 'approved'):
            (root / subdir).mkdir(mode=0o700, exist_ok=True)
        target = root / 'approved' / 'kin-computer'
        if not target.exists():
            shutil.copyfile(self.config['kin_computer'], target)
            target.chmod(0o755)
        # Upstream may invoke docker build as an image-pull fallback. Refuse it
        # at runtime; production must only use prebuilt images.
        wrapper = root / 'restricted' / 'docker'
        if wrapper.is_symlink():
            raise ManagerError('unsafe_path')
        shutil.copyfile(pathlib.Path(__file__).with_name('vm2api-docker-guard.py'), wrapper)
        wrapper.chmod(0o755)
        (root / 'restricted' / 'images.json').write_text(json.dumps(record['slot_images']))
        env = {
            'VM2API_API_KEY': record['api_key'], 'VM2API_DB_SECRET': record['db_secret'],
            'VM2API_ADMIN_USER': 'admin', 'VM2API_ADMIN_PASSWORD': record['admin_password'],
            'VM2API_CONTAINER_NAME': record['container_name'], 'VM2API_HOST_ROOT': str(root),
            'HOST': self.config['host'], 'PORT': str(record['port']),
            'KIN_COMPUTER_BIN': '/opt/vm2api/approved/kin-computer', 'KIN_AUTO_SYNC_WRAP': '0', 'PATH': '/restricted:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
        }
        env_file = root / 'runtime.env'
        fd = os.open(env_file, os.O_CREAT | os.O_WRONLY | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'w') as handle:
            handle.write(''.join(key + '=' + value + '\n' for key, value in env.items()))
        args = ['run', '--detach', '--name', record['container_name'], '--label', OWNER + '=' + record['id'], '--restart', 'unless-stopped', '--init', '--network', 'host', '--memory', self.config.get('memory', '512m'), '--cpus', str(self.config.get('cpus', 1)), '--pids-limit', '256', '--log-opt', 'max-size=10m', '--log-opt', 'max-file=3', '--cap-add', 'NET_ADMIN', '--cap-add', 'NET_RAW', '--env-file', str(env_file)]
        for source, destination in [('data', '/opt/vm2api/data'), ('vms', '/opt/vm2api/vms'), ('bin', '/opt/vm2api/bin'), ('share', '/opt/vm2api/share'), ('config', '/opt/vm2api/src/config'), ('restricted', '/restricted:ro'), ('approved', '/opt/vm2api/approved:ro')]:
            args += ['--volume', str(root / source) + ':' + destination]
        args += ['--volume', '/var/run/docker.sock:/var/run/docker.sock', record['image']]
        self.run(args)

    def preflight(self):
        result = {}
        for image in [self.config['image'], *self.config['slot_images']]:
            images = self.run(['image', 'inspect', image])
            if not images or images[0].get('Architecture') != 'amd64' or images[0].get('Os') != 'linux' or not IMAGE.fullmatch(images[0].get('Id', '')):
                raise ManagerError('image_platform_mismatch')
            if image != self.config['image']:
                result[image] = images[0]['Id']
        return result

    def ensure_locked(self, ident):
        self.directory(ident)
        try:
            record = self.read(ident)
        except ManagerError as error:
            if str(error) != 'instance_missing':
                raise
            if self.inspect('vm2api-account-' + ident) is not None:
                raise ManagerError('ownership_conflict')
            record = self.allocate(ident)
        state = self.owned(record)
        if state is None:
            self.create(record)
        # Ensure deliberately does not unpause an operator-stopped instance.
        result = self.public(record)
        result.update(management_key=record['api_key'], relay_key=record['api_key'])
        return result

    def ensure(self, ident):
        with self.locked():
            return self.ensure_locked(ident)

    def action(self, ident, action):
        if action not in ('start', 'stop', 'restart', 'rebuild'):
            raise ManagerError('invalid_action')
        with self.locked():
            record = self.read(ident)
            state, child = self.owned(record), self.owned(record, child=True)
            child_name = 'kin-new-api-' + ident + '-isolated'
            if action in ('stop', 'restart', 'rebuild') and state is not None:
                self.run(['stop', record['container_name']])
                child = self.owned(record, child=True)
            if action in ('stop', 'restart', 'rebuild') and child is not None:
                self.run(['stop', child_name])
            if action == 'rebuild':
                record['slot_images'] = self.preflight()
                record['image'] = self.config['image']
                self.write(ident, record)
                if state is not None:
                    self.run(['rm', '-f', record['container_name']])
                self.create(record)
            elif state is None:
                if action == 'stop':
                    raise ManagerError('instance_missing')
                self.create(record)
            else:
                self.run([action, record['container_name']])
            if action in ('start', 'restart', 'rebuild') and child is not None:
                self.run(['start', child_name])
            record['updated_at'] = int(time.time())
            self.write(ident, record)
            return self.public(record)

    def delete(self, ident):
        with self.locked():
            try:
                record = self.read(ident)
            except ManagerError as error:
                if str(error) == 'instance_missing':
                    return {'id': ident, 'status': 'deleted'}
                raise
            # Validate both BEFORE touching either. Never remove an unrelated
            # container based only on its name or a stale database row.
            state, child = self.owned(record), self.owned(record, child=True)
            if state is not None:
                self.run(['stop', record['container_name']])
            # With the controller stopped it cannot race deletion by starting
            # another slot. Recheck after stopping to catch an in-flight create.
            child = self.owned(record, child=True)
            if child is not None:
                self.run(['rm', '-f', 'kin-new-api-' + ident + '-isolated'])
            if state is not None:
                self.run(['rm', '-f', record['container_name']])
            if self.owned(record) is not None or self.owned(record, child=True) is not None:
                raise ManagerError('cleanup_incomplete')
            shutil.rmtree(self.directory(ident))
            return {'id': ident, 'status': 'deleted'}


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def dispatch(self):
        self.connection.settimeout(10)
        try:
            length = int(self.headers.get('Content-Length', '0'))
            if length < 0 or length > 1024 or self.headers.get('Transfer-Encoding'):
                raise ManagerError('invalid_body')
            if length and json.loads(self.rfile.read(length)) != {}:
                raise ManagerError('invalid_body')
            match = re.fullmatch(r'/v1/instances/([a-f0-9]{32})(?:/(start|stop|restart|rebuild))?', self.path)
            if not match:
                raise ManagerError('invalid_route')
            ident, action = match.groups()
            manager = self.server.manager
            if self.command == 'POST' and action:
                result = manager.action(ident, action)
            elif action:
                raise ManagerError('invalid_route')
            elif self.command == 'PUT':
                result = manager.ensure(ident)
            elif self.command == 'GET':
                result = manager.status(ident)
            elif self.command == 'DELETE':
                result = manager.delete(ident)
            else:
                raise ManagerError('invalid_route')
            self.reply(200, result)
        except ManagerError as error:
            code = str(error)
            status = 404 if code == 'instance_missing' else 409
            if code.startswith('invalid_'):
                status = 400
            self.reply(status, {'error_code': code})
        except (ValueError, OSError, KeyError, TypeError):
            self.reply(500, {'error_code': 'manager_failed'})

    def reply(self, status, result):
        payload = json.dumps(result).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write(payload)

    do_GET = do_PUT = do_POST = do_DELETE = dispatch


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True
    request_queue_size = 16


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', required=True)
    parser.add_argument('--socket', default='/run/new-api-vm2api/manager.sock')
    args = parser.parse_args()
    os.umask(0o077)
    config = json.loads(pathlib.Path(args.config).read_text())
    instance_manager = InstanceManager(config)
    address = pathlib.Path(args.socket)
    address.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    # Lock the daemon lifetime before replacing an old socket.
    with open(address.parent / 'daemon.lock', 'w') as daemon_lock:
        fcntl.flock(daemon_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        address.unlink(missing_ok=True)
        with Server(str(address), Handler) as server:
            server.manager = instance_manager
            address.chmod(0o600)
            server.serve_forever()


if __name__ == '__main__':
    main()
