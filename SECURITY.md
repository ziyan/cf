# Reporting a security problem

Please do not open a public issue for anything exploitable. Report it through
GitHub's private vulnerability reporting, on the Security tab of this
repository.

## What is in scope

This program, as a user runs it against a Confluence site.

- **The API token.** It is read from the configuration file and sent as an
  `Authorization` header. It must not reach a log line, an error message, or a
  request to another host.
- **The archive directory.** Pages are written to paths built from data the
  server supplies: space keys, titles, attachment names. A site that can make
  `cf` write outside the archive directory is a bug, as is one page overwriting
  another's file.
- **Anything the server sends being treated as more than data.** Page bodies,
  titles and comments all arrive from other people, and the markdown conversion
  runs over them.

## What is not

- Confluence itself, and what a token is allowed to read. The server decides
  that.
- The archive's own permissions. It holds everything the account could read, in
  plain files, and is as private as the directory you put it in.
