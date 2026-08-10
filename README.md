# OSTreeUploader
Tools to upload an ostree repo to the Foundries backend, and a pure-Go ostree
client (`fiopull`) to pull commits and estimate an update's size.

## Usage

### Build

```make```

This builds `fiopush`, `fiocheck` and `fiosync`. `fiopull` is Linux-only and is
not part of the default target; build it separately:

```make fiopull```

### Run

#### Push
Pushes an ostree repo to the Factory storage.
```
./bin/fiopush -creds <credentials.zip> -repo <path-to-repo>
```

#### Check
Checks whether a given ostree repo is present on the Factory storage.
```
./bin/fiocheck -creds <credentials.zip> -repo <path-to-repo>
```

#### Sync
Syncs a given ostree repo commit from one Factory to another.
The `ostree` command line utility must be installed on a host system.
```
./bin/fiosync -src-creds <src-factory_credentials.zip> -dst-creds <dst-factory_credentials.zip> -commit <commit-to-sync> [-repo-dir <directory to download a source repo commit>]
```
If `-repo-dir` is not specified then a temporal directory will be created before the pull process and then removed once a repo is fully synced.
If `-repo-dir` is specified then the repo directory is not removed after the sync process completion.

#### Pull
`fiopull` is a pure-Go ostree client (no libostree, no `ostree` binary). It is
Linux-only and designed to be shelled out to by other tools (e.g.
aktualizr-lite), so it offers a machine-readable `--format json` mode. It has
two subcommands.

`--url` may be an `http(s)://` or `file://` base URL of an ostree repo — for
example a signed object-store (GCS) URL that a caller such as aktualizr-lite
obtained from the Device Gateway. `fiopull` is Device-Gateway-agnostic: it does
not talk to the gateway or handle mTLS/PKCS#11 itself. Pass any auth via the URL
itself (a signed URL) or via repeatable `--header 'Key: Value'` (e.g.
`--header 'Authorization: Bearer <token>'`).

##### update-size
Estimates the storage an update needs, without downloading object bodies. It
picks the cheapest accurate source automatically: the `--from`→target static
delta superblock if one is published, else the target commit's `ostree.sizes`
metadata (present when the commit was built with `ostree commit
--generate-sizes`). Both are a single fetch.
```
./bin/fiopull update-size --url <URL> (--ref <REF> | --commit <CSUM>) [--from <CSUM>] [--repo <local-repo>] [--format text|json]
```
`--from` enables the static-delta fast path. `--repo` points at a local
bare-user repo whose already-present objects are subtracted from the estimate.
When neither a static delta nor `ostree.sizes` is available the size cannot be
determined and the command exits 4.

Exit codes: `0` ok, `1` error, `2` usage, `3` insufficient storage, `4` size
unavailable.

The `ostree.sizes` metadata path only works if the factory's ostree commits were
built with `ostree commit --generate-sizes`. For LmP-based factories this is
controlled by the OSTree commit step of the image build in your
`meta-subscriber-overrides` layer (or your own Yocto CI); enable it there so the
commits your factory publishes carry the sizes table. Without it (and without a
published static delta) `update-size` cannot estimate a size and exits 4.

##### pull
Pulls a commit (and every object it references) into a local bare-user repo. The
pull is delta-aware and resumable: pass `--from` to use the from→target static
delta when the remote publishes one (falling back to a full object pull), and
re-running resumes an interrupted pull.
```
./bin/fiopull pull [--from <CSUM>] [--no-delta] [--jobs <N>] --repo <local-repo> <URL> <COMMIT | REF>
```
`URL` and the target `COMMIT` (a 64-char hex checksum) or `REF` (e.g. `main`,
resolved against the remote) are mandatory positional arguments and must come
after any flags. `--no-delta` forces a full object pull; `--jobs` bounds
concurrent content downloads.

