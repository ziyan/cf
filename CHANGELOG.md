# Changelog

All notable changes to cf will be documented in this file.

The format is based loosely on Keep a Changelog.

## [Unreleased]

### Added

- `cf archive sync <dir>` keeps a local copy of a Confluence site, incrementally. Pages are stored as markdown with front matter, and as the storage format the server sent, so nothing is lost to the conversion. Comments of both kinds are archived beside the page they belong to.
- `cf archive search <dir> <query>` searches the archive offline, with space, regex and comment filters.
- `cf archive status <dir>` says what the archive holds.
- `cf auth login`, `list`, `use` and `remove` manage the sites this talks to. The token is read without echoing and checked against the site before it is written.
- Blog posts are archived alongside pages, into `blogposts/<space>/`. They live behind their own endpoint, so a walk over pages never sees one.
- `cf archive prune` says which archived pages the site no longer has, and removes them with `--remove`.
- `cf archive tree` writes an index per space, nesting page titles the way the site nests them.
- `cf archive labels` records which labels each page carries, read label by label rather than page by page.
- `--workers` keeps several requests in flight where a command makes one per page.

- `cf archive attachments index` and `download` record what is attached and fetch the files a filter picks out. A sync never fetches files: a large site holds attachments by the million.
