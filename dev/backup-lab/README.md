# Reelkeeper backup lab

A throw-away lab that stress-tests Reelkeeper's own backup and restore end to end: make a backup,
change things, restore it, and prove (with an independent dump of every table) that the data came
back exactly, on SQLite and on Postgres, across engines, across versions, and when things go wrong.

It is isolated. Nothing in it can reach a real service, the internet, the LAN or TMDB.

## Run it

```
just backup-lab          # everything, from scratch; about an hour. Results in results/<timestamp>.md
just backup-lab-down     # remove the lab's containers, network and volumes
```

Needs Colima (or Docker), `jq`, `perl`, Go. `run.sh` builds the version under test from the working tree
(`docker buildx build --load` from the repo root, tagged `reelkeeper-lab:current`), so the lab tests what is
checked out. The older versions the upgrade scenarios start from, `reelkeeper:0.8.4` and `0.8.1`, must already
be pulled first, from `ghcr.io/saltglass/reelkeeper` or, with `REELKEEPER_LAB_IMAGE`, from another registry path. The lab never touches the dev stack (`dev/compose.yaml`), except
that scenario 16 runs the existing opt-in `TestRealBackup` against the dev stack's Postgres.

Useful knobs (environment variables):

| Variable | Effect |
| --- | --- |
| `LAB_IMAGE=repo/image:tag` | test that image (for example a released tag) instead of building the working tree |
| `RUN_ONLY="s1 s2 s7"` | only those scenarios (`s1`..`s17`; the core instances are still built; `s15` includes the crash scenarios 10 and 11) |
| `SCALE=0.2` | a smaller seed for a quick check of the lab itself (default 1) |
| `BIGSCALE=3` | the multiplier for the "big" scenarios (15, 10, 11) |
| `LAB_REUSE=1` | keep containers, volumes and the earlier run's core instances (development) |
| `REAL_BACKUP=path` | the real backup for scenario 16 (scenario 16 is skipped when it is not set) |

## What is in it

* `compose.yaml`: project `rk-backup-lab`. One network `lab` with `internal: true` (no route out), Postgres 18,
  a `driver` toolbox, and Reelkeeper instances: `rk-sqlite`, `rk-pg` (the version under test); `rk-fresh-sqlite`, `rk-fresh-pg`
  (wiped and recreated per scenario); `rk-wrongkey` (a different secret key); and older versions
  `rk-old081` (0.8.1, SQLite), `rk-old081-pg` (0.8.1, Postgres), `rk-old084` (0.8.4, Postgres),
  `rk-old084-sqlite` (0.8.4, SQLite). The keys and passwords in it are made up, and marked lab-only.
* `labtool/`: a Go program (static, linux/arm64) with `seed`, `dump`, `compare`, `mutate`, `fkcheck`, and a few
  helpers. It never uses `internal/backup`, because that is what is under test.
  * `seed` is catalog-driven: it reads the tables, columns and keys from the database itself, so it works on
    older schemas too. It fills everything in foreign-key order with realistic data and edge values (emoji,
    accents, quotes, newlines, `?` and `$1` in text, empty vs NULL, zero, negative and large numbers, float
    precision, far-past and far-future timestamps with microseconds, both booleans, a 2 MB setting). Default scale:
    4,400 titles (4,000 movies, 400 series), 40,000 episodes, 44,000 media files, 120,000 history rows.
  * `dump` writes an independent canonical dump (booleans 0/1, JSON re-serialized, UTC microsecond timestamps,
    sorted rows) that compares equal between the two engines. `compare` shows the differences per table.
  * Seeding also adds a finished "run" far in the future for every background task, so no task is ever due:
    otherwise Reelkeeper would scan, search and prune in the background and change the data under test.
* `driver/scripts/`: `setup-instance.sh` (first account, then settings that hold sealed credentials),
  `smoke.sh` (logins, API key, ~40 endpoints, stored credentials still decrypt), `writer.sh`.
* `run.sh`, `lib.sh`, `scenarios.sh`: the runner. Each check prints PASS or FAIL with the reason and the run goes
  on; at the end `results/<timestamp>.md` has the scenario table, every check, and time/size/memory per step.

## Scenarios

1/2 in-place restore (SQLite, Postgres); 3 across engines through download and upload; 4 first-run restore;
5 undo through the safety backup; 6 a six-hop chain between the engines; 7 older versions (upgrade path, and a
newer backup refused); 8 wrong secret key; 9 damaged files (16 kinds); 10 crash in the middle of a restore;
11 crash in the middle of a backup; 12 backups while the database is being changed; 13 full, read-only and
unwritable backup folders; 14 retention, the schedule, same-second names; 15 size, time and memory at 1x and 3x;
16 `TestRealBackup` on a real backup.

## Isolation

The first thing `run.sh` does is prove that the network is internal, that every lab container is on it and
nowhere else, that the driver and the Reelkeeper containers have no default route, and that connections to
1.1.1.1:443, 192.168.1.20:8080, 192.168.1.30:8096 and api.themoviedb.org fail. If anything fails, the run aborts.
Once an instance has an account, it also asks the app itself to connect to those addresses.
