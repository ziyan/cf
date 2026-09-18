# cf

Keep a local copy of a Confluence site, and search it without going back to the
server.

A sync is incremental: the first run reads the site in full, later runs ask only
for what changed since, so re-running is cheap and safe.

```bash
cf archive sync ~/confluence-archive              # fetch what changed
cf archive sync ~/confluence-archive --space ENG  # just the spaces whose key matches
cf archive sync ~/confluence-archive --full       # ignore the marks, read everything again
cf archive sync ~/confluence-archive --skip-comments

cf archive tree ~/confluence-archive              # write an index per space
cf archive labels ~/confluence-archive            # record which labels a page carries
cf archive prune ~/confluence-archive             # what the site no longer has
cf archive prune ~/confluence-archive --remove

cf archive status ~/confluence-archive            # what the archive holds
cf archive search ~/confluence-archive "deadlock" # search it offline
cf archive search ~/confluence-archive "e-stop" -s ENG -n 20
cf archive search ~/confluence-archive "c.t sat" --regex
cf archive search ~/confluence-archive "question" --comments
```

## What it stores

```
spaces.json                          every space seen
users.json                           account id to name, so a reader sees people
state.json                           what is archived and at which version
pages/<space>/_index.md              the space as a tree, written by cf archive tree
pages/<space>/<id>-<slug>.md         the page as markdown, with front matter
pages/<space>/<id>-<slug>.xhtml      the storage format the server sent
blogposts/<space>/<id>-<slug>.md     blog posts, which live behind their own endpoint
comments/<space>/<id>-<slug>.md      the comments on that page, both kinds
labels.json                          which labels each page carries
attachments.jsonl                    one record per attachment, not fetched
attachments/<id>__<name>             only what somebody asked for
```

Markdown is for reading and grepping. The storage beside it is what the server
actually holds: macros, layouts and embedded content do not survive the
conversion, so the archive keeps both rather than choosing. A page's front
matter carries its id, title, space, version, author and modification time.

Authors and mentions are people, not identifiers. A sync asks the site who an
account belongs to once and keeps the answer in `users.json`, so `@62e393ee...`
reads as the person who was actually mentioned.

## Attachments

A sync records pages and comments, never files. A large site holds attachments
by the million, which is not something to fetch by accident, so attachments are
two deliberate steps:

```bash
cf archive attachments index ~/confluence-archive --space ENG
cf archive attachments download ~/confluence-archive --space ENG --dry-run
cf archive attachments download ~/confluence-archive --space ENG --type image/ --max-mb 5
```

Index asks the archived pages what they hold. Download takes filters for space,
media type, size and count, and skips what is already on disk, so it can be run
again to widen the net.

## How the incremental sync works

Pages are walked newest modification first, by cursor rather than by offset. An
offset walk gets slower the deeper it goes, and a page written while the walk is
in progress shifts the window and is missed. The walk stops at the mark recorded
by the last run, and a page at or after the mark is checked against the version
recorded for it, so a run cut short writes nothing twice.

Comments are walked apart from pages, because writing a comment does not change
the page it is on: a sync that followed only page modification dates would never
see a conversation that happened after the last edit. A first run enumerates
every comment of both kinds; later runs ask which pages have had a comment
change and rewrite just those.

A sync narrowed to a space records its own mark for that space and leaves the
whole-site mark alone. A mark saying the site is current when only one space was
read would make the next whole-site sync stop early and never archive the rest.

## Two things worth knowing

Asking the comments endpoints to sort by modification date makes them stop after
about a thousand records and report themselves finished. On the site this was
written against, it stopped at a small fraction of them. The walk is
deliberately unsorted for that reason.

Deleted pages are not detected. The walk enumerates what is there, so a page
removed from the site stays in the archive until somebody clears it out. That is
usually what an archive is for.

## What a sync does not do

A sync only ever adds. A page deleted on the site, or moved to another space,
stays in the archive, so an archive that is years old quietly stops matching
what it is a copy of. `cf archive prune` reads every page id the site still has,
which is cheap because it asks for no bodies, and says what the archive holds
beyond that. It removes nothing without `--remove`, and refuses to act at all
if the site reports no pages, since that is a failed walk rather than an empty
site.

Labels are read label by label rather than page by page, because there are a
few thousand labels against a hundred thousand pages. `cf archive labels` writes
`labels.json`, and `cf archive tree` shows them beside each page in the index.

## Configuration

```bash
cf auth login --domain example.atlassian.net --email you@example.com
cf auth list
cf auth use <name>
cf auth remove <name>
```

An API token comes from id.atlassian.com, under Security, API tokens. It is not
a password and can be revoked on its own. The token is read without echoing, or
from `CF_TOKEN` when there is no terminal to ask, and the credentials are
checked against the site before they are written to `~/.config/cf/config.json`
with owner-only permissions.

## License

MIT. See [LICENSE](LICENSE).
