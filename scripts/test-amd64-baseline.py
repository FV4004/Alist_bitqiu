"""Execute the actual static release binary on pre-AVX CPU models.

GOAMD64=v1 does not constrain linked C libraries. In particular an amd64v3
musl archive can SIGILL before Go starts. Never replace this with an opcode
scan: runtime-dispatched optional SIMD instructions are valid in Go binaries.
Uses temporary data and loopback HTTP only; never prints startup credentials.
"""
import argparse
import http.client
import json
import os
from pathlib import Path
import resource
import shutil
import socket
import sqlite3
import subprocess
import tempfile
import time


def no_core_dump():
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))


def fetch(port, path):
    conn = http.client.HTTPConnection('127.0.0.1', port, timeout=2)
    try:
        conn.request('GET', path)
        response = conn.getresponse()
        return response.status, response.read()
    finally:
        conn.close()


def boot(qemu, cpu, binary, data, port):
    env = os.environ.copy()
    env['GOMAXPROCS'] = '2'
    with (data / 'startup-test.log').open('wb') as output:
        proc = subprocess.Popen(
            [qemu, '-cpu', cpu, str(binary), 'server', '--data', str(data)],
            stdout=output, stderr=subprocess.STDOUT, env=env,
            preexec_fn=no_core_dump,
        )
        try:
            deadline = time.monotonic() + 90
            while time.monotonic() < deadline:
                if proc.poll() is not None:
                    raise RuntimeError(f'{cpu}: server exited {proc.returncode}; possible CPU/toolchain incompatibility')
                try:
                    status, body = fetch(port, '/ping')
                    if status == 200 and body == b'pong':
                        break
                except (OSError, http.client.HTTPException):
                    pass
                time.sleep(.25)
            else:
                raise RuntimeError(f'{cpu}: server health timeout')
            status, page = fetch(port, '/@nas')
            assert status == 200 and b'inventory.js' in page, f'{cpu}: NAS page failed'
            with sqlite3.connect(data / 'data.db') as db:
                users = db.execute('select count(*) from x_users').fetchone()[0]
                assert users >= 2, f'{cpu}: SQLite initialization failed'
            assert proc.poll() is None, f'{cpu}: server stopped after initialization'
        finally:
            if proc.poll() is None:
                proc.terminate()
            try:
                proc.wait(timeout=20)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    parser.add_argument('--qemu', default=shutil.which('qemu-x86_64') or shutil.which('qemu-x86_64-static'))
    args = parser.parse_args()
    if not args.qemu:
        parser.error('install qemu-user: the CPU compatibility gate must not be skipped')
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error('release binary does not exist')
    for cpu in ['qemu64', 'Nehalem']:
        result = subprocess.run(
            [args.qemu, '-cpu', cpu, str(binary), 'version'],
            capture_output=True, timeout=60, preexec_fn=no_core_dump,
        )
        if result.returncode != 0:
            raise RuntimeError(f'{cpu}: version exited {result.returncode}; SIGILL (-4 / container exit 132) indicates an unsupported CPU baseline')
        with tempfile.TemporaryDirectory(prefix='alist-cpu-baseline-') as tmp:
            data = Path(tmp)
            with socket.socket() as listener:
                listener.bind(('127.0.0.1', 0))
                port = listener.getsockname()[1]
            (data / 'config.json').write_text(json.dumps({
                'force': True,
                'scheme': {'address': '127.0.0.1', 'http_port': port, 'https_port': -1},
                'database': {'type': 'sqlite3', 'db_file': str(data / 'data.db')},
                'temp_dir': str(data / 'temp'),
                'log': {'enable': False},
            }))
            boot(args.qemu, cpu, binary, data, port)
            boot(args.qemu, cpu, binary, data, port)
        print(f'PASS {cpu}: version, SQLite initialization, NAS HTTP page, restart with existing data', flush=True)


if __name__ == '__main__':
    main()
