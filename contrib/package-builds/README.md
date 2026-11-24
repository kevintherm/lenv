# Apple Silicon package build experiments

These scripts build candidate archives from checksum-pinned upstream sources. They are for maintainers, not part of `lenv service install`. They do not publish packages, change the catalog, or install anything into Homebrew or system directories.

Run on Apple Silicon as a regular user with Apple's Command Line Tools installed. For MariaDB, also supply the absolute path to CMake (the experiment uses [CMake 3.31.12](https://github.com/Kitware/CMake/releases/tag/v3.31.12)).

```sh
out="$(mktemp -d)"
bash contrib/package-builds/redis-darwin-arm64.sh "$out"
bash contrib/package-builds/mariadb-darwin-arm64.sh "$out" /absolute/path/to/cmake
```

The candidates target macOS 15 or newer. The Redis package contains the core server and CLI, without TLS or the optional Redis modules. The MariaDB release candidate uses bundled wolfSSL for the server, with client TLS and Galera clustering disabled. The earlier static OpenSSL experiment is not a release candidate.

MariaDB can take longer than 15 minutes to compile. The recipe prints its work directory and retains it on failure or interruption. If CMake configuration finished, pass that same work directory as the third argument to resume compilation:

```sh
bash contrib/package-builds/mariadb-darwin-arm64.sh "$out" /absolute/path/to/cmake "$out/.mariadb-build.XXXXXXXX"
```

After extracting an archive into a different directory, run the smoke test against the extracted bundle:

```sh
python3 contrib/package-builds/smoke.py redis /path/to/extracted/redis-bundle
python3 contrib/package-builds/smoke.py mariadb /path/to/extracted/mariadb-bundle
```

The probe uses a private Unix socket and temporary data. It does not contact a running Redis or MariaDB instance.

Verify that lenv's package extractor accepts the archive, too:

```sh
LENV_TEST_PACKAGE_ARCHIVE=/absolute/path/to/candidate.tar.gz go test ./internal/packages -run '^TestCandidateArchive$' -count=1
```

Before adding a candidate to the public catalog, verify it from a relocated directory on a Mac without Homebrew dependencies. Check the runtime libraries, initialize temporary data, query the service, stop it, and verify data survives restart. Decide who owns updates and where to host the archives. Review license notices and corresponding-source requirements before distributing binaries. The printed SHA-256 is for that specific build; the scripts do not promise byte-for-byte reproducibility across toolchains.

Publish package assets under versioned `pkg-` tags, separate from lenv application releases. Include matching upstream source, the build recipe and any patches or dependency sources needed to reproduce the binary, license notices, and `SHA256SUMS`. Verify the public download before adding its URL, size, and SHA-256 to the catalog. Use a new tag for a rebuilt package; don't replace an asset referenced by a released catalog.
