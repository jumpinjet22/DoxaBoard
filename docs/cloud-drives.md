# Cloud drives

Screens can show photos straight from OneDrive (or SharePoint), Google Drive
or Dropbox. You connect a folder once in the admin console; after that,
adding or removing a picture in the cloud folder adds or removes it on the
screens within about ten minutes.

There is nothing to install on your own computer and no terminal involved.

## Connect a folder

1. In the console, open **Content → Cloud folders → Connect a folder**.
2. Pick the kind of drive. A link opens the provider's sign-in page in a new
   tab: sign in and approve access.
3. Your browser then lands on a page that **won't load**, with an address
   starting `http://localhost:53682/` (Google uses `http://127.0.0.1:53682/`).
   That is expected: the page belongs to the DoxaBoard server, not to your
   computer. Copy the whole address from the address bar, paste it into the
   console and press **Finish sign-in**.
4. Browse to the folder that holds the pictures and give the album a name.
5. In **Profiles**, pick that album for the slideshow.

Connecting a second folder from the same account skips the sign-in.

## What gets shown

- Pictures directly inside the folder: jpg, png, webp and gif, up to 50 MB
  each. Subfolders and other file types are ignored.
- The album mirrors the folder. A picture deleted from the cloud folder is
  deleted from the album at the next sync.
- An album fed by a cloud folder is owned by it: anything else put in that
  album's folder on the server is removed. DoxaBoard refuses to connect a
  cloud folder to an album that already has photos in it.

**Sync now** on a folder fetches immediately. **Remove** stops the syncing
and leaves the album's photos in place, so screens showing it don't go blank.

## What access DoxaBoard gets

| Drive | Access requested |
|---|---|
| OneDrive / SharePoint | Read-only |
| Google Drive | Read-only |
| Dropbox | Read and write (rclone has no read-only option for Dropbox). DoxaBoard only reads. |

The sign-in is handled by [rclone](https://rclone.org), using rclone's own
registered apps: that is why the consent screen says "rclone". The access
token goes from the provider straight to rclone on your server and is stored
in `deploy/server/rclone/rclone.conf`. It never passes through your browser
and DoxaBoard never sees it. Treat that file like a password, and keep it out
of backups you share.

To revoke access, press **disconnect** next to the account in the console,
and remove "rclone" from the account's connected apps on the provider's site.

## How it works

The `rclone` service in Docker Compose runs as a daemon that the server
drives over a unix socket in `deploy/server/run/`. It listens on no network
port. While you are connecting a drive it opens `127.0.0.1:53682` on the
server for the sign-in result, which the server passes on from the address
you paste.

## Troubleshooting

- **"The cloud drive service (rclone) isn't running":** `docker compose ps
  rclone` and `docker compose logs rclone`. The folders `photos`, `rclone`
  and `run` must be owned by user 65532 (see the quick start).
- **"That address is from a different sign-in":** each attempt has its own
  link. Start again and use the page you land on from the newest link.
- **"The drive's sign-in has expired or was revoked":** disconnect the
  account and connect it again.
- **Sign-in says the app is blocked or needs admin approval:** your
  Microsoft 365 or Google Workspace administrator restricts third-party
  apps. Ask them to allow rclone, or use a folder shared to a personal
  account.
- **Other drives** (S3, SMB shares, Box...): anything rclone supports works
  if you add the remote to `rclone.conf` yourself; it then appears under
  "Already signed in" when you connect a folder.
