#!/usr/bin/env python3
"""MOD-1 research: external NUT monitoring with no host-actuation privileges.

Uses cached images only, an internal Docker network, synthetic credentials, and
owned container IDs. Does not mount a Docker socket, kubeconfig, or host PID space.
This is not a supported agents-only installer or a LocalNUT implementation.
"""
import json
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def run(*args, check=True):
    return subprocess.run(args, check=check, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, timeout=30)


def main():
    images = {}
    for name in ('nut-server', 'upsmon-agent', 'node-actuator', 'nut-operator'):
        info = json.loads(run('docker', 'image', 'inspect',
                             f'example.com/{name}:v0.0.1').stdout)[0]
        images[name] = info['Id']
        print(json.dumps({'image': name, 'id': info['Id'], 'bytes': info['Size']}), flush=True)
    prefix = 'mod1-' + uuid.uuid4().hex[:12]
    owned = []
    network = None
    with tempfile.TemporaryDirectory(prefix='nut-mod1-') as directory:
        root = Path(directory)
        root.chmod(0o755)

        def write(name, contents):
            path = root / name
            path.write_text(contents)
            path.chmod(0o644)  # Only disposable, synthetic fixture data.
            return str(path)

        def start(name, image, options, args=()):
            result = run('docker', 'run', '-d', '--pull=never', '--name', prefix + '-' + name,
                         '--network', network, '--cap-drop=ALL', '--read-only',
                         '--security-opt=no-new-privileges', '--user', '65532:65532',
                         '--tmpfs', '/run/nut:uid=65532,gid=65532,mode=0700',
                         '--tmpfs', '/run/power-agent:uid=65532,gid=65532,mode=0700',
                         *options, images[image], *args)
            cid = result.stdout.strip()
            owned.append(cid)
            return cid

        try:
            network = run('docker', 'network', 'create', '--internal', prefix).stdout.strip()
            server_files = {
                'ups.conf': '[critical]\n driver = dummy-ups\n port = critical.dev\n'
                            '[healthy]\n driver = dummy-ups\n port = healthy.dev\n',
                'upsd.conf': 'LISTEN 0.0.0.0 3493\n',
                'upsd.users': '[observer]\n password = synthetic-mod1\n upsmon secondary\n',
                'critical.dev': 'ups.status: OB LB\nbattery.charge: 5\n',
                'healthy.dev': 'ups.status: OL\nbattery.charge: 100\n',
            }
            mounts = ['--network-alias', 'external-nut']
            for name, contents in server_files.items():
                mounts += ['-v', write(name, contents) + ':/etc/nut/' + name + ':ro']
            server = start('server', 'nut-server', mounts)
            run('docker', 'exec', '-d', server, 'upsdrvctl', '-FF', 'start')
            time.sleep(5)
            common = ('MINSUPPLIES 1\nSHUTDOWNCMD "/usr/local/bin/power-signal-writer"\n'
                      'POLLFREQ 1\nPOLLFREQALERT 1\nDEADTIME 3\nHOSTSYNC 0\nFINALDELAY 0\n'
                      'MONITOR critical@external-nut 1 observer synthetic-mod1 secondary\n')
            agents = {}
            for name, extra in [('single', ''), ('dual', 'MONITOR healthy@external-nut 1 observer synthetic-mod1 secondary\n')]:
                options = ['-v', write(name + '.conf', common + extra) + ':/etc/nut/upsmon.conf:ro']
                for key, value in {'POWER_NODE_NAME': name, 'POWER_AGENT_CONFIG_HASH': 'research',
                                   'POWER_AGENT_MODE': 'DryRun', 'POWER_SIGNAL_HOLD_AFTER_WRITE': 'true'}.items():
                    options += ['-e', key + '=' + value]
                agents[name] = start(name, 'upsmon-agent', options, ('-F',))
            channel = root / 'channel'
            channel.mkdir(mode=0o755)
            (channel / 'delivery-channel').write_text('research-only')
            actuator = start('actuator', 'node-actuator', [
                '-v', str(channel) + ':/signals:ro', '-e', 'POWER_AGENT_MODE=DryRun',
                '-e', 'POWER_ACTUATOR_POLICY=Simulate', '-e', 'POWER_NODE_NAME=single',
                '-e', 'POWER_ACTUATOR_STATE_PATH=/run/power-agent/state.json',
                '-e', 'POWER_SIGNAL_PATHS=/signals/single.json'])
            time.sleep(10)
            results = {}
            for name, cid in agents.items():
                signal = run('docker', 'exec', cid, 'test', '-s',
                             '/run/power-agent/shutdown.json', check=False)
                results[name + '_local_signal'] = signal.returncode == 0
                if signal.returncode not in (0, 1):
                    raise RuntimeError('Signal check failed: ' + signal.stderr)
            for ups, expected in [('critical', 'OB LB'), ('healthy', 'OL')]:
                observed = run('docker', 'exec', agents['dual'], 'upsc',
                               ups + '@external-nut', 'ups.status').stdout.strip()
                assert observed == expected, (ups, observed)
            dual_logs = run('docker', 'logs', agents['dual'])
            assert 'Login on UPS' not in dual_logs.stdout + dual_logs.stderr
            run('docker', 'exec', actuator, '/node-actuator', '--ready')
            log_result = run('docker', 'logs', actuator)
            logs = log_result.stdout + log_result.stderr
            results['operator_channel_ignored_local_signal'] = 'SignalAccepted' not in logs
            for cid in (*agents.values(), actuator):
                assert json.loads(run('docker', 'inspect', cid).stdout)[0]['State']['Running']
            assert results == {'single_local_signal': True, 'dual_local_signal': False,
                               'operator_channel_ignored_local_signal': True}, results
            print(json.dumps(results), flush=True)
            print(run('docker', 'stats', '--no-stream', '--format', '{{.Name}} {{.MemUsage}} {{.CPUPerc}}',
                      *agents.values(), actuator).stdout, flush=True)
            help_text = run('docker', 'run', '--rm', '--pull=never', '--network=none',
                            '--cap-drop=ALL', images['nut-operator'], '--help', check=False)
            print(json.dumps({'manager_selective_controller_flag':
                              '--controllers' in help_text.stdout + help_text.stderr}), flush=True)
        finally:
            for cid in reversed(owned):
                run('docker', 'rm', '-f', '-v', cid)
            if network:
                run('docker', 'network', 'rm', network)
            print('Owned containers and network removed', flush=True)


if __name__ == '__main__':
    main()
