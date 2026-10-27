# First-time setup

This page walks through the screens to visit after
[installing](install.md), in a sensible order. Each step says what the screen
is for and what to choose first. Everything here can be changed later.

Reelkeeper starts out cautious. **Nothing is downloaded and nothing is
deleted until you switch it on**, so you can look around safely.

## 1. Create your account

Open Reelkeeper in your browser (for example `http://your-server:7676`). The
first visit shows "Welcome to Reelkeeper" and asks you to create the
administrator account: a username, and a password of at least 8 characters.
You are signed in straight away and land on **Home**.

Until a few basics are in place, Home shows a short checklist: *Add a media
folder*, *Add an indexer* and *Set up a download client*. Each line links to
its page and gets ticked when it's done, so you can always go back to Home to
see what's left. The steps below cover those and the rest.

Only the first visit can create an account. Later you can add
[single sign-on](sign-in.md) if you want it.

## 2. Add your TMDB key (Settings → General)

Reelkeeper identifies movies and TV shows through The Movie Database (TMDB).
Open Settings → General, find "Movie information (TMDB)", paste your API key
and press Check key, then Save. You can get a free key at themoviedb.org
(account Settings → API).

Without a key Reelkeeper cannot identify anything new. This is the one step
you cannot skip.

## 3. Quality profiles (Settings → Quality profiles)

A quality profile is a kind of copy: "HD", "Remux", "4K". It says which
resolutions and sources are acceptable, how big files may be, and when to stop
upgrading. A single movie can have several copies, one per profile, each
searched and upgraded on its own.

Reelkeeper starts with two profiles, **HD** (movies and TV) and **Remux**
(movies). That is enough to begin. To add another, choose Add a profile, name
it, and pick a starting point:

- **HD**: 1080p from web or Blu-ray, upgraded to a Blu-ray encode when one
  appears.
- **Remux**: untouched Blu-ray video and audio, 1080p or 4K. Large files.
- **4K**: 2160p from web or Blu-ray, preferring HDR and Dolby Vision.
- **Compact**: 720p or 1080p kept small, good for kids' movies and phones.

Open Edit settings on a profile to change anything. Every profile page has a
"Test a release name" box that shows whether a release would be accepted and
why. Under Show advanced you can edit the raw rules (custom formats).

Each profile has a short internal name (its "slug", such as `hd` or `remux`),
shown under the profile's title. You need it when
[connecting Seerr](seerr.md). It does not change if you rename the profile.

## 4. Media folders (Settings → Media folders)

Tell Reelkeeper where your library lives. Choose Add a media folder, then:

1. Pick what is in it: **Movies** or **TV shows**. One folder holds one or
   the other.
2. Pick the folder, as Reelkeeper sees it inside its container (for example
   `/data/media/movies`). The folder browser lets you click through.
3. Pick which profile the folder's titles belong to. A folder of normal movies
   gets HD; a folder of remuxes gets Remux.
4. Choose Add and scan.

Reelkeeper reads the folder, identifies what is in it (by the folder and file
names, then TMDB) and checks again every 6 hours. Anything it is not sure
about goes to a review screen instead of being guessed.

**Reelkeeper manages files here.** Each folder has this switch, and new
folders start with it **off**. While it is off, Reelkeeper only reads the
folder and never renames, adds or removes anything in it. Reelkeeper also will
not grab for that folder, because it could not import the result.

Leave it off while you look around and check that the scan found the right
things. Turn it on when you want Reelkeeper to import new downloads there and
rename files. If another app (Radarr, Sonarr) still manages the same folder,
keep it off until that app is retired, or the two will fight over the files.

**Upgrade movies found here.** Movie folders have a second switch, and new
ones start with it **off**. Off, Reelkeeper catalogs the movies it finds and
keeps their files exactly as they are; it never goes looking for better copies,
so adding a big library does not start hundreds of upgrade searches. Movies
Reelkeeper downloads later are still upgraded as their quality profile says.
Turn the switch on and Reelkeeper looks for better copies of every movie in
the folder, at its usual pace (three searches an hour). Each movie also has
its own Upgrades switch on its page, so you can keep one file or upgrade just
one movie. A movie whose file goes missing is still downloaded again, whatever
the switch says. TV folders have no such switch.

