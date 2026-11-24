# lenv

lenv is a lightweight local development environment manager for macOS and Linux. It provides read-only discovery for PostgreSQL and Redis, and can manage a separate PostgreSQL instance on macOS. Discovery does not change or take ownership of any existing service.

## Try headless discovery

Build with Go 1.22 or newer:

```sh
go build -o lenv ./cmd/lenv
./lenv daemon
```

From another terminal:

```sh
./lenv status
./lenv discover
./lenv discover --json --dir /absolute/path/to/a/local/install
```

The daemon runs in the foreground. Press Ctrl-C to stop it. CLI requests go over a private Unix socket. To override the default socket, set `LENV_SOCKET` to the same absolute path in both terminals. Its parent directory must be private to your user.

Discovery checks the daemon's `PATH`, common installation paths, and any directories passed via `--dir`. It reports executable paths without running those binaries. It separately queries Homebrew Services without elevation on macOS and the user and system systemd inventories on Linux. A binary does not imply a registered or running service. Listings that the daemon cannot query are reported as warnings.

## Install a package

Run `./lenv service <service> list` to see available versions for your platform, and `./lenv service <service> install <version>` to download a checksum-pinned archive. Install prints the binary directory. It does not change your `PATH` or start anything.

| Service | Installable on Apple Silicon | What lenv manages |
| --- | --- | --- |
| `postgres` | 16.16, 17.12 | Isolated instances and project group membership |
| `mysql` | 8.4.10 (macOS 15 or newer) | Package only |
| `mariadb` | Not available yet | |
| `mongodb` | 8.0.38 Community Server | Package only; `mongosh` is not included |
| `redis` | Not available yet | Read-only discovery |
| `caddy` | 2.11.9 | Local HTTPS proxy via `lenv site` |
| `php` | 8.5.12 CLI | Package only; no PHP-FPM or `pdo_pgsql` in this build |
| `node` | 24.22.3 LTS with npm | Package only |

The managed catalog does not yet have entries for Intel Macs or Linux.

## Run an isolated PostgreSQL instance on macOS

Use a logged-in desktop session and run `lenv daemon` as your regular user, not with `sudo`. If the daemon was already running before you rebuilt lenv, restart it to load the new commands.

On Apple Silicon, lenv can install [PostgreSQL builds from Theseus](https://github.com/theseus-rs/postgresql-binaries/releases) without Homebrew. Install and initialization are separate so multiple instances can share the same version:

```sh
./lenv service postgres list
./lenv service postgres install 16.16
./lenv service postgres init --version 16.16 --port 55432 dev
./lenv service postgres start dev
./lenv service postgres status dev
./lenv service postgres logs dev
```

Choose an unused port. The package store lives under `~/Library/Application Support/lenv/packages/postgresql`; `init` creates a separate cluster under `~/Library/Application Support/lenv/postgres/dev`. Set `LENV_STATE_DIR` in the daemon environment to use a different private state directory.

The instance listens only on `127.0.0.1`. Use the `pgpass` file shown by `status` and the package's `bin` directory shown by `list` to connect:

```sh
PG_BIN="$HOME/Library/Application Support/lenv/packages/postgresql/16.16-darwin-arm64/bin"
PGPASSFILE="$HOME/Library/Application Support/lenv/postgres/dev/pgpass" \
  "$PG_BIN/psql" -h 127.0.0.1 -p 55432 -U lenv -d postgres
```

On macOS you can also bring your own trusted binaries, including a Homebrew build: replace `--version 16.16` with `--bin-dir /absolute/path/to/bin`. lenv never reuses that installation's existing database or changes its service registration. lenv records the resolved binary directory, so removing that build can prevent subsequent starts.

launchd keeps the PostgreSQL instance running independently of the lenv daemon and restarts it after a crash. The job is scoped to the current login session; lenv does not register it to start at the next login. `./lenv service postgres stop dev` shuts it down and removes the launchd job but retains the data.

## Link an existing Homebrew PostgreSQL service

On macOS, `lenv discover` shows registered services. If it shows a Homebrew PostgreSQL service registered for your user, use its exact formula ID:

```sh
./lenv service postgres link postgresql@16
./lenv service postgres status --linked postgresql@16
./lenv service postgres stop --linked postgresql@16
./lenv service postgres start --linked postgresql@16
./lenv service postgres unlink postgresql@16
```

Linking saves permission to control that service. It does not start or stop it. Unlinking only removes that permission and leaves the service as it is. Without `--linked`, lifecycle commands still target lenv-owned instances.

Stop keeps the existing login registration, so Homebrew can still launch the service at the next login. Start loads the existing launch file through Homebrew rather than replacing it. Status reports Homebrew's service state, not SQL readiness. System services, unregistered formulas, symlinked launch files, and other service managers are not supported for linking.

## Group instances by project

After initializing a lenv-managed PostgreSQL instance such as `dev`, add it to a group:

```sh
./lenv group create my-app
./lenv group add my-app postgres dev
./lenv group start my-app
./lenv group status my-app
./lenv group stop my-app
```

An instance can belong to one group at a time. Initialize a separate instance on a different port for a second project. Linked Homebrew services cannot be added to groups.

Group start checks for duplicate ports and checks port availability for stopped members before starting anything. Members start in the order they were added. If a member fails during startup, earlier members may still be running. Use `group status` to check, or `group stop` to stop members in reverse order. To detach a member without stopping it, use `./lenv group remove my-app postgres dev`.

## Open a local app over HTTPS

For the optional site proxy, run `lenv daemon` as your regular user and start your app separately on IPv4 loopback. Install Caddy and point lenv at your app's HTTP port:

```sh
./lenv service caddy install 2.11.9
./lenv site start --version 2.11.9 my-app 3000
./lenv site status
curl --cacert '/path/from-site-status/root.crt' https://my-app.localhost:8443/
./lenv site stop
```

To use a [Caddy 2](https://caddyserver.com/download) binary already on the daemon's `PATH`, omit `--version`.

The proxy listens only on `127.0.0.1:8443`. If that port is busy, set `LENV_HTTPS_PORT` to another unprivileged port before starting the daemon. The prototype supports one active site; stopping it or shutting down the daemon normally leaves the upstream app running. The route is not restored on daemon restart, but the local CA is reused.

lenv does not install any trust automatically. `curl --cacert` trusts the CA only for that request. For browser use on macOS, you can import the printed **`root.crt`** into your **login** keychain in Keychain Access and explicitly trust it for SSL. Remove that certificate from your login keychain to undo the change. Only do this for a CA you control, and keep lenv's state directory private: it contains the signing key.
