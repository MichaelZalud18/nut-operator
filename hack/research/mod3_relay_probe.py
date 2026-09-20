#!/usr/bin/env python3
"""Safe MOD-3 protocol probe. Cached images, internal network, no actuators."""
import json
import argparse
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def run(*args, check=True):
    return subprocess.run(args, check=check, text=True, capture_output=True, timeout=30)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--legacy-no-authconf', action='store_true',
                        help='Diagnostic only: omit the renderer authconf option for older NUT')
    args = parser.parse_args()
    info = json.loads(run('docker', 'image', 'inspect', 'example.com/nut-server:v0.0.1').stdout)[0]
    print(json.dumps({'image': info['Id'], 'bytes': info['Size']}), flush=True)
    prefix = 'mod3-' + uuid.uuid4().hex[:12]
    owned = []
    network = None
    with tempfile.TemporaryDirectory(prefix='nut-mod3-') as directory:
        root = Path(directory)
        root.chmod(0o755)

        def start(name, config, fixture=None):
            files = {'ups.conf': config, 'upsd.conf': 'LISTEN 0.0.0.0 3493\n',
                     'upsd.users': '[observer]\n password = synthetic-mod3\n upsmon secondary\n'}
            if fixture:
                files['fixture.dev'] = fixture
            mounts = []
            for filename, content in files.items():
                path = root / (name + '-' + filename)
                path.write_text(content)
                path.chmod(0o644)
                mounts += ['-v', str(path) + ':/etc/nut/' + filename + ':ro']
            cid = run('docker', 'run', '-d', '--pull=never', '--name', prefix + '-' + name,
                      '--network', network, '--network-alias', name, '--cap-drop=ALL',
                      '--read-only', '--security-opt=no-new-privileges', '--user', '65532:65532',
                      '--tmpfs', '/run/nut:uid=65532,gid=65532,mode=0700', *mounts, info['Id']).stdout.strip()
            owned.append(cid)
            run('docker', 'exec', '-d', cid, 'sh', '-c', 'upsdrvctl -FF start >/run/nut/research-driver.log 2>&1')
            return cid

        try:
            network = run('docker', 'network', 'create', '--internal', prefix).stdout.strip()
            upstream = start('upstream', '[ups]\n driver = dummy-ups\n port = fixture.dev\n',
                             'ups.status: OL\nbattery.charge: 97\nbattery.runtime: 600\n')
            time.sleep(5)
            config = '[relayed]\n driver = dummy-ups\n mode = repeater\n port = ups@upstream:3493\n'
            if not args.legacy_no_authconf:
                config += ' authconf = none\n'
            relay = start('relay', config)
            time.sleep(5)
            for variable, expected in [('ups.status', 'OL'), ('battery.charge', '97'), ('battery.runtime', '600')]:
                value = run('docker', 'exec', upstream, 'upsc', 'relayed@relay', variable).stdout.strip()
                if value != expected:
                    raise RuntimeError(f'{variable}: expected {expected}, got {value}')
                print(json.dumps({'variable': variable, 'value': value}), flush=True)
            # A source update must cross the relay, not just appear at startup.
            (root / 'upstream-fixture.dev').write_text('ups.status: OB LB\nbattery.charge: 5\nbattery.runtime: 30\n')
            time.sleep(5)
            value = run('docker', 'exec', upstream, 'upsc', 'relayed@relay', 'ups.status').stdout.strip()
            source = run('docker', 'exec', upstream, 'upsc', 'ups@localhost', 'ups.status').stdout.strip()
            if value != source or not {'OB', 'LB'}.issubset(value.split()) or 'OL' in value.split():
                raise RuntimeError(f'update did not propagate: {value}')
            print(json.dumps({'updated_status': value, 'manager': False, 'agents': False, 'database': False}), flush=True)
        except Exception:
            for cid in owned:
                log = run('docker', 'exec', cid, 'cat', '/run/nut/research-driver.log', check=False)
                print(log.stdout + log.stderr, flush=True)
            raise
        finally:
            failures = []
            for cid in reversed(owned):
                result = run('docker', 'rm', '-f', cid, check=False)
                if result.returncode:
                    failures.append(result.stderr)
            if network:
                result = run('docker', 'network', 'rm', network, check=False)
                if result.returncode:
                    failures.append(result.stderr)
            if failures:
                raise RuntimeError('Owned resource cleanup failed: ' + '; '.join(failures))
            print('Owned resources removed', flush=True)


if __name__ == '__main__':
    main()
