"""Opt-in verification of a supplied LX source in disposable Docker services."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import secrets
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('container_fixture', ROOT / 'tests/docker-smoke.py')
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


def call(client, path, method='GET', body=None):
    request = urllib.request.Request(client.base + '/api/v2/' + path,
        None if body is None else json.dumps(body).encode(),
        {'Origin': client.base, 'X-CSRF-Token': client.csrf, 'Content-Type': 'application/json'}, method=method)
    with client.http.open(request, timeout=40) as response:
        content = response.read()
        return json.loads(content) if content else None


def verify(args):
    fixture.DOCKER = args.docker
    project = 'pikmelo-source-' + uuid.uuid4().hex[:12]
    volume = project + '-data'
    environment = dict(os.environ, PIKMELO_IMAGE=args.image, PIKMELO_WORKER_IMAGE=args.worker_image,
                       PIKMELO_DATA_VOLUME=volume, PIKMELO_PORT='0', PIKMELO_BIND_ADDRESS='127.0.0.1',
                       PIKMELO_PUBLIC_URL='', PIKMELO_DATABASE='/data/data/library.sqlite', PIKMELO_MEDIA_ROOT='/data/media')
    report = {'source_imports': [], 'platforms': [], 'jobs': []}

    def compose(*commands, check=True):
        return fixture.docker('compose', '-p', project, '-f', ROOT / 'compose.yaml',
                              *commands, check=check, env=environment)

    try:
        compose('up', '-d', '--no-build', '--wait', '--wait-timeout', '90')
        port = compose('port', 'pikmelo', '8080').stdout.strip().rsplit(':', 1)[1]
        base = 'http://127.0.0.1:' + port
        fixture.wait_health(base, 9)
        token = compose('exec', '-T', 'pikmelo', 'cat', '/data/data/setup-token').stdout.strip()
        password = secrets.token_urlsafe(24)
        client = fixture.account(base)
        client.call = lambda path, method='GET', body=None: call(client, path, method, body)
        client.call('setup', 'POST', {'token': token, 'username': 'admin', 'password': password, 'confirm_password': password})
        user = client.login('admin', password)['user']
        fixture.patch_user(client, user)
        source = None
        for index, source_url in enumerate(args.source_url):
            try:
                result = client.call('online/sources', 'POST', {'url': source_url})
                report['source_imports'].append({'index': index, 'state': 'passed', 'digest': result['digest']})
                source = result
                if args.init_only:
                    report['source_imports'][-1].update(name=result['name'], version=result['version'], platforms=list(result['sources']))
                    client.call('online/sources/' + result['id'], 'DELETE')
            except urllib.error.HTTPError as error:
                try:
                    result = json.loads(error.read(4096))
                    code = result.get('error', {}).get('code') if isinstance(result.get('error'), dict) else result.get('code')
                except (ValueError, TypeError):
                    code = None
                report['source_imports'].append({'index': index, 'state': 'failed', 'http_status': error.code, 'code': code})
            if args.init_only:
                print(json.dumps(report['source_imports'][-1], ensure_ascii=True))
        if args.init_only:
            return 0 if any(item['state'] == 'passed' for item in report['source_imports']) else 1
        if source is None:
            print('FAIL supplied source initialization')
            return 1
        report['source'] = {key: source[key] for key in ['name', 'version', 'digest', 'sources']}
        target = client.call('playlists', 'POST', {'id': str(uuid.uuid4()), 'name': 'Isolated online source verification', 'track_ids': []})
        target_id = target.get('playlist', target)['id']
        search = client.call('online/search?' + urllib.parse.urlencode({'q': args.query, 'platform': 'all', 'page': 1, 'limit': 5}))
        report['platforms'] = search['platforms']
        for platform in source['sources']:
            if args.platform and platform not in args.platform:
                continue
            candidates = [item for item in search['items'] if item['platform'] == platform]
            report['platforms'].append({'platform': platform, 'result_count': len(candidates)})
            if not candidates:
                continue
            bounded = [item for item in candidates if 0 < item['duration'] <= 600] or candidates
            item = bounded[min(args.candidate_index, len(bounded) - 1)]
            qualities = source['sources'][platform]['qualitys']
            quality = args.quality
            if quality not in qualities:
                report['jobs'].append({'platform': platform, 'quality': quality, 'status': 'unsupported'})
                continue
            job = client.call('online/jobs', 'POST', {'source_id': source['id'], 'item': item,
                              'quality': quality, 'target_playlist_id': target_id})
            deadline = time.monotonic() + 150
            while time.monotonic() < deadline:
                current = next(value for value in client.call('online/jobs')['jobs'] if value['id'] == job['id'])
                if current['status'] in ['completed', 'failed', 'cancelled']:
                    break
                time.sleep(.5)
            entry = {'platform': platform, 'quality': quality, 'status': current['status'],
                     'bytes': current['downloaded'], 'error': current['error'],
                     'actual_format': current.get('actual_format'),
                     'actual_bitrate_kbps': current.get('actual_bitrate_kbps'),
                     'warning': current.get('warning')}
            if current['status'] == 'completed':
                playlist = client.call('playlists/' + target_id)['playlist']
                entry['playlist_retained'] = current['track_id'] in playlist['track_ids']
                assert entry['playlist_retained']
                track = client.call('tracks/' + current['track_id'])
                track = track.get('track', track)
                entry['verified_track'] = {key: track.get(key) for key in ['title', 'artist', 'size', 'duration_ms', 'bitrate_kbps', 'format']}
                assert entry['verified_track']['size'] > 0
                assert entry['actual_format'] == track['format']
                assert entry['actual_bitrate_kbps'] == track.get('bitrate_kbps', 0)
                if quality.startswith('flac') and track['format'] != 'flac':
                    assert entry['warning'], 'The source returned another format without a warning'
            else:
                client.call('online/jobs/' + job['id'] + '/cancel', 'POST', {})
            report['jobs'].append(entry)
            print(json.dumps(entry, ensure_ascii=True))
        report['completed'] = sum(job['status'] == 'completed' for job in report['jobs'])
        return 0 if report['completed'] else 1
    finally:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2, ensure_ascii=False) + '\n', encoding='utf-8')
        compose('down', '--remove-orphans', check=False)
        for name in [volume, volume + '-tmp']:
            assert name.startswith(project + '-')
            fixture.docker('volume', 'rm', name, check=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-url', action='append', required=True)
    parser.add_argument('--query', required=True)
    parser.add_argument('--init-only', action='store_true', help='Import each URL, record initialization and delete it without downloading audio')
    parser.add_argument('--platform', action='append', choices=['kw', 'kg', 'tx', 'wy', 'mg'])
    parser.add_argument('--quality', default='128k', choices=['128k', '320k', 'flac', 'flac24bit'])
    parser.add_argument('--candidate-index', type=int, choices=range(5), default=0)
    parser.add_argument('--image', required=True)
    parser.add_argument('--worker-image', required=True)
    parser.add_argument('--docker', default='docker')
    parser.add_argument('--report', type=Path, required=True)
    raise SystemExit(verify(parser.parse_args()))
