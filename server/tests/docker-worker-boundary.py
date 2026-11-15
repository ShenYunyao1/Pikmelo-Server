"""Verify the actual Compose worker isolation using a disposable project."""

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import uuid


SOURCE = Path(__file__).resolve().parents[1]


def verify(args):
    docker = args.docker or shutil.which('docker')
    if not docker:
        raise RuntimeError('Docker CLI is required')
    project = 'pikmelo-boundary-' + uuid.uuid4().hex[:12]
    environment = dict(os.environ, PIKMELO_WORKER_IMAGE=args.worker_image,
                       PIKMELO_DATA_VOLUME=project + '-data')

    def command(*arguments, check=True, timeout=90):
        result = subprocess.run([docker, *map(str, arguments)],
                                capture_output=True, text=True, encoding='utf-8',
                                errors='replace', env=environment, timeout=timeout)
        if check and result.returncode:
            raise AssertionError('Docker verification command failed: ' + result.stderr[-4000:])
        return result

    def compose(*arguments, **kwargs):
        return command('compose', '--project-name', project,
                       '--project-directory', SOURCE, '-f', SOURCE / 'compose.yaml',
                       *arguments, **kwargs)

    try:
        compose('up', '--no-build', '--detach', '--wait', '--wait-timeout', '60', 'online-worker')
        container = compose('ps', '--quiet', 'online-worker').stdout.strip()
        assert container and '\n' not in container, 'Expected exactly one worker container'
        identity = json.loads(command('inspect', container).stdout)[0]
        configuration = identity['Config']
        host = identity['HostConfig']
        assert configuration['User'].split(':')[0] not in ['', '0', 'root'], 'Worker runs as root'
        assert host['ReadonlyRootfs'] is True, 'Worker root filesystem is writable'
        assert host['Memory'] == 256 * 1024 * 1024, 'Worker memory limit differs from 256 MiB'
        assert host['PidsLimit'] == 64, 'Worker process limit is missing'
        assert host['NanoCpus'] == 1000000000, 'Worker CPU limit differs from one core'
        assert 'ALL' in host['CapDrop'], 'Worker must drop all capabilities'
        assert 'no-new-privileges:true' in host['SecurityOpt'], 'Worker privilege escalation is allowed'
        assert not any(identity['NetworkSettings']['Ports'].values()), 'Worker publishes a host port'
        for mount in identity['Mounts']:
            assert mount['Type'] == 'tmpfs' and mount['Destination'] == '/tmp', 'Worker has an unexpected data mount'
        networks = identity['NetworkSettings']['Networks']
        assert len(networks) == 1, 'Worker has access to more than one network'
        network_name = next(iter(networks))
        assert network_name.startswith(project + '_'), 'Worker network belongs to another project'
        network = json.loads(command('network', 'inspect', network_name).stdout)[0]
        assert network['Internal'] is True, 'Worker network permits external routing'

        script = r"""
const assert = require('node:assert/strict');
const fs = require('node:fs');
(async () => {
  assert.notEqual(process.getuid(), 0, 'Worker process runs as root');
  await assert.rejects(
    fetch('http://1.1.1.1/', {signal: AbortSignal.timeout(1500)}),
    'Worker unexpectedly reached the public network',
  );
  assert.throws(() => fs.writeFileSync('/runner/probe', 'boundary'), {code:'EROFS'});
  assert.equal(fs.existsSync('/data/data/library.sqlite'), false, 'Worker can see the business database');
  assert.match(fs.readFileSync('/usr/share/doc/pikmelo-online-worker/NOTICE','utf8'), /LX Music Desktop/);
  assert.match(fs.readFileSync('/usr/share/doc/pikmelo-online-worker/LICENSE-LX-Music.txt','utf8'), /Apache License/);
  const health = await fetch('http://127.0.0.1:3000/health', {signal:AbortSignal.timeout(2000)});
  assert.equal(health.status, 200);
  const response = await fetch('http://127.0.0.1:3000/execute', {
    method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({action:'init',script:`
      lx.send(lx.EVENT_NAMES.inited, {sources:{
        kw:{type:'music',actions:['musicUrl','search'],qualitys:['128k','192k']},
        qs:{type:'music',actions:['musicUrl'],qualitys:['128k']},
      }});
    `}),
    signal:AbortSignal.timeout(5000),
  });
  assert.equal(response.status, 200);
  const result = await response.json();
  assert.deepEqual(result.sources.kw.qualitys, ['128k']);
  assert.deepEqual(result.sources.kw.actions, ['musicUrl']);
  assert.deepEqual(Object.keys(result.sources), ['kw']);
  console.log('PASS live non-root process, blocked external HTTP, read-only root, absent business database, health and LX initialization');
})().catch(error => {console.error(error);process.exitCode=1;});
"""
        result = command('exec', container, 'node', '-e', script, timeout=20)
        print(result.stdout.strip())
        print('PASS Compose worker mount, network, resource, capability and published-port boundaries')
    finally:
        # Only the random project created above is eligible for removal.
        cleanup = compose('down', '--remove-orphans', check=False, timeout=60)
        if cleanup.returncode:
            raise AssertionError('Disposable worker project cleanup failed: ' + cleanup.stderr[-4000:])


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--worker-image', required=True)
    parser.add_argument('--docker')
    verify(parser.parse_args())
