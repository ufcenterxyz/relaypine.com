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


def configured_admin(contents):
    """Read only the owner setting; never evaluate a shell configuration file."""
    value = None
    for line in contents.splitlines():
        match = re.match(r'^\s*(?:export\s+)?SUPERADMIN_EMAIL\s*=(.*)$', line)
        if match:
            try:
                words = shlex.split(match.group(1), comments=True)
            except ValueError:
                raise SystemExit('Invalid SUPERADMIN_EMAIL assignment in .env; correct it before setup.')
            if len(words) > 1:
                raise SystemExit('Invalid SUPERADMIN_EMAIL assignment in .env; correct it before setup.')
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
        owner = configured_admin(contents)
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
    envlocal = root / 'apps/console/.env.local'
    if not envlocal.exists():
        envlocal.write_text('GATEWAY_URL=http://127.0.0.1:4811\n')
    print('Next: make infra, then make gateway and make console in separate terminals.')


if __name__ == '__main__':
    main()
