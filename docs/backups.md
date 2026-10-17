# Backups

Reelkeeper makes its own backups of everything it knows, on a schedule or
when you ask, and can restore them from the app. They live at **System →
Backups**.

## What a backup contains

A backup holds the whole database: your library (titles, versions, files
found), settings, quality profiles, indexers, history, blocklist and so on.
The one thing left out is the list of **sign-in sessions**, so after a restore
everyone signs in again.

A backup does **not** contain:

- **Your media files and downloads.** Those are on your disks; back them up
  the way you already do.
- **The built-in download client's torrent list.** If you use Reelkeeper's
  [built-in client](install.md#the-built-in-download-client), its torrents,
  and which pieces of them it has, are kept in the `builtin` folder inside the
  data folder, not in the database. Like the list inside qBittorrent, they are
  not in a backup (the downloaded files themselves are on your disk, as
  above). Back that folder up too if you want the client to remember its
  torrents after a disaster; a restore leaves it alone.
- **The secret key** (`REELKEEPER_SECRET_KEY`). Stored passwords and keys
  (indexer logins, qBittorrent, email, Emby, your single sign-on secret and
  API key) are in the backup but sealed with that key, so they can only be read
  with the same key. Keep a copy of the key somewhere safe, apart from your
  backups. See [The secret key](install.md#the-secret-key).

A backup is a single compressed file ending in `.backup`, named like
`reelkeeper-20261011-033000-scheduled.backup` (the time is UTC). It is
written in a neutral format, not the database's own, so the same file can be
restored into SQLite or PostgreSQL. A typical library makes a backup of a few
megabytes.

## Making backups

- **Back up now** on the Backups page makes one immediately. You can do the
  same from System → Tasks by running "Back up the database".
- **Daily backup:** on by default, at **03:30** (in the container's time zone;
  set `TZ` in Compose, see [Installing](install.md)). If Reelkeeper was
  switched off at that time, the backup is made as soon as it is running again.
- **How many to keep:** the newest **14** scheduled and manual backups. After
  each new backup, older ones are deleted. You can set any number from 1 to
  365.

The time, the number kept, the daily switch and the folder are all settings
on the Backups page.

The list shows each backup's date, size, the Reelkeeper version that made it,
and its kind:

- **Scheduled**: made by the daily timer.
- **Manual**: made by Back up now.
- **Safety**: made automatically just before a restore (see below).

You can download any backup to your computer, upload one you saved earlier
(it is stored as `...-uploaded.backup`), and delete ones you do not want.

### Where they go

By default, into a `backups` folder inside the data folder
(`/config/backups` in Docker). That is on the same disk as the database, so
it protects you from mistakes but not from a failed disk.

To keep backups somewhere else, such as a network share or another disk,
mount that folder into the container and enter its path as the backup
**folder** on the Backups page. The path is the one inside the container, and
must be a full path:

```yaml
    volumes:
      - ./config:/config
      - /mnt/nas/reelkeeper-backups:/backups
```

Then set the folder to `/backups`. The user Reelkeeper runs as must be allowed
to write there. Leave the setting empty to go back to the data folder.

Pruning only looks at the folder currently set; backups in an old folder stay
where they are.

If you use PostgreSQL, you can also back up the PostgreSQL server with its own
tools. The two do not conflict.

## The secret key

Every backup records a fingerprint of the key it was made with (not the key
itself). Restoring needs the same key. If the key is different, Reelkeeper
refuses before changing anything and tells you why; start Reelkeeper with the
original key, then restore.

If you have lost the key for good, the backup is still useful for the library,
profiles and history, but Reelkeeper will not restore it. You would set up
again from scratch and re-enter your passwords.

## Restoring

1. Open System → Backups and pick a backup from the list (or upload one
   first).
2. Choose Restore. Reelkeeper checks the file first, **without changing
   anything**: that it is complete and not damaged, that the secret key
   matches, and that it was not made by a newer Reelkeeper than the one
   running. It then tells you what it will replace.
3. Confirm. Reelkeeper restarts. This depends on Docker bringing the container
   back up, which is why the examples use `restart: unless-stopped`.
4. On the way back up, before anything else runs, Reelkeeper first makes a
   **safety backup** of the database as it is now, in the same folder. Then it
   replaces everything in one step. If any part fails, nothing is changed and
   Reelkeeper starts with the database it had. The result is shown on the
   Backups page.
5. Everyone is signed out. Sign in again.

A few rules to know about:

- A backup from an **older** Reelkeeper is brought up to date automatically.
- A backup from a **newer** Reelkeeper is refused. Update Reelkeeper first.
- A backup made with a **different secret key** is refused.
- A **damaged or unfinished** file is refused.

A restore can be undone: the safety backup is in the list. Restore that one.
The newest three safety backups are kept; they are not counted in the number of
backups you keep, and uploaded backups are never deleted automatically.

After a restore, files on disk are unchanged. Run a scan from Settings → Media
folders if you want Reelkeeper to compare its restored library with what is
there now.

## Rebuilding on a new machine

1. Install Reelkeeper as described in [Installing](install.md), with the
   **same `REELKEEPER_SECRET_KEY`**. (If you never set one, copy the
   `secret.key` file from the old data folder into the new one instead.)
2. Open it. On the first-run screen, instead of creating an account, choose
   **Restore from a backup** and pick the backup file (download one from
   System → Backups on the old install).
3. Reelkeeper checks the file, restarts and restores it. Sign in with the
   accounts from the backup.

This only works on a new install, before any account exists. On an install
that's already set up, upload the backup on System → Backups and restore it
there.

## Moving between SQLite and PostgreSQL

Backups don't depend on the database engine, so they are also the way to move
from SQLite to PostgreSQL, or back:

1. Make a backup on the current install (System → Backups → Back up now) and
   download it.
2. Start Reelkeeper pointed at the other, empty database (change
   `REELKEEPER_DB`), with the **same secret key**.
3. On its first-run screen, choose **Restore from a backup** and give it the
   file.

The old database is left as it was, so you can switch back by pointing
`REELKEEPER_DB` at it again.
