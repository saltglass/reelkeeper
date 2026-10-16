# Migrations

One folder per database engine. Every schema change adds a file to **both**
folders with the same version number and name, for example
`00002_users.sql`. `TestMigrationsMatch` fails if they drift apart.

Migrations run automatically at startup (`aerie serve`) or with
`aerie migrate`. On SQLite, Aerie copies the database into
`<data dir>/backups/` before applying new migrations.

Never edit a migration that has been released; add a new one instead.
