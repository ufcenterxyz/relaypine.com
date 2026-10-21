#!/usr/bin/env python3
"""Create local development secrets once; never print their values."""
import base64
from email.headerregistry import Address
import os
from pathlib import Path
import re
import secrets
import shlex
import sys
from urllib.parse import urlsplit, urlunsplit


def admin_email(value):
    value = value.strip().lower()
    try:
        address = Address(addr_spec=value)
        valid = (address.addr_spec == value and address.username and
                 address.domain and len(value.encode()) <= 254)
    except (ValueError, IndexError):
        valid = False
    if not valid:
        raise SystemExit('SUPERADMIN_EMAIL must be a valid email address without a display name.')
    return value


def configured_value(contents, key):
    """Read a setting without evaluating a shell configuration file."""
    value = None
    for line in contents.splitlines():
        match = re.match(r'^\s*(?:export\s+)?' + re.escape(key) + r'\s*=(.*)$', line)
        if match:
            try:
                words = shlex.split(match.group(1), comments=True)
            except ValueError:
                raise SystemExit(f'Invalid {key} assignment in .env; correct it before setup.')
            if len(words) > 1:
                raise SystemExit(f'Invalid {key} assignment in .env; correct it before setup.')
            value = words[0] if words else ''
    return value


def main():
    root = Path(__file__).resolve().parents[1]
    target = root / '.env'
    supplied_admin = os.environ.get('SUPERADMIN_EMAIL', '')
    if not target.exists():
        if not supplied_admin:
            if not sys.stdin.isatty():
                raise SystemExit('SUPERADMIN_EMAIL is required. Run SUPERADMIN_EMAIL=you@example.com make setup.')
            supplied_admin = input('Administrator email (receives sign-in codes): ')
        owner = admin_email(supplied_admin)
        password = secrets.token_hex(24)
        values = {
            'MYSQL_ROOT_PASSWORD': secrets.token_hex(24),
            'MYSQL_PASSWORD': password,
            'MYSQL_DSN': f'relaypine:{password}@tcp(127.0.0.1:4812)/relaypine?parseTime=true&loc=UTC',
            'MASTER_KEY': base64.b64encode(os.urandom(32)).decode(),
            'SUPERADMIN_EMAIL': owner,
        }
        lines = []
        for line in (root / '.env.example').read_text().splitlines():
            if line and not line.startswith('#') and '=' in line:
                key, value = line.split('=', 1)
                line = key + '=' + shlex.quote(values.get(key, value))
            lines.append(line)
        # Create exclusively with private permissions; do not expose secrets
        # between writing and chmod, or overwrite a concurrent setup.
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as output:
            output.write('\n'.join(lines) + '\n')
        print('Created .env with local-only infrastructure credentials. Configure MAILGUN_* to enable email-code sign-in.')
    else:
        contents = target.read_text()
        owner = configured_value(contents, 'SUPERADMIN_EMAIL')
        if owner:
            admin_email(owner)
            print('Existing .env preserved, including its administrator setting.')
        else:
            if not supplied_admin:
                raise SystemExit('Existing .env needs SUPERADMIN_EMAIL. Run SUPERADMIN_EMAIL=you@example.com make setup; existing secrets will be preserved.')
            owner = admin_email(supplied_admin)
            with target.open('a') as output:
                output.write(('\n' if contents and not contents.endswith('\n') else '') +
                             'SUPERADMIN_EMAIL=' + shlex.quote(owner) + '\n')
            target.chmod(0o600)
            print('Added SUPERADMIN_EMAIL; all existing configuration and secrets were preserved.')
    contents = target.read_text()
    app_url = configured_value(contents, 'APP_URL') or 'http://localhost:4810'
    admin_url = configured_value(contents, 'ADMIN_APP_URL')
    if not admin_url:
        origin = urlsplit(app_url)
        # Separate DNS names, even during development, keep cookies isolated.
        hostname = 'admin.localhost' if origin.hostname in ('localhost', '127.0.0.1', '::1') else 'admin.' + (origin.hostname or 'localhost')
        authority = hostname + (f':{origin.port}' if origin.port else '')
        admin_url = urlunsplit((origin.scheme or 'http', authority, '', '', ''))
        with target.open('a') as output:
            output.write(('\n' if contents and not contents.endswith('\n') else '') +
                         'ADMIN_APP_URL=' + shlex.quote(admin_url) + '\n')
        print('Added a separate ADMIN_APP_URL; existing settings were preserved.')
    envlocal = root / 'apps/console/.env.local'
    local_contents = envlocal.read_text() if envlocal.exists() else ''
    additions = {key: value for key, value in {
        'GATEWAY_URL': 'http://127.0.0.1:4811',
        'APP_URL': app_url,
        'ADMIN_APP_URL': admin_url,
    }.items() if not configured_value(local_contents, key)}
    if additions:
        with envlocal.open('a') as output:
            if local_contents and not local_contents.endswith('\n'):
                output.write('\n')
            for key, value in additions.items():
                output.write(key + '=' + shlex.quote(value) + '\n')
    print('Next: make infra, then make gateway and make console in separate terminals.')


if __name__ == '__main__':
    main()
