"""Executed only inside the freshly created NetBox container."""
import os

from users.models import Token, User

os.umask(0o077)
user = User.objects.create_superuser("disposable", "test@example.invalid", password=None)
for name, version, writable in (("seed", 2, True), ("read", 2, False), ("legacy", 1, False)):
    token = Token.objects.create(user=user, version=version, write_enabled=writable)
    header = token.get_auth_header_prefix() + token.token
    # Fresh disposable container only; exclusive creation rejects preexisting paths.
    path = "/tmp/netbox-" + name + "-token"  # nosec B108
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        stream.write(header.split(" ", 1)[1])
