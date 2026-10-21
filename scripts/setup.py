#!/usr/bin/env python3
"""Create local development secrets once; never print their values."""
import base64
import os
from pathlib import Path
import secrets

root = Path(__file__).resolve().parents[1]
target = root / '.env'
if not target.exists():
    password = secrets.token_hex(24)
    values = {
        'MYSQL_ROOT_PASSWORD': secrets.token_hex(24),
        'MYSQL_PASSWORD': password,
        'MYSQL_DSN': f'relaypine:{password}@tcp(127.0.0.1:4812)/relaypine?parseTime=true&loc=UTC',
        'MASTER_KEY': base64.b64encode(os.urandom(32)).decode(),
        'BOOTSTRAP_EMAIL': 'ufcenterxyz@gmail.com',
    }
    lines = []
    for line in (root / '.env.example').read_text().splitlines():
        if line and not line.startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            value = values.get(key, value)
            line = key + "='" + value.replace("'", "'\\''") + "'"
        lines.append(line)
    target.write_text('\n'.join(lines) + '\n')
    target.chmod(0o600)
    print('Created .env with local-only infrastructure credentials. Configure MAILGUN_* to enable email-code sign-in.')
else:
    print('Existing .env preserved.')
envlocal = root / 'apps/console/.env.local'
if not envlocal.exists():
    envlocal.write_text('GATEWAY_URL=http://127.0.0.1:4811\n')
print('Next: make infra, then make gateway and make console in separate terminals.')
