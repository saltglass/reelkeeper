#!/bin/sh
# One database per Reelkeeper instance (lab only).
set -e
for db in rk_pg rk_fresh_pg rk_old081_pg rk_old084_pg; do
  psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres -c "CREATE DATABASE $db"
done
