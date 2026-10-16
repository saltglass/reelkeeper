# Installing Reelkeeper

Reelkeeper runs as one container. It needs somewhere to keep its own data, a
way to reach your downloads and your media folders, and (optionally) a
PostgreSQL database. This page shows two ways to set it up with Docker
Compose:

- [SQLite](#option-1-sqlite) is the simplest. There is nothing else to run.
- [PostgreSQL](#option-2-postgresql) is for people who already run a
  PostgreSQL server or want the database in a separate container.

Either way, finish with [First-time setup](first-run.md).

Before you start you need:

- Docker with Compose v2.
- qBittorrent (with its Web UI turned on), Transmission 4.0 or newer, or
  Deluge 2.1 or newer (with its web UI running and the Label plugin turned
  on), running somewhere Reelkeeper can reach. Reelkeeper does not include a
  download client. qBittorrent Enhanced Edition works too (tested with
  5.2.4.10).
- A free [TMDB](https://www.themoviedb.org) account and API key. Reelkeeper
  uses it to identify movies and shows.

## Folders and paths: read this first

Reelkeeper and your download client (qBittorrent, Transmission or Deluge;
"qBittorrent" below stands for any of them) must see your files at **the same paths**.

When Reelkeeper grabs a release, qBittorrent downloads it and tells Reelkeeper
where it saved the files, as a path like `/data/downloads/Some.Movie.2020`.
Reelkeeper then opens that exact path to copy the file into your library.
Reelkeeper has no "remote path mapping" setting, so if qBittorrent says
`/data/downloads/...` then `/data/downloads/...` must also exist inside the
Reelkeeper container and hold the same files.

So mount your folders into the Reelkeeper container at the same paths
qBittorrent uses:

| What | Inside qBittorrent | Inside Reelkeeper |
| --- | --- | --- |
| Downloads | `/data/downloads` | `/data/downloads` (the same) |
| Movies and TV | not needed | `/data/media/movies`, `/data/media/tv` (your choice) |

The paths in this guide (`/data/downloads`, `/data/media`) are only examples.
Use your own, as long as the downloads path matches on both sides. If
qBittorrent runs in a container too, give both containers the same
`/data/downloads:/data/downloads` mount.

Imports always **copy** the file and leave the original alone, so the torrent
keeps seeding. Your download folder and your media folder do not need to be on
the same disk.

## Option 1: SQLite

Create a folder for the stack and put two files in it.

`compose.yaml`:

```yaml
services:
  reelkeeper:
    image: ghcr.io/saltglass/reelkeeper:X.Y.Z
    container_name: reelkeeper
    restart: unless-stopped
    user: "1000:1000"
    environment:
      TZ: America/New_York
      REELKEEPER_SECRET_KEY: ${REELKEEPER_SECRET_KEY:?REELKEEPER_SECRET_KEY must be set}
    ports:
      - "7676:7676"
    volumes:
      - ./config:/config
      - /data/downloads:/data/downloads
      - /data/media:/data/media
```

`.env` (next to `compose.yaml`; Compose reads it automatically):

```
REELKEEPER_SECRET_KEY=paste-a-long-random-value-here
```

Make up the key with `openssl rand -hex 32`, and read
[The secret key](#the-secret-key) before you start.

Then:

```sh
mkdir config
sudo chown 1000:1000 config      # the same numbers as "user:" above
docker compose up -d
```

Open `http://<your-server>:7676`. The first visit asks you to create the
administrator account.

The folder mounted at `/config` must be writable by the `user:` above, on
Postgres installs too (the secret key, backups and indexer definitions live
there). A bind mount like `./config` gets that from the `chown`. A Docker
*named* volume is created owned by root, so either `chown 1000:1000` its
folder or use a bind mount instead; otherwise Reelkeeper stops at startup
and says it can't write to its data folder.

Replace `X.Y.Z` with a version from the
[releases page](https://github.com/saltglass/reelkeeper/releases). Use an
exact version tag, not `latest`, so you decide when Reelkeeper changes.

Notes on the example:

- `restart: unless-stopped` matters. Reelkeeper stops itself after you ask
  for a [restore](backups.md), and relies on Docker starting it again.
- `TZ` sets the time zone used for the daily backup time and for "today" in
  the grab counts. Without it the container uses UTC.
- The `./config` folder holds the database, so keep it somewhere you back up.

## Option 2: PostgreSQL

This example starts PostgreSQL next to Reelkeeper. If you already have a
PostgreSQL server, leave out the `postgres` service, create an empty database
and a user that owns it, and point `REELKEEPER_DB` at it.

`compose.yaml`:

```yaml
services:
  reelkeeper:
    image: ghcr.io/saltglass/reelkeeper:X.Y.Z
    container_name: reelkeeper
    restart: unless-stopped
    user: "1000:1000"
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      TZ: America/New_York
      REELKEEPER_SECRET_KEY: ${REELKEEPER_SECRET_KEY:?REELKEEPER_SECRET_KEY must be set}
      REELKEEPER_DB: postgres://reelkeeper:${POSTGRES_PASSWORD:?POSTGRES_PASSWORD must be set}@postgres:5432/reelkeeper?sslmode=disable
    ports:
      - "7676:7676"
    volumes:
      - ./config:/config
      - /data/downloads:/data/downloads
      - /data/media:/data/media

  postgres:
    image: postgres:18-alpine
    container_name: reelkeeper-postgres
    restart: unless-stopped
    environment:
      POSTGRES_USER: reelkeeper
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?POSTGRES_PASSWORD must be set}
      POSTGRES_DB: reelkeeper
    volumes:
      - postgres:/var/lib/postgresql
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "reelkeeper", "-d", "reelkeeper"]
      interval: 5s
      timeout: 3s
      retries: 20

volumes:
  postgres:
```

`.env`:

```
REELKEEPER_SECRET_KEY=paste-a-long-random-value-here
POSTGRES_PASSWORD=choose-a-password-with-only-letters-and-numbers
```

Use only letters and numbers in the password. It sits inside a web address
(`postgres://user:password@host/...`), and characters like `@` or `/` would
break it.

The database address looks like this:

```
postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=disable
```

`postgresql://` works too. Reelkeeper creates and upgrades its own tables on
every start; it only needs a database it may create tables in. Remove
`?sslmode=disable` if your server uses TLS. Reelkeeper is tested against
PostgreSQL 18.

You still mount `/config` with PostgreSQL. It holds the generated or
downloaded files Reelkeeper keeps outside the database (see
[The data folder](#the-data-folder)), and it is the default place for
[backups](backups.md).

## The settings (environment variables)

These are the only settings that live outside the app. They are needed before
Reelkeeper can open its database. Everything else is edited in the web UI.

| Variable | Default | What it does |
| --- | --- | --- |
| `REELKEEPER_DB` | `reelkeeper.db` inside the data folder | Where the database is. Either a SQLite file path or a `postgres://` address. |
| `REELKEEPER_PORT` | `7676` | The port Reelkeeper listens on inside the container. A number from 1 to 65535. |
| `REELKEEPER_URL_BASE` | empty | A path prefix, such as `/reelkeeper`, if you serve Reelkeeper below a sub-path of a web address instead of on its own name. Leave empty if you use a hostname like `reelkeeper.example.com`. |
| `REELKEEPER_DATA_DIR` | `/config` in the image (`./data` when run outside Docker) | The data folder. See below. |
| `REELKEEPER_SECRET_KEY` | none; a key is generated and saved in the data folder | Scrambles the passwords and keys Reelkeeper stores. At least 16 characters. See below. |
| `REELKEEPER_RESET_AUTH` | unset | Set to `true` to turn password sign-in back on when you are locked out. See [Signing in](sign-in.md#getting-back-in). Remove it afterwards. |

The image also reads the usual `TZ` time zone variable.

A bad value (for example a port of `abc`) stops Reelkeeper at startup with a
message saying which variable is wrong.

## The secret key

Reelkeeper stores logins for your trackers, qBittorrent, email and Emby, plus
your single sign-on secret and API key. They are stored scrambled
("sealed") with `REELKEEPER_SECRET_KEY`.

- **Set it yourself.** If you leave it out, Reelkeeper makes a random key and
  saves it as `secret.key` in the data folder, and writes a warning in the log.
  That works, but then the key and the database sit in the same folder.
- **Keep a copy somewhere else**, such as a password manager. The key is not
  part of a [backup](backups.md). If you lose it, everything else (your
  library, profiles, history) survives, but you have to type in every stored
  password again.
- **Do not change it** once Reelkeeper is in use. With a different key, the
  stored passwords can no longer be read. Settings → Security shows a warning
  if that happens; putting the old key back fixes it.
- A restore only accepts a backup made with the same key.

## The data folder

The image keeps its data in `/config` (that is what `REELKEEPER_DATA_DIR` is
set to). It contains:

- `reelkeeper.db`: the database, when you use SQLite.
- `secret.key`: only if you did not set `REELKEEPER_SECRET_KEY`.
- `definitions/`: the tracker definitions Reelkeeper downloads for the
  Indexers page. It can be rebuilt, so it does not need backing up.
- `backups/`: where [backups](backups.md) go, unless you choose another folder.

Always mount `/config` from your server (`./config:/config` above). Without a
mount, everything is lost when the container is replaced.

## Running as a particular user

The image has no shell and runs as an unprivileged user by default. Use
`user: "UID:GID"` in Compose to run as the user that should own your files.
That user needs:

- **write** access to the `/config` folder,
- **write** access to your media folders (so Reelkeeper can add and rename
  files there), and
- **read** access to the download folder.

If a folder is on a NAS that forces its own owner on every file, use that
owner's numbers. Reelkeeper never changes file owners or permissions itself.

If you see "permission denied" in the log, the numbers in `user:` and the
folder's owner do not match.

## The image has no shell

The container image holds only the Reelkeeper program. There is no `sh`, no
`curl`, and no package manager, so `docker exec -it reelkeeper sh` will not
work. Everything you need is a Reelkeeper command, run like this:

```sh
docker exec reelkeeper /reelkeeper version
docker exec reelkeeper /reelkeeper reset-auth
```

The commands are `serve` (the default), `migrate`, `reset-auth`,
`healthcheck`, `probe` and `version`.

The health check is built into the image. It runs
`/reelkeeper healthcheck`, which asks Reelkeeper's own `/ping` page whether it
is alive and exits with a failure if not. If you want to write it yourself in
Compose:

```yaml
    healthcheck:
      test: ["CMD", "/reelkeeper", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 20s
```

`/ping` needs no sign-in and never contacts any other service.

## Reverse proxy and HTTPS

Reelkeeper speaks plain HTTP. To reach it over HTTPS from outside your home,
put a reverse proxy in front (Traefik, Caddy, nginx and so on) that forwards
`reelkeeper.example.com` to port 7676. Then tell Reelkeeper about the proxy
under Settings → Security → Reverse proxies. This is required for single
sign-on and for the bypass list to work safely. See
[Signing in](sign-in.md#reverse-proxies-and-the-url-base).

## Updating

1. In Reelkeeper, open System → Backups and choose Back up now. (If you have
   not read about backups yet, see [Backups](backups.md).)
2. Change the version tag on the `image:` line of `compose.yaml`.
3. Run `docker compose pull && docker compose up -d`.

Reelkeeper upgrades its database on startup. On SQLite it first saves a copy
of the old database file into the backups folder (a file named like
`reelkeeper-pre-migrate-v12-20260101-120000.db`; the newest three are kept).

Going back to an older version is not supported. If an update goes wrong,
put the old image tag back and restore the backup you made in step 1.
