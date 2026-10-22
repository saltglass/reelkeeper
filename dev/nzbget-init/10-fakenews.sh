#!/bin/sh
# Runs inside the dev stack's NZBGet at every start, after the image's own
# setup has written /config/nzbget.conf (linuxserver custom-cont-init.d).
# NZBGet's default config names a real news server; the dev stack must never
# reach the internet, so fakenews (dev/fakenews) becomes its only server
# before NZBGet starts, whatever the volume held.
conf=/config/nzbget.conf
[ -f "$conf" ] || exit 0
sed -i -E '/^Server[0-9]+\./d' "$conf"
cat >> "$conf" <<'CONF'
Server1.Active=yes
Server1.Name=fakenews
Server1.Level=0
Server1.Host=fakenews
Server1.Port=119
Server1.Username=dev
Server1.Password=dev
Server1.Encryption=no
Server1.Connections=4
CONF
echo "nzbget: news server set to fakenews only"
