# Connecting Seerr

[Seerr](https://github.com/seerr-team/seerr) is where you and your family
request movies and shows. Reelkeeper answers Seerr the way Radarr and Sonarr
do, so you add it to Seerr as an ordinary Radarr or Sonarr server. Nothing
needs installing on the Seerr side.

The one thing that is different from Radarr and Sonarr: **you add Reelkeeper
once for every quality profile you want requests to use.** Each profile has
its own address.

## What you need

- Reelkeeper's address, as Seerr can reach it. Seerr makes the connection from
  its own server, not from your browser, so use a name or IP that works from
  where Seerr runs. If both are containers on the same Docker network, that
  can be `reelkeeper` and port `7676`.
- Reelkeeper's **API key**: Settings → Security → API key. Press the eye
  button to show it and the copy button to copy it.
- The **slug** of each quality profile. Open Settings → Quality profiles and
  open the profile; the slug is shown under its name, as in `hd · used for
  Movies and TV`. A fresh install has `hd` and `remux`. The slug is made when
  the profile is created and never changes, even if you rename the profile.
- A TMDB key set in Reelkeeper (see [First-time setup](first-run.md)).
  Seerr asks for series by their TVDB number, and Reelkeeper looks them up
  through TMDB.

## The URL base for each profile

The "URL base" you give Seerr is where the profile lives:

| Seerr server type | URL base |
| --- | --- |
| Radarr (movies) | `/compat/radarr/<profile-slug>` |
| Sonarr (TV) | `/compat/sonarr/<profile-slug>` |

For example `/compat/radarr/hd`, `/compat/radarr/remux` or
`/compat/sonarr/hd`.

A profile only appears on the kind of server it is used for. A profile set to
"Movies" works as a Radarr server only, one set to "TV" as a Sonarr server
only, and one set to "Movies and TV" (like the built-in HD) as both. If you
get this wrong, Seerr's Test button reports an error.

If you run Reelkeeper below a sub-path with `REELKEEPER_URL_BASE` (for
example `/reelkeeper`), put that in front: `/reelkeeper/compat/radarr/hd`.

## Adding a movie server

In Seerr, open Settings → Services and choose Add Radarr Server. Fill in:

- **Server Name**: anything you like, for example `Reelkeeper (HD)`.
- **Hostname or IP Address** and **Port**: Reelkeeper's. For a server behind
  HTTPS on a hostname such as `reelkeeper.example.com`, use that name,
  port `443` and switch on **Use SSL**. For a direct connection on your
  network, use port `7676` and leave Use SSL off.
- **API Key**: Reelkeeper's API key.
- **URL Base**: `/compat/radarr/hd` (or your profile's slug).

Press **Test**. If it works, Seerr fills in the dropdowns below it. Then:

- **Quality Profile**: the list holds just one entry, the profile this
  address stands for. Select it.
- **Root Folder**: the movie folders Reelkeeper files this profile's movies in.
  If you gave a media folder this profile under Settings → Media folders, only
  those folders are listed; otherwise all your movie folders are.
- **Minimum Availability**, **Tags**, **Enable Scan** and
  **Enable Automatic Search**: optional, as you prefer.

Tick **Default Server** if this should take ordinary requests. Press Add
Server.

## Adding a TV server

The same, with Add Sonarr Server, and `/compat/sonarr/hd` (or another TV
profile's slug) as the URL base. A request in Seerr makes Reelkeeper watch
exactly the seasons that were requested, plus new episodes as they air.
Removing a series in Seerr never deletes files in Reelkeeper.

## What a request gets

A request from Seerr is searched for straight away, not at the next scheduled
pass. If a release is found, it is grabbed even when the queue is full (it
still takes a place in the queue, and the free-space check still applies). It is tagged `requested` in qBittorrent and moved to the top of
qBittorrent's queue, when queueing is turned on there.

## Why one server per profile

Reelkeeper can keep several copies of one title: for instance a 1080p copy and
a remux copy, each from its own profile and media folder. Seerr tracks what
each server has. It also skips the add if the server says the movie already
has a file.

If every profile shared one address, a request for the remux copy of a movie
you already own in 1080p would look "finished" and nothing would ever
download. Giving each profile its own address means each one only reports its
own copy: `hasFile`, monitored state, size, queue and history all describe
that profile's version alone.

A typical setup for a household that keeps both a normal and a 4K/remux
library:

| In Seerr | Address | 4K Server box |
| --- | --- | --- |
| Reelkeeper (HD), default movie server | `/compat/radarr/hd` | off |
| Reelkeeper (Remux), the 4K movie server | `/compat/radarr/remux` | on |
| Reelkeeper (HD), TV server | `/compat/sonarr/hd` | off |

Seerr's **4K Server** option marks the server that handles 4K requests, so
people can ask for the same movie in regular and 4K quality and each request
is tracked separately. Point it at your Remux (or 4K) profile.

## If Test fails

- **Unauthorized (401):** the API key is wrong. Copy it again from
  Settings → Security. Generating a new key stops the old one working at once,
  so update Seerr if you do.
- **Not found (404):** the URL base is wrong, or the profile does not apply to
  that kind of server (a movie-only profile used for Sonarr, or the reverse).
  Check the slug and that you chose Radarr for movies and Sonarr for TV.
- **Cannot connect:** Seerr cannot reach that host and port. Try the address
  from the machine Seerr runs on.
- Behind HTTPS and a reverse proxy, use the public name with Use SSL on, not
  port 7676.

A wrong API key is always refused, even from an address on the
[bypass list](sign-in.md#the-bypass-list), so a mistake shows up straight
away instead of working by accident.