The Media folders page also has buttons to import from Radarr and Sonarr, and
the file naming settings. They are optional.

## 5. Indexers (Settings → Indexers)

Indexers are where Reelkeeper searches for releases. There is no search page:
Reelkeeper searches by itself when something is wanted.

Choose Add indexer and pick one of:

- **Tracker**: a site from Reelkeeper's built-in list of hundreds, public or
  private. Private ones ask for your login or API key. The list is downloaded
  from the internet, so Reelkeeper needs internet access to show it.
- **Torznab or Newznab feed**: an address plus an API key, for example from
  Jackett. A Newznab feed is a Usenet indexer: from 2.0 it is searched once a
  Usenet client is set up (see
  [Usenet: SABnzbd or NZBGet](#usenet-sabnzbd-or-nzbget-from-20)).
  Before 2.0, a Newznab feed will not produce downloads.
- **Knaben**: a public search engine covering many torrent sites. No settings.

For private trackers, fill in the "minimum ratio" and "minimum seed time" the
site asks for. Cleanup reads them so it never removes a torrent too early.

Press Test before saving. A healthy indexer shows "Working" on its card.

**Cloudflare solver.** Some sites sit behind Cloudflare's "checking your
browser" page, which Reelkeeper cannot pass on its own. If you run a
FlareSolverr-compatible solver (such as FlareSolverr or TRAWL), enter its
address once in the Cloudflare solver card at the bottom of this page (for
example `http://solver:8191`) and press Check. Then switch on "Use the
Cloudflare solver" only for the sites that need it. If none do, leave it
empty.

Coming from Prowlarr? The Import from Prowlarr button brings your indexers
over.

## 6. Download client (Settings → Download client)

Choose your download client at the top of the card: **qBittorrent**,
**Transmission** (see [Using Transmission](#using-transmission) below),
**Deluge** (see [Using Deluge](#using-deluge)) or Reelkeeper's own
**Built-in** client (experimental; see [Using the built-in
client](#using-the-built-in-client)). Then,
for qBittorrent, enter the Web UI address (with the port, such as
`http://qbittorrent:8080`), the username and the password, and press Test.
It should answer "Connected to qBittorrent" with the version number.

- **Download folder** is where qBittorrent saves Reelkeeper's downloads, as
  qBittorrent sees it, such as `/data/downloads/reelkeeper`. A folder of its
  own lets Reelkeeper spot stray files. This must be a path that also exists
  inside the Reelkeeper container; see
  [Folders and paths](install.md#folders-and-paths-read-this-first).
- Reelkeeper uses its own categories in qBittorrent, `reelkeeper-movies` and
  `reelkeeper-tv`, and never touches other torrents.

In the **Automatic grabbing** card, movies and TV each have three modes:

- **Off**: Reelkeeper does not search on its own.
- **Dry run**: Reelkeeper searches and records in Activity → History what it
  *would* grab, and downloads nothing. **This is where it starts.**
- **On**: Reelkeeper sends the releases it picks to qBittorrent.

Why start in dry run? It lets you see the choices Reelkeeper makes with your
profiles and indexers before anything is downloaded. If the History shows
sensible picks for a few days, switch Movies to On. TV has its own switch, so
you can enable it separately. "On" only becomes available once the qBittorrent
connection is saved.

Below the switches is the **queue limit**: the most downloads Activity → Queue
may hold (movies and TV together; a season pack counts once). While the queue
is that full, Reelkeeper grabs nothing new and says so on the Queue page and
in the sidebar; it carries on as downloads finish. Requests from Seerr still go
through. A small limit such as 10 is a good safety net the first week; 0 means
no limit.

On Activity → Queue, each download that is still downloading has **Pause**
(and **Resume** while it is paused) and **Move to top**, next to Remove. Pause
stops the torrent in qBittorrent without removing anything, and cleanup never
counts a paused download as stuck. Move to top puts it first in qBittorrent's
download queue, which only matters while qBittorrent limits how many torrents
download at once. A paused download still counts toward the queue limit, and
each action is recorded in History.

### Using Transmission

Choose **Transmission** at the top of the card to use it instead of
qBittorrent. Everything else Reelkeeper does works the same.

- **Version 4.0 or newer.** Reelkeeper keeps its own downloads apart with
  Transmission's *labels*, which 4.0 added. Test refuses an older Transmission
  and says so.
- **RPC address.** Enter Transmission's RPC address with the port, such as
  `http://transmission:9091` (Reelkeeper adds `/transmission/rpc` itself). The
  username and password are only needed if you turned on the RPC login; leave
  them empty if you didn't. If Test says Transmission refused Reelkeeper's
  address, add it to Transmission's `rpc-whitelist`.
- **Labels instead of categories.** Reelkeeper's downloads carry the labels
  `reelkeeper-movies` and `reelkeeper-tv`, and Reelkeeper never touches a
  torrent without one of them.
- **Same paths as Reelkeeper.** As with qBittorrent, the download folder must
  exist at the same path inside Reelkeeper and Transmission (see
  [Folders and paths](install.md#folders-and-paths-read-this-first)). If
  Transmission keeps unfinished downloads in an "incomplete" folder, that is
  fine: Reelkeeper only imports once Transmission has moved the finished files
  into the download folder.
- **Move to top** uses Transmission's own download queue. It only matters when
  Transmission limits how many torrents download at once (its "download queue
  size" setting).
- **Seeding limits.** Reelkeeper turns off Transmission's ratio and idle limits
  on its own torrents, one by one, so that Reelkeeper's cleanup decides when
  they stop. Your other torrents keep their limits.
- Transmission calls a download "stalled" only after it has been idle for its
  "queue-stalled-minutes" (30 by default), so Reelkeeper's stuck-download rules
  notice a stalled Transmission download a little later than a qBittorrent one.

### Using Deluge

Choose **Deluge** at the top of the card to use it instead. Everything else
Reelkeeper does works the same.

- **Version 2.1 or newer** (Deluge 2.x). Test refuses an older Deluge and says
  so. Deluge 1.x isn't supported.
- **The Label plugin must be on.** Reelkeeper keeps its own downloads apart
  with Deluge's *labels*, which come from the Label plugin. Turn it on in
  Deluge under Preferences → Plugins → Label. Test refuses a Deluge without it
  and says so.
- **Web UI address and password only.** Enter the address of Deluge's *web UI*
  with the port, such as `http://deluge:8112` (Reelkeeper adds `/json` itself),
  and the web UI's password. Deluge's web UI has no username, so there isn't
  one to enter. Reelkeeper talks to the web UI (`deluge-web`), which talks to
  the Deluge daemon; it does not use the daemon's own port (58846).
- **The web UI must be connected to its daemon.** If it isn't, and only one
  daemon is configured (the usual case), Reelkeeper connects it for you. If
  Test says "Deluge's web UI isn't connected to a daemon: open it and connect
  once", open Deluge's web UI in a browser and choose the daemon once (or
  remove the extra daemons from its Connection Manager).
- **Labels instead of categories.** Reelkeeper's downloads carry the labels
  `reelkeeper-movies` and `reelkeeper-tv` (one label per torrent; Deluge has no
  tags), and Reelkeeper never touches a torrent without one of them.
- **Same paths as Reelkeeper.** As with the other clients, the download folder
  must exist at the same path inside Reelkeeper and Deluge (see
  [Folders and paths](install.md#folders-and-paths-read-this-first)). Deluge
  keeps unfinished downloads under their plain names in the download folder
  itself, so Reelkeeper imports a torrent as soon as it is finished.
- **Seeding limits and "move completed."** A new torrent in Deluge copies your
  global settings, so Reelkeeper turns off Deluge's "stop at ratio", "remove at
  ratio" and "move completed" on its own torrents, one by one, and checks that
  Deluge took it. Your other torrents keep their settings. Files then stay
  where Reelkeeper expects them, and Reelkeeper's cleanup decides when a
  torrent stops. (Deluge's global seed-time and ratio settings don't stop
  anything on their own.)
- **Move to top** uses Deluge's own queue, which only matters while it limits
  how many torrents are active (3 downloading and 5 seeding by default).
  Reelkeeper leaves those queue settings alone; a torrent waiting in the queue
  shows as "Queued", and the seeding goal counts from when the download
  finished, so a seed waiting its turn doesn't hold cleanup up.

### Using the built-in client

Choose **Built-in** (it carries an *Experimental* badge) to let Reelkeeper
download torrents itself, with no other program. Everything else Reelkeeper
does works the same. It is new: it works, but for torrents you care about, an
external client is still the safer choice. Without a VPN, peers see your real
IP address, and the card at the top says so in red.

- **No address and no login.** It is set up by its **Download folder**, a path
  inside the Reelkeeper container that exists and can be written to (Test and
  Save check). Press Test to check the folder and that the port can be
  opened; there is nothing to connect to.
- **Listen port** (6881). Peers connect here. Publish the port from the
  container, TCP and UDP (`6881:6881/tcp` and `6881:6881/udp`), or Reelkeeper
  can still download but finds fewer peers. It never asks your router to open
  it. See [The built-in download client](install.md#the-built-in-download-client).
- **VPN (WireGuard)**, optional. Drop, upload or paste the WireGuard config
  file your VPN provider gives you; Reelkeeper shows what is in it and keeps
  it encrypted. Then everything the built-in client does goes through the VPN,
  a green card says which address peers see, and if the VPN drops, all
  downloads stop until it is back. Removing it asks first and holds the
  downloads until you press **Start without VPN**. Enter the provider's
  **Forwarded port** if you have one (it replaces the listen port). Details:
  [A VPN for the built-in client](install.md#a-vpn-for-the-built-in-client-wireguard).
- **Max active downloads** (3) and **Max active seeding torrents** (25, 0 for no
  limit). Torrents beyond them wait in order and show as *Queued* (downloads)
  or keep their "seeding" state without uploading (finished ones waiting for a
  slot). Move to top moves a torrent to the front of either line.
- **Speed limits** (KB/s, 0 for none) are under *Show advanced* and apply at
  once.
- It remembers its torrents in the data folder (`builtin/`), which backups do
  not include, and picks them up again after a restart without checking the
  finished ones again.
- It uses the DHT and peer exchange only for public torrents. For a torrent
  from a tracker that asks for it (a *private* torrent), it talks only to the
  peers the tracker gives it.
- Reelkeeper reads what the trackers say, so cleanup's "unregistered torrent"
  rule works with it as with the others.
- Switching to or from it moves the torrents like any other switch, below.
  Torrents left in the built-in client stop seeding when you switch away (it
  stops with the setting), so the dialog does not offer to leave them there.

**Switching between qBittorrent, Transmission, Deluge and the built-in client.** Choose the other
client, enter its address and login (or, for the built-in client, its folder), and press Save. If Reelkeeper has
torrents, it asks first: "Move 12 torrents to Transmission? 3 are still downloading. Files
already downloaded are checked, not downloaded again; nothing is deleted."
**Move them** is the default; Cancel changes nothing.

- **What moves.** Everything in Activity → Queue (still downloading, or
  finished and waiting to be imported) always moves, and so does everything
  still seeding, unless you tick **Leave the seeding ones in qBittorrent**.
  Those finish seeding there, so a private tracker's share rules aren't broken
  by accident; Reelkeeper stops tracking them, History records each, and
  nothing is deleted. (Leaving one in the Queue behind would make Reelkeeper
  search for it again while the old client kept downloading it, so those
  can't be left.)
- **How.** Reelkeeper stops the torrent in the old client, hands the new client
  the same torrent (Reelkeeper keeps the `.torrent` file or magnet link it sent
  when it grabbed a release, encrypted, only while the torrent is active), and
  the new client *checks* the files already on disk against the torrent's
  pieces. Nothing is downloaded again. Once the check finds at least what the
  old client had, the torrent is removed from the old client **without
  deleting any file**. Unfinished files that carry a client's own ending
  (Transmission's `.part`, qBittorrent's `.!qB`) are renamed by Reelkeeper to
  the plain name, so the new client finds them. (Deluge uses plain names, so
  moving out of Deluge renames nothing.) A Seerr request still goes to the top
  of the new client's queue, whichever client it comes from.
- **While it runs.** The move is a task under System → Tasks, with progress.
  Activity → Queue shows "Moving to Transmission" on each row, and History
  gets one entry per torrent. Grabbing, imports and cleanup wait until it's
  done. If Reelkeeper restarts, it carries on where each torrent left off. The
  old client's address and login are kept until the move ends, then forgotten.
- **Seeding time and ratio carry over.** Neither client lets anyone set a
  torrent's seeding time or upload counters, so Reelkeeper remembers what the
  old client had and adds it to what the new one reports. Your seeding goal and
  the tracker's minimum are neither started over nor cut short.
- **If a torrent can't move,** nothing is lost: it is removed from the new client
  (files left alone), every file gets back the name and folder it had, and the
  old client checks it again. A torrent that was seeding keeps seeding in the old
  client, which Reelkeeper no longer tracks. One that was downloading stays
  *stopped* in the old client, and Reelkeeper searches for it again and grabs it in
  the new one. History says why.
- **Limits.** Both clients must see the files at the same paths, as Reelkeeper
  already requires (see [Folders and paths](install.md#folders-and-paths-read-this-first)).
  Files in a folder the new client can't see (for example an "incomplete"
  folder on a volume only the old client has) can't be checked there, so that
  torrent stays where it is. Moving files to a different folder isn't part of
  this. Reelkeeper keeps its own copy of each torrent it grabs (the
  `.torrent` file or magnet link) while the torrent is in the client, so the
  new client always has what it needs. The dialog lists any torrent that
  can't move.
- The stored password isn't carried over to the other client, so enter the
  new one.

### Usenet: SABnzbd or NZBGet (from 2.0)

Usenet is optional. If you have a Usenet provider and a Usenet (Newznab)
indexer, set up **SABnzbd or NZBGet** (choose one at the top of its card,
below the torrent client): either client can be set up alone, or next to the
torrent client. Press Test when you've filled it in; it should answer
"Connected to" the client with its version.

- **SABnzbd**: its address with the port, such as `http://sabnzbd:8080`, and
  its API key (in SABnzbd: Config → General → Security). Version 4.0 or
  newer.
- **NZBGet**: its address with the port, such as `http://nzbget:6789`, and
  its username and password (in NZBGet: Settings → Security, ControlUsername
  and ControlPassword; use the control login, not the restricted one). Version
  21.0 or newer. Both the original NZBGet and the maintained nzbget.com
  fork work.
- **An older version** is refused by Test, which says so.
- **Its own categories.** Downloads go in `reelkeeper-movies` and
  `reelkeeper-tv`, and Reelkeeper never touches other jobs. SABnzbd gets
  those two categories created by Reelkeeper, each with a folder of the same
  name inside its completed folder. NZBGet needs nothing set up: it files
  them in a folder of that name inside its DestDir, and Reelkeeper never
  changes NZBGet's settings. (If you made a category of that name in NZBGet
  with a folder of its own that isn't named the same, Test refuses it and
  says what to change.)
- **The same paths.** SABnzbd's completed folder (Config → Folders), or
  NZBGet's DestDir (Settings → Paths), must exist at the same path inside
  Reelkeeper, as for the torrent client (see
  [Folders and paths](install.md#folders-and-paths-read-this-first)).
- **Settings Test checks.**
  - SABnzbd: with its duplicate detection on, a release Reelkeeper grabs
    again would be refused, so Test warns about it: turn it off (Config →
    Switches); Reelkeeper already avoids grabbing the same release twice. If
    it is set to forget finished jobs at once (History Retention),
    Reelkeeper would never see them finish, so Test refuses until you choose
    to keep them (any number of days or jobs is fine).
  - NZBGet: the same for KeepHistory (Settings → Download Queue; 0 keeps
    no history, so Test refuses it; the default is 30 days) and for
    AppendCategoryDir (Settings → Paths; it must stay on, since Reelkeeper
    only cleans up inside a folder named after its category). It warns if
    NZBGet is paused, or set to pause damaged downloads (HealthCheck) instead
    of failing them. Its duplicate check can stay on: Reelkeeper adds each job
    with it off.
- **Which release wins.** Your quality profile decides as always. Only
  between two equally good releases (same quality and score), one on Usenet
  and one a torrent, does Usenet win: it is usually faster and leaves nothing
  to seed. History says when that happened. Minimum seeders doesn't apply to
  Usenet releases.
- **Indexers.** Usenet indexers are searched only while a Usenet client is
  set up, and torrent indexers only while a torrent client is (with neither,
  torrent indexers still are, so dry run shows what would be grabbed).
- **In the Queue**, Usenet downloads show the client's own steps: queued,
  downloading, checking and repairing files (when a few pieces were missing),
  unpacking, then waiting to import. Pause, Resume and Move to top work while
  a download is still downloading.
- **After the import** there is nothing to seed, so cleanup removes the job
  from the client and deletes its folder (only that job's folder, inside
  Reelkeeper's category folder). **A download the client gives up on** (too
  many missing pieces, a failed repair or unpack) is removed, blocklisted and
  searched for again, with the client's reason. Both follow cleanup's mode, so
  in dry run they are only listed.
- **Free space.** A Usenet download needs about twice its size while
  the client unpacks it, and Reelkeeper allows for that against "Keep at least
  N GB free", using the free space the client reports.
- Usenet downloads never move between clients: switching the torrent client
  leaves them alone, and switching from SABnzbd to NZBGet doesn't carry the
  old downloads over (finish or remove them first).

## 7. Cleanup (Settings → Cleanup)

Cleanup tidies qBittorrent after Reelkeeper: it removes finished torrents once
they have seeded enough, and removes stuck downloads (no progress, too slow,
deleted on the tracker, files missing) and asks for a different release. It
only ever touches torrents in Reelkeeper's own categories.

From 2.0 it also tidies SABnzbd or NZBGet: Usenet downloads are removed
straight after their import, and ones the client gave up on are replaced (see
[Usenet: SABnzbd or NZBGet](#usenet-sabnzbd-or-nzbget-from-20)).

It starts in **Dry run**, like grabbing: Activity → Seeding & cleanup lists
what Reelkeeper *would* remove and why, and nothing is removed. Watch it for a
while, then switch it to On. The defaults are cautious. After an import, a
torrent seeds until a ratio of 1.0 or 24 hours, whichever comes first, and
never less than the tracker's own minimum.

**Download disk.** *Keep at least (GB) free* (100 by default) stops the
download disk from filling up. Set it to a bit more than your largest
downloads, or 0 to turn it off. Reelkeeper counts what downloads in progress
still need, and:

- skips a grab that wouldn't leave that much free. History says why, you get
  one email, and the title is tried again once there is room. It doesn't wait
  for its next scheduled search.
- pauses grabbing and the hourly search while the disk is short, and says so
  on Home. They carry on by themselves once there is room again.
- removes finished torrents early while the disk is short (longest finished
  first, never before the tracker's own minimum), once cleanup is On.

The free space is measured at the download folder as Reelkeeper sees it, so
that folder must be the real download disk (see
[Folders and paths](install.md#folders-and-paths-read-this-first)).

Files replaced by an upgrade are not deleted at once. They go into a hidden
`.reelkeeper-replaced` folder inside the media folder and are emptied after 30
days (0 keeps them forever).

## 8. Media servers (Settings → Media servers)

All optional. Reelkeeper can tell your media server about each change, so
it appears there without a full library scan:

- **Emby**: enter Emby's address and an API key (Emby: Dashboard →
  Advanced → API Keys). If Emby sees the same folders at different paths than
  Reelkeeper does, fill in "Reelkeeper's path" and "Emby's path" to translate
  between them.
- **Jellyfin** (from 2.0): the same, for Jellyfin. Enter its address and an
  API key (Jellyfin: Dashboard → API Keys), and its path if it sees your
  media under a different one, such as `/data/media` in its own container.
  Emby and Jellyfin can both be on, if you run both. A new movie shows up in
  Jellyfin after about a minute, but a renamed one can take a few minutes:
  Jellyfin drops the old name after a minute, then rechecks the whole
  library folder before the new name appears.
- **Plex** (from 2.0): the same, for Plex. Enter its address (port 32400)
  and your Plex token: in Plex Web, open any movie's ⋯ menu → Get Info →
  View XML, and copy what follows `X-Plex-Token=` in the new tab's address.
  Plex scans only the changed folder, in the Plex library that holds it, so
  each media folder must be in a Plex library; the test button lists your
  libraries and any media folder none of them holds. A deleted or renamed
  movie leaves Plex only while its "Empty trash automatically after every
  scan" setting (Settings → Library) is on, which it is by default.

Each has a Test button.

## 9. Notifications (Settings → Notifications)

All optional. Reelkeeper can send:

- **Email**: problems only (failed imports, stuck downloads it removed, an
  indexer failing for a day, qBittorrent unreachable). Enter your mail
  server's details and press the test button.
- **Webhook**: every event as JSON to an address of your choice, for other
  tools. An optional secret is sent in the `X-Reelkeeper-Secret` header.

Each has a Test button.

## 10. Add your movies and shows

Open Movies or Series in the sidebar. Whatever Reelkeeper found by scanning
your folders is already there. To have it look for something new, add it with
the Add movie or Add series button, or connect [Seerr](seerr.md) and request it there.

To change many titles at once, press **Select** on Movies or Series, tick
posters or rows (or *Select all N shown* to take everything the current
filter shows), and use the bar at the bottom:

- **Monitoring**: start or stop looking after them.
- **Upgrades** (movies): turn upgrades on or off, for example to keep the
  files a scanned library already has.
- **Episodes** (shows): monitor all episodes, future ones only, or none.

Reelkeeper asks before it changes anything. When the selected titles have
copies in more than one quality profile, that dialog also asks which
profile's copies to change. Titles that become
wanted are searched straight away, up to 10 of them; the rest wait for the
hourly search, so a big change doesn't flood your indexers.

The **Wanted** page lists everything Reelkeeper is still looking for, and why
it hasn't been found yet. Tick rows to **Search** for them now (the first 10
of a bigger selection; the rest wait for the hourly search) or to **Stop
monitoring** them.

## 11. Day to day: Home

Home is where Reelkeeper opens. At the top it says whether anything needs
you: grabbing switched off or in dry run, a download client it can't reach,
a full download disk or queue, downloads that couldn't be imported, folders
or shows waiting for you to identify them, a failing indexer, or (with the
built-in client) a VPN that is down. Each line links to where it's fixed.
When nothing needs you, it says "All good." Below that are a few numbers, what is
downloading, what was just grabbed or imported, and what's due this week.

## Next

- [Connect Seerr](seerr.md)
- [Sign-in options and recovery](sign-in.md)
- [Backups](backups.md). Worth setting up before you rely on Reelkeeper.
