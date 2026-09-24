---
status: accepted-for-prototype
---

# Store attachment bytes as server-owned artifacts bound at Send

Pasted images and copied files need bytes that do not fit the inline 64 KiB text
captures carried in snapshots. The Go server stores them as auxiliary files in
the application home, `<home>/artifacts/<id>`, with metadata in a SQLite
`artifacts` table added by schema version 2 (migrated behind the existing synced
pre-migration backup). A file is written to a temporary name, synced and renamed
before its row commits; only a committed row is returned to a client or
referenced by a command. A startup and periodic sweep deletes temporary files and
files without rows and marks rows whose file is missing unavailable, so recovered
state never presents absent content as empty.

Clients stage bytes with an authenticated raw `POST /v1/artifacts`. The server
bounds one artifact at 16 MiB and staged bytes at 256 MiB, sniffs the media type
(`http.DetectContentType`; `image.DecodeConfig` for PNG, JPEG and GIF), rejects a
claimed or detected image it cannot decode and images over 40 megapixels, and
keeps a text claim only for verified UTF-8 without NUL. Image validation reads
only the header: dimensions are checked, but the image is not fully decoded,
which saves memory. A corrupt image body can therefore still be refused by a
provider after dispatch. At most two uploads run at once (others get `busy`),
each body has a read deadline, and temporary files are written and synced
outside the publication lock. `GET /v1/artifacts/{id}` serves the stored type
with `nosniff` and a digest header that the client verifies. `DELETE
/v1/artifacts/{id}` removes a staged draft attachment, freeing quota. It refuses
accepted artifacts. An unavailable flag clears when the file reappears with the
recorded size, and reads still verify the digest. Snapshots and views carry only artifact
metadata on `protocol.Attachment`, never bytes, except that text artifacts
within the existing 64 KiB capture rule also fill `Content` so earlier agent and
viewer paths work unchanged.

A Send (`thread.start`, `prompt.send`, `prompt.reopen-send`) replaces
client-supplied metadata with the stored record and binds each staged artifact
to its thread in the same transaction as command acceptance. A missing, expired,
unavailable or foreign artifact rejects the command with an error naming the
attachment and nothing is queued or dispatched; retries of an accepted command
return its receipt without rebinding. Staged artifacts, including attachments
sitting in unsent drafts, expire after 7 days, and a draft that references an
expired one is rejected at Send and must attach it again;
accepted ones live until their thread is deleted or project removed, whose rows
go in that transaction and whose files go after commit.

Agent support is checked at Send against the agent's probed prompt capabilities.
Images need `promptCapabilities.image`. Every other artifact without inline
text content needs `embeddedContext`, including binary files and text over the
64 KiB capture limit. The JSON size of the whole prompt's ACP content blocks
(prompt text, escaped text resources, base64 images and blobs) is computed
exactly at Send and bounded at 6 MiB, under the Claude bridge's 8 MiB frame and
the SDK's 10 MiB line. Unsupported or oversized attachments are refused with an
error naming the attachment, never dropped or converted. Dispatch repeats both
checks as a backstop and keeps the prompt queued if the live session no longer
advertises support or the bytes are missing or fail their digest. Artifact
resources always use `attachment://<artifact-id>/<name>`. The client-supplied
Source is display identity only and never becomes a file URI or path.
The built-in Claude bridge advertises and forwards images but not embedded
context. The Codex bridge advertises neither. Both therefore refuse
non-image files, and text over 64 KiB, at Send. The fixture accepts every artifact and only echoes its
metadata.

Draft previews use `GET /v1/preview` with the exact Send capture rules. A client
may record the preview digest as `PreviewSHA256`; Send still captures the
current file and sets `ChangedSincePreview` when it differs. An artifact
attachment may carry `PreviewSHA256` too; it is compared with the artifact's
digest. Previews stop when the server is stopping. At most four run at once, and
a slot is held until its read finishes, so reads stuck on a hung mount stay
bounded.

Consequences: artifact bytes are not in database backups or migrations, and
accepted attachments count against no budget until their thread is deleted.
Artifact retention, quotas and backup scope remain prototype choices to revisit
before a released storage format.
