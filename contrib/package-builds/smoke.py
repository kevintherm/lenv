import os
from pathlib import Path
import pwd
import secrets
import subprocess
import sys
import tempfile
import time


def run(cmd, env, check=True):
    result = subprocess.run(cmd, env=env, capture_output=True, text=True, timeout=10)
    if check and result.returncode:
        raise RuntimeError(f"{cmd[0]} exited {result.returncode}:\n{result.stdout}{result.stderr}")
    return result


def data_check(server, query, env, changed, read_back, root):
    for restart in (False, True):
        with (root / "server.log").open("w+") as log:
            with subprocess.Popen(server, env=env, stdout=log, stderr=log) as process:
                try:
                    deadline = time.monotonic() + 15
                    while True:
                        if process.poll() is not None or time.monotonic() >= deadline:
                            log.seek(0)
                            raise RuntimeError(f"server did not become ready:\n{log.read()}")
                        if query() == "ready":
                            break
                        time.sleep(0.1)
                    if restart:
                        if read_back() != "lenv-smoke":
                            raise RuntimeError("data did not survive restart")
                    else:
                        changed()
                finally:
                    if process.poll() is None:
                        process.terminate()
                        try:
                            process.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()
                    if process.returncode != 0:
                        log.seek(0)
                        raise RuntimeError(f"server did not stop cleanly:\n{log.read()}")


def redis(bundle, root, env):
    bindir = bundle / "bin"
    sock = root / "r.sock"
    password = secrets.token_hex(24)
    config = root / "redis.conf"
    config.write_text(f'port 0\nunixsocket "{sock}"\nunixsocketperm 700\ndir "{root}"\nrequirepass {password}\n')
    config.chmod(0o600)
    cli = [bindir / "redis-cli", "--raw", "-s", sock]
    authenticated = dict(env, REDISCLI_AUTH=password)

    def call(*args, check=True):
        return run([*cli, *args], authenticated, check=check).stdout.strip()

    def change():
        if "NOAUTH" not in run([*cli, "PING"], env, check=False).stdout:
            raise RuntimeError("Redis accepted an unauthenticated command")
        if call("SET", "key", "lenv-smoke") != "OK" or call("SAVE") != "OK":
            raise RuntimeError("Redis write or SAVE failed")

    data_check(
        [bindir / "redis-server", config],
        lambda: "ready" if call("PING", check=False) == "PONG" else "",
        env,
        change,
        lambda: call("GET", "key"),
        root,
    )


def mariadb(bundle, root, env):
    bindir = bundle / "bin"
    sock = root / "m.sock"
    data = root / "data"
    account = pwd.getpwuid(os.geteuid()).pw_name
    setup = next(path for path in (
        bindir / "mariadb-install-db",
        bundle / "scripts" / "mariadb-install-db",
    ) if path.is_file())
    run([setup, "--no-defaults", f"--basedir={bundle}", f"--datadir={data}",
         "--auth-root-authentication-method=socket", f"--auth-root-socket-user={account}",
         "--skip-test-db"], env)
    client = [bindir / "mariadb", "--no-defaults", f"--socket={sock}", f"--user={account}", "-NBe"]

    def sql(statement, check=True):
        return run([*client, statement], env, check=check).stdout.strip()

    data_check(
        [bindir / "mariadbd", "--no-defaults", f"--basedir={bundle}", f"--datadir={data}",
         f"--socket={sock}", f"--pid-file={root / 'm.pid'}", "--skip-networking"],
        lambda: sql("SELECT 'ready'", check=False),
        env,
        lambda: sql("CREATE DATABASE probe; CREATE TABLE probe.data (val TEXT); INSERT INTO probe.data VALUES ('lenv-smoke')"),
        lambda: sql("SELECT val FROM probe.data"),
        root,
    )


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] not in ("redis", "mariadb"):
        sys.exit(f"Usage: {sys.argv[0]} redis|mariadb EXTRACTED_BUNDLE_DIRECTORY")
    if os.geteuid() == 0:
        sys.exit("Run the smoke test as a regular user.")
    bundle = Path(sys.argv[2]).resolve(strict=True)
    tests = {"redis": redis, "mariadb": mariadb}
    with tempfile.TemporaryDirectory(prefix="lv-smoke-") as tmp:
        root = Path(tmp)
        tests[sys.argv[1]](bundle, root, {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HOME": tmp, "LC_ALL": "C"})
    print("Local socket query, clean stop, and restart persistence passed.")
