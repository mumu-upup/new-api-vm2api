#!/usr/bin/python3
"""Prevent upstream build fallback and enforce guest-container resource limits."""
import os
import json
import pathlib
import sys


def guarded_arguments(args, images):
    if not args or args[0] not in ('run', 'create', 'update', 'exec', 'inspect', 'ps', 'stop', 'start', 'restart', 'rm', 'network', 'image', 'images', 'pull', 'info', 'version', 'cp', 'logs', 'stats', 'wait'):
        raise ValueError('Docker command is not permitted')
    if args[0] == 'image' and (len(args) < 2 or args[1] not in ('inspect', 'ls', 'pull')):
        raise ValueError('Docker image command is not permitted')
    if args[0] == 'pull' or args[:2] == ['image', 'pull']:
        if args[-1] not in images:
            raise ValueError('Image was not validated externally')
        return ['image', 'inspect', images[args[-1]]]
    if args[:2] == ['image', 'inspect']:
        return [images.get(arg, arg) for arg in args]
    if args[0] in ('run', 'create') and not any(arg in images or arg in images.values() for arg in args[1:]):
        raise ValueError('Image was not validated externally')
    args = [images.get(arg, arg) for arg in args]
    if args[0] not in ('run', 'create', 'update'):
        return args
    result = [args[0], '--memory=512m', '--memory-swap=512m', '--cpus=1']
    index = 1
    while index < len(args):
        argument = args[index]
        flag = argument.split('=', 1)[0]
        if flag in ('--memory', '-m', '--memory-swap', '--cpus', '--cpu-quota', '--cpu-period', '--memory-reservation'):
            index += 1 if '=' in argument else 2
            continue
        if argument.startswith('-m') and argument != '-m' and not argument.startswith('--'):
            index += 1
            continue
        result.append(argument)
        index += 1
    return result


if __name__ == '__main__':
    try:
        images = json.loads(pathlib.Path(__file__).with_name('images.json').read_text())
        arguments = guarded_arguments(sys.argv[1:], images)
    except (ValueError, OSError):
        print('Docker command blocked by production resource/build policy', file=sys.stderr)
        sys.exit(126)
    os.execv('/usr/local/bin/docker', ['/usr/local/bin/docker', *arguments])
